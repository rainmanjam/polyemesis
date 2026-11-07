package oauth

// Twitch's viewer stats, which are a read and only a read.
//
// The provider's other behaviour is exercised from metadata_test.go (title and
// category push), credcheck_providers_test.go (the client-credentials probe)
// and endpoints_test.go (nothing escapes to api.twitch.tv). This file is the
// stats surface, and its fixtures are shaped like Get Streams responses rather
// than like the struct that decodes them -- the failure kick_test.go's stats
// section records is a fake that agreed with the code instead of the platform.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// twitchStatsStub aims a provider at a handler that answers both calls Stats
// makes: /users, to resolve the broadcaster id, and /streams for the read
// itself. Both bases move together -- see WithBaseURL -- so no request can
// reach the real Helix.
func twitchStatsStub(t *testing.T, streams http.HandlerFunc, users http.HandlerFunc) *Twitch {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/users":
			if users != nil {
				users(w, r)
				return
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"4242","login":"dj","display_name":"DJ"}]}`))
		case twitchStreamsPath:
			streams(w, r)
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return NewTwitch(WithBaseURL(srv.URL))
}

func TestTwitchStatsReportsLivenessWithoutInventingAViewerCount(t *testing.T) {
	viewers := func(n int) *int { return &n }

	tests := []struct {
		name        string
		body        string
		status      int
		usersStatus int
		wantLive    bool
		wantViewers *int
		wantTitle   string
		wantCat     string
		wantLang    string
		wantZeroAt  bool
		wantErr     bool
	}{
		{
			name: "a live channel carries the count and the metadata",
			body: `{"data":[{"user_id":"4242","user_login":"dj","type":"live",` +
				`"title":"Friday night set","game_name":"Just Chatting","language":"en",` +
				`"started_at":"2026-08-16T20:00:00Z","viewer_count":1312}]}`,
			wantLive:    true,
			wantViewers: viewers(1312),
			wantTitle:   "Friday night set",
			wantCat:     "Just Chatting",
			wantLang:    "en",
		},
		{
			// The case the pointer exists for. An offline channel is absent
			// from the response entirely, so Twitch reported no audience of
			// none -- it reported nothing, and a pointer to 0 here would be a
			// number polyemesis made up.
			name:        "an offline channel is an answer with no count at all, not a count of zero",
			body:        `{"data":[]}`,
			wantLive:    false,
			wantViewers: nil,
			wantZeroAt:  true,
		},
		{
			// The other half of the distinction, and the reason the count is
			// not simply dropped when it is zero the way Kick's is: Twitch
			// documents no opt-out value, so a zero that arrives is a real
			// audience of none and is passed on as one.
			name: "a live channel Twitch says has nobody watching reports zero, because Twitch sent zero",
			body: `{"data":[{"user_id":"4242","user_login":"dj","type":"live",` +
				`"title":"soundcheck","viewer_count":0,"started_at":"2026-08-16T20:00:00Z"}]}`,
			wantLive:    true,
			wantViewers: viewers(0),
			wantTitle:   "soundcheck",
		},
		{
			name: "a live channel whose count is absent from the body reports no count",
			body: `{"data":[{"user_id":"4242","user_login":"dj","type":"live",` +
				`"title":"soundcheck","started_at":"2026-08-16T20:00:00Z"}]}`,
			wantLive:    true,
			wantViewers: nil,
			wantTitle:   "soundcheck",
		},
		{
			// type is documented as an empty string when an error occurs, so
			// liveness is read off presence in the array instead. A channel
			// reported offline here would be a live stream shown as dark.
			name: "presence in the array is liveness even when type is the empty string Twitch documents",
			body: `{"data":[{"user_id":"4242","user_login":"dj","type":"",` +
				`"viewer_count":7,"started_at":"2026-08-16T20:00:00Z"}]}`,
			wantLive:    true,
			wantViewers: viewers(7),
		},
		{
			name: "an unreadable started_at costs the timestamp, not the read",
			body: `{"data":[{"user_id":"4242","user_login":"dj","type":"live",` +
				`"viewer_count":7,"started_at":"last tuesday"}]}`,
			wantLive:    true,
			wantViewers: viewers(7),
			wantZeroAt:  true,
		},
		{
			name:    "a refused Get Streams is a real error, not an offline channel",
			body:    `{"error":"Internal Server Error","status":500}`,
			status:  http.StatusInternalServerError,
			wantErr: true,
		},
		{
			// Stats cannot even ask without the broadcaster id, so a rejected
			// token has to surface rather than read as "not live".
			name:        "a rejected token surfaces from the user lookup",
			usersStatus: http.StatusUnauthorized,
			wantErr:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var users http.HandlerFunc
			if tc.usersStatus != 0 {
				users = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(tc.usersStatus) }
			}
			tw := twitchStatsStub(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.usersStatus != 0 {
					t.Error("Get Streams was called even though the user lookup failed")
				}
				if got := r.URL.Query().Get("user_id"); got != "4242" {
					t.Errorf("user_id = %q, want the id Account resolved", got)
				}
				// Get Streams defaults type to "all". Liveness is read off
				// presence in the response, so without this filter any non-live
				// entry Twitch returned would report the channel live.
				if got := r.URL.Query().Get("type"); got != "live" {
					t.Errorf("type = %q, want \"live\" -- the default is \"all\"", got)
				}
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				_, _ = w.Write([]byte(tc.body))
			}, users)

			got, err := tw.Stats(context.Background(), "cid", "tok")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want an error, got %#v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Stats: %v", err)
			}
			if got.Live != tc.wantLive {
				t.Errorf("Live = %v, want %v", got.Live, tc.wantLive)
			}
			switch {
			case tc.wantViewers == nil && got.ViewerCount != nil:
				t.Errorf("ViewerCount = %d, want nil -- no count reported at all", *got.ViewerCount)
			case tc.wantViewers != nil && got.ViewerCount == nil:
				t.Errorf("ViewerCount was not reported, want %d", *tc.wantViewers)
			case tc.wantViewers != nil && *got.ViewerCount != *tc.wantViewers:
				t.Errorf("ViewerCount = %d, want %d", *got.ViewerCount, *tc.wantViewers)
			}
			if got.Title != tc.wantTitle {
				t.Errorf("Title = %q, want %q", got.Title, tc.wantTitle)
			}
			if got.Category != tc.wantCat {
				t.Errorf("Category = %q, want %q", got.Category, tc.wantCat)
			}
			if got.Language != tc.wantLang {
				t.Errorf("Language = %q, want %q", got.Language, tc.wantLang)
			}
			// Absent, not the zero time -- see LiveStats.StartedAt. An offline
			// channel has no start time to report and must not serialise one.
			if (got.StartedAt == nil) != tc.wantZeroAt {
				t.Errorf("StartedAt = %v, want absent=%v", got.StartedAt, tc.wantZeroAt)
			}
			if got.Source != twitchStreamsPath {
				t.Errorf("Source = %q, want %q so a disputed number can be traced to the endpoint that gave it",
					got.Source, twitchStreamsPath)
			}
		})
	}
}

// TestTwitchStatsSendsTheClientIdOnEveryHelixCall guards the header Helix
// refuses the request without. Both calls are checked rather than one: Stats
// makes two, and a Client-Id present on the user lookup alone would still 401
// on the read that matters.
func TestTwitchStatsSendsTheClientIdOnEveryHelixCall(t *testing.T) {
	seen := map[string]string{}
	record := func(w http.ResponseWriter, r *http.Request, body string) {
		seen[r.URL.Path] = r.Header.Get("Client-Id")
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("%s Authorization = %q, want the bearer token", r.URL.Path, got)
		}
		_, _ = w.Write([]byte(body))
	}
	tw := twitchStatsStub(t,
		func(w http.ResponseWriter, r *http.Request) {
			record(w, r, `{"data":[{"user_id":"4242","user_login":"dj","viewer_count":3}]}`)
		},
		func(w http.ResponseWriter, r *http.Request) {
			record(w, r, `{"data":[{"id":"4242","login":"dj","display_name":"DJ"}]}`)
		})

	if _, err := tw.Stats(context.Background(), "the-client-id", "tok"); err != nil {
		t.Fatalf("Stats: %v", err)
	}
	for _, path := range []string{"/users", twitchStreamsPath} {
		if seen[path] != "the-client-id" {
			t.Errorf("Client-Id on %s = %q, want the app's client id", path, seen[path])
		}
	}
}

// TestTwitchIngestIsRTMPS: connecting an account hands back the TLS ingest on
// 443 (the maintainer's decision), not plain RTMP on 1935. Twitch's
// live.twitch.tv is not an IVS host, so the IVS /app-path rule leaves it alone
// -- the path is already /app.
func TestTwitchIngestIsRTMPS(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/users":
			_, _ = w.Write([]byte(`{"data":[{"id":"4242","login":"dj","display_name":"DJ"}]}`))
		case "/streams/key":
			_, _ = w.Write([]byte(`{"data":[{"stream_key":"live_4242_current"}]}`))
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	ing, err := NewTwitch(WithBaseURL(srv.URL)).Ingest(context.Background(), "cid", "tok")
	if err != nil {
		t.Fatal(err)
	}
	if want := "rtmps://live.twitch.tv:443/app"; ing.URL != want {
		t.Fatalf("Ingest URL = %q, want %q", ing.URL, want)
	}
	if ing.Key != "live_4242_current" {
		t.Fatalf("Ingest key = %q", ing.Key)
	}
}

// Twitch requires every app that holds a user's token to call /oauth2/validate
// at startup and hourly, and to end the sessions that use the token when it
// answers 401 (dev.twitch.tv/docs/authentication/validate-tokens).
func TestTwitchValidateTellsAValidTokenFromARevokedOne(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		invalid bool
		wantErr bool
	}{
		{"valid", http.StatusOK, `{"client_id":"c","login":"dj","user_id":"4242","scopes":[],"expires_in":5000}`, false, false},
		{"revoked", http.StatusUnauthorized, `{"status":401,"message":"invalid access token"}`, true, true},
		{"server error is not a verdict", http.StatusInternalServerError, `oops`, false, true},
		{"rate limit is not a verdict", http.StatusTooManyRequests, `slow`, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotAuth, gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			err := NewTwitch(WithBaseURL(srv.URL)).Validate(context.Background(), "tok-123")
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got := IsTokenInvalid(err); got != tc.invalid {
				t.Fatalf("IsTokenInvalid(%v) = %v, want %v", err, got, tc.invalid)
			}
			if gotPath != "/oauth2/validate" {
				t.Errorf("path = %q, want /oauth2/validate", gotPath)
			}
			if gotAuth != "OAuth tok-123" {
				t.Errorf("Authorization = %q, want the OAuth scheme the docs use", gotAuth)
			}
		})
	}
}

// Twitch refuses a revoked refresh token with a 400 "Invalid refresh token" and
// no invalid_grant, and that has to read as "reconnect", not as a failure to retry.
func TestTwitchRefreshOfARevokedGrantRequiresReconnect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"Bad Request","status":400,"message":"Invalid refresh token"}`))
	}))
	defer srv.Close()
	_, err := NewTwitch(WithBaseURL(srv.URL)).Refresh(context.Background(), "cid", "sec", "rt")
	if !errors.Is(err, ErrReconnectRequired) {
		t.Fatalf("err = %v, want it to match ErrReconnectRequired", err)
	}
}

// The Twitch-specific phrase must not widen every platform's reconnect rule.
func TestInvalidRefreshTokenPhraseIsTwitchOnly(t *testing.T) {
	if grantGone(`{"message":"Invalid refresh token"}`) {
		t.Fatal("grantGone, which every provider shares, matches Twitch's phrase")
	}
}
