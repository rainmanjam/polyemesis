package oauth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// twitchPollStub is a fake Helix /polls. It checks what every call must carry
// and answers with the documented shapes.
func twitchPollStub(t *testing.T, status int, body string, seen *[]string) *Twitch {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != twitchPollsPath {
			t.Errorf("unexpected request to %s", r.URL.Path)
		}
		if r.Header.Get("Client-Id") != "cid" || r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("%s %s without the Client-Id and bearer token", r.Method, r.URL)
		}
		b, _ := io.ReadAll(r.Body)
		*seen = append(*seen, r.Method+" "+r.URL.RawQuery+" "+string(b))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return NewTwitch(WithBaseURL(srv.URL))
}

const twitchPollBody = `{"data":[{"id":"ed96","broadcaster_id":"55","title":"Heads or Tails?",
 "choices":[{"id":"c1","title":"Heads","votes":7,"channel_points_votes":0,"bits_votes":0},
            {"id":"c2","title":"Tails","votes":3,"channel_points_votes":0,"bits_votes":0}],
 "status":"ACTIVE","duration":60}]}`

// TestTwitchCreatePollSendsTheDocumentedBody: broadcaster_id, title, choices
// as objects with a title each, duration in seconds -- and channel-points
// voting OFF, because #14 excludes channel points and a viewer buying extra
// votes would break one-vote-per-viewer.
func TestTwitchCreatePollSendsTheDocumentedBody(t *testing.T) {
	var seen []string
	tw := twitchPollStub(t, http.StatusOK, twitchPollBody, &seen)
	p, err := tw.CreatePoll(context.Background(), "cid", "tok", "55", "Heads or Tails?", []string{"Heads", "Tails"}, 60)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "ed96" || len(p.Votes) != 2 || p.Votes[0] != 7 || p.Votes[1] != 3 {
		t.Errorf("poll = %+v", p)
	}
	if len(seen) != 1 || !strings.HasPrefix(seen[0], "POST  ") {
		t.Fatalf("calls = %q", seen)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(seen[0], "POST  ")), &body); err != nil {
		t.Fatal(err)
	}
	if body["broadcaster_id"] != "55" || body["title"] != "Heads or Tails?" || body["duration"] != float64(60) ||
		body["channel_points_voting_enabled"] != false {
		t.Errorf("body = %v", body)
	}
	if c := body["choices"].([]any); len(c) != 2 || c[1].(map[string]any)["title"] != "Tails" {
		t.Errorf("choices = %v", body["choices"])
	}
}

func TestTwitchPollIsReadAndEndedByID(t *testing.T) {
	var seen []string
	tw := twitchPollStub(t, http.StatusOK, twitchPollBody, &seen)
	if _, err := tw.GetPoll(context.Background(), "cid", "tok", "55", "ed96"); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.EndPoll(context.Background(), "cid", "tok", "55", "ed96"); err != nil {
		t.Fatal(err)
	}
	if seen[0] != "GET broadcaster_id=55&id=ed96 " {
		t.Errorf("read = %q", seen[0])
	}
	if !strings.HasPrefix(seen[1], "PATCH  ") || !strings.Contains(seen[1], `"status":"TERMINATED"`) ||
		!strings.Contains(seen[1], `"id":"ed96"`) {
		t.Errorf("end = %q", seen[1])
	}
}

// TestTwitchPollRefusalSaysWhy: a channel that is not an Affiliate or
// Partner, or a token without the scope, is refused with 403; the error must
// say so rather than surface as an opaque failure on the poll.
func TestTwitchPollRefusalSaysWhy(t *testing.T) {
	var seen []string
	tw := twitchPollStub(t, http.StatusForbidden, `{"error":"Forbidden","status":403,"message":"The broadcaster is not a partner or affiliate"}`, &seen)
	_, err := tw.CreatePoll(context.Background(), "cid", "tok", "55", "q", []string{"a", "b"}, 60)
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("err = %v", err)
	}
}

func TestTwitchPollLimitsMatchTheDocumentation(t *testing.T) {
	ok := []string{"a", "b"}
	for name, tc := range map[string]struct {
		title   string
		choices []string
		secs    int
		fits    bool
	}{
		"fits":            {"q", ok, 60, true},
		"six choices":     {"q", []string{"a", "b", "c", "d", "e", "f"}, 60, false},
		"long title":      {strings.Repeat("x", 61), ok, 60, false},
		"long choice":     {"q", []string{"a", strings.Repeat("y", 26)}, 60, false},
		"over 30 minutes": {"q", ok, 1801, false},
		"under 15 s":      {"q", ok, 14, false},
		"unicode title":   {strings.Repeat("é", 60), ok, 1800, true},
	} {
		if reason := TwitchPollFits(tc.title, tc.choices, tc.secs); (reason == "") != tc.fits {
			t.Errorf("%s: TwitchPollFits = %q", name, reason)
		}
	}
}
