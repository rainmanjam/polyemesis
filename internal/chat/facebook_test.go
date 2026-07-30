package chat

// Facebook is the only platform of the four with a reversible moderation
// primitive, because its live chat is a comment thread rather than a chat room.
// That makes hide worth having explicitly: a comment hidden in error costs an
// apology, and a comment deleted in error costs the thing itself.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

func fbAdapter(t *testing.T, base string, opts func(*FacebookConfig)) *FacebookAdapter {
	t.Helper()
	cfg := FacebookConfig{
		AccountRef:  "page:123",
		Channel:     "My Page",
		Token:       StaticToken("fb-token"),
		LiveVideoID: "live-1",
		APIBase:     base,
		Now:         fixedClock(time.Date(2026, 3, 1, 20, 0, 0, 0, time.UTC)),
	}
	if opts != nil {
		opts(&cfg)
	}
	a, err := NewFacebook(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestFacebookCommentNormalisation(t *testing.T) {
	a := fbAdapter(t, "http://example.invalid", nil)

	tests := []struct {
		name   string
		in     fbComment
		wantOK bool
		check  func(t *testing.T, m Message)
	}{
		{
			name: "graph's offset timestamp format is understood",
			in: fbComment{ID: "c1", Message: "nice stream", CreatedTime: "2026-03-01T19:59:00+0000",
				From: struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				}{ID: "u1", Name: "Viewer"}},
			wantOK: true,
			check: func(t *testing.T, m Message) {
				if m.ID != "c1" || m.Text != "nice stream" || m.Author.Name != "Viewer" {
					t.Fatalf("message = %+v", m)
				}
				if m.Platform != db.PlatformFacebook {
					t.Fatalf("platform = %q", m.Platform)
				}
				if !m.At.Equal(time.Date(2026, 3, 1, 19, 59, 0, 0, time.UTC)) {
					t.Fatalf("timestamp = %v", m.At)
				}
			},
		},
		{
			name:   "an RFC 3339 timestamp is also understood",
			in:     fbComment{ID: "c2", Message: "hi", CreatedTime: "2026-03-01T19:59:00Z"},
			wantOK: true,
			check: func(t *testing.T, m Message) {
				if !m.At.Equal(time.Date(2026, 3, 1, 19, 59, 0, 0, time.UTC)) {
					t.Fatalf("timestamp = %v", m.At)
				}
			},
		},
		{
			name:   "an unreadable timestamp falls back to the clock rather than losing the comment",
			in:     fbComment{ID: "c3", Message: "hi", CreatedTime: "yesterday"},
			wantOK: true,
			check: func(t *testing.T, m Message) {
				if m.At.IsZero() {
					t.Fatal("no timestamp at all")
				}
			},
		},
		{
			name:   "a comment with no text is not a message",
			in:     fbComment{ID: "c4", Message: "  "},
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, ok := a.messageFrom(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok {
				tc.check(t, m)
			}
		})
	}
}

func TestFacebookPollDeliversNewCommentsOldestFirstAndOnlyOnce(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer fb-token" {
			t.Errorf("authorization = %q", got)
		}
		if !strings.Contains(r.URL.RawQuery, "live_filter=no_filter") {
			t.Errorf("query = %q, want no_filter so nothing is silently hidden", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			// Newest first, which is how reverse_chronological returns them.
			fmt.Fprint(w, `{"data":[
				{"id":"c2","message":"second","created_time":"2026-03-01T19:59:30+0000","from":{"id":"u2","name":"Two"}},
				{"id":"c1","message":"first","created_time":"2026-03-01T19:59:00+0000","from":{"id":"u1","name":"One"}}]}`)
			return
		}
		// The same page again, plus one new comment.
		fmt.Fprint(w, `{"data":[
			{"id":"c3","message":"third","created_time":"2026-03-01T19:59:45+0000","from":{"id":"u3","name":"Three"}},
			{"id":"c2","message":"second","created_time":"2026-03-01T19:59:30+0000","from":{"id":"u2","name":"Two"}},
			{"id":"c1","message":"first","created_time":"2026-03-01T19:59:00+0000","from":{"id":"u1","name":"One"}}]}`)
	}))
	defer srv.Close()

	a := fbAdapter(t, srv.URL, nil)
	a.mu.Lock()
	a.lastSeen = time.Date(2026, 3, 1, 19, 55, 0, 0, time.UTC)
	a.mu.Unlock()

	var got []Message
	sink := SinkFunc(func(m Message) { got = append(got, m) })

	if _, err := a.poll(context.Background(), "live-1", sink); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Text != "first" || got[1].Text != "second" {
		t.Fatalf("first poll delivered %+v, want oldest first", got)
	}

	if _, err := a.poll(context.Background(), "live-1", sink); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[2].Text != "third" {
		t.Fatalf("second poll delivered %+v, want only the new comment", got)
	}
}

func TestFacebookWithoutALiveVideoIDExplainsItselfWithoutCallingTheAPI(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	defer srv.Close()

	sleeps := 0
	a := fbAdapter(t, srv.URL, func(c *FacebookConfig) {
		c.LiveVideoID = ""
		c.Sleep = func(ctx context.Context, _ time.Duration) bool {
			sleeps++
			return sleeps < 2
		}
	})

	if err := a.Run(context.Background(), SinkFunc(func(Message) {})); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatalf("the API was called %d times without a live video id", calls.Load())
	}
	h := a.Health()
	if h.State != StateDegraded {
		t.Fatalf("state = %q, want degraded", h.State)
	}
	if !strings.Contains(h.Detail, "pasted by hand") {
		t.Fatalf("detail = %q, want it to name the likely cause", h.Detail)
	}
}

func TestFacebookLiveVideoIDCanBeSetAfterTheAdapterStarts(t *testing.T) {
	a := fbAdapter(t, "http://example.invalid", func(c *FacebookConfig) { c.LiveVideoID = "" })
	if a.liveVideoID() != "" {
		t.Fatal("started with an id it was not given")
	}
	a.SetLiveVideoID("  live-42  ")
	if a.liveVideoID() != "live-42" {
		t.Fatalf("id = %q, want the trimmed value", a.liveVideoID())
	}
}

func TestFacebookErrorsAreClassifiedForTheOperator(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		wantDone  bool
		wantFatal bool
		wantState State
		wantSub   string
	}{
		{
			name: "a rejected token is fatal and says to reconnect", status: http.StatusUnauthorized,
			wantFatal: true, wantState: StateFailed, wantSub: "reconnect",
		},
		{
			name: "a missing live video means the broadcast ended", status: http.StatusNotFound,
			wantDone: true, wantState: StateStopped, wantSub: "ended",
		},
		{
			name: "a permissions refusal names the permission", status: http.StatusForbidden,
			wantState: StateDegraded, wantSub: "pages_read_engagement",
		},
		{
			name: "an unfamiliar failure is transient rather than terminal", status: http.StatusBadGateway,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, `{"error":{"message":"nope"}}`)
			}))
			defer srv.Close()

			a := fbAdapter(t, srv.URL, nil)
			_, err := a.poll(context.Background(), "live-1", SinkFunc(func(Message) {}))
			if err == nil {
				t.Fatal("a failing poll reported success")
			}
			done, fatal := a.classify(err)
			if done != tc.wantDone {
				t.Fatalf("done = %v, want %v", done, tc.wantDone)
			}
			if (fatal != nil) != tc.wantFatal {
				t.Fatalf("fatal = %v, want %v", fatal, tc.wantFatal)
			}
			if fatal != nil && !IsFatal(fatal) {
				t.Fatal("the fatal error is not marked fatal, so the Hub would keep retrying")
			}
			if tc.wantState != "" && a.Health().State != tc.wantState {
				t.Fatalf("state = %q, want %q", a.Health().State, tc.wantState)
			}
			if tc.wantSub != "" {
				detail := a.Health().Detail
				if fatal != nil {
					detail = fatal.Error()
				}
				if !strings.Contains(detail, tc.wantSub) {
					t.Fatalf("%q does not contain %q", detail, tc.wantSub)
				}
			}
			if strings.Contains(err.Error(), "fb-token") {
				t.Fatal("the access token leaked into the error")
			}
		})
	}
}

func TestNewFacebookWithoutATokenIsAConfigurationState(t *testing.T) {
	_, err := NewFacebook(FacebookConfig{})
	if err == nil {
		t.Fatal("a missing token was accepted")
	}
	if !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("error %q does not read as a configuration state", err)
	}
}

// ---------------------------------------------------------------- moderation

// Facebook's live chat IS the comment thread, so moderating it is comment
// moderation: DELETE /{comment_id}, with no chat-specific endpoint to find.
func TestFacebookDeleteRemovesTheComment(t *testing.T) {
	var method, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		fmt.Fprint(w, `{"success":true}`)
	}))
	defer srv.Close()

	a := fbAdapter(t, srv.URL, nil)
	if err := a.Delete(context.Background(), "comment-9"); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodDelete {
		t.Fatalf("method = %s, want DELETE", method)
	}
	if !strings.HasSuffix(path, "/comment-9") {
		t.Fatalf("path = %q, want the comment id as the node", path)
	}
}

// Hide is the reversible one, and the only such primitive across all four
// platforms. It must send is_hidden rather than deleting anything.
func TestFacebookHideIsReversibleAndNotADelete(t *testing.T) {
	for _, hidden := range []bool{true, false} {
		var method string
		var body map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			method = r.Method
			json.NewDecoder(r.Body).Decode(&body)
			fmt.Fprint(w, `{"success":true}`)
		}))

		a := fbAdapter(t, srv.URL, nil)
		if err := a.Hide(context.Background(), "comment-9", hidden); err != nil {
			t.Fatal(err)
		}
		if method == http.MethodDelete {
			t.Fatal("Hide issued a DELETE; hiding must not destroy the comment")
		}
		if got, ok := body["is_hidden"].(bool); !ok || got != hidden {
			t.Fatalf("body = %+v, want is_hidden=%v", body, hidden)
		}
		srv.Close()
	}
}

// A 403 here is the permission story that costs people a day: an app can read a
// Page's comments and still be unable to act on them without MODERATE.
func TestFacebookModerationNamesTheModeratePermission(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"message":"permissions"}}`)
	}))
	defer srv.Close()

	a := fbAdapter(t, srv.URL, nil)
	err := a.Delete(context.Background(), "comment-9")
	if err == nil {
		t.Fatal("a 403 was reported as success")
	}
	if !strings.Contains(err.Error(), "MODERATE") {
		t.Fatalf("error = %q, want it to name the MODERATE task permission", err)
	}
	if IsFatal(err) {
		t.Fatal("a permissions 403 was Fatal, which would tear down a working comment poll")
	}
}

func TestFacebookModerationRefusesABlankID(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	a := fbAdapter(t, srv.URL, nil)
	if err := a.Delete(context.Background(), "  "); err == nil {
		t.Fatal("a blank comment id was accepted for delete")
	}
	if err := a.Hide(context.Background(), "  ", true); err == nil {
		t.Fatal("a blank comment id was accepted for hide")
	}
	if called {
		t.Fatal("a blank comment id reached the Graph API")
	}
}
