package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

func ysFixture(t *testing.T, calls *atomic.Int64, fail *atomic.Bool) (*Server, http.Handler, *db.DB, func(*http.Request), int64) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		switch {
		case r.URL.Path == "/liveStreams":
			_, _ = io.WriteString(w, ypeStreams)
		case r.URL.Query().Get("broadcastStatus") == "upcoming":
			_, _ = io.WriteString(w, `{"items":[`+ypeEvent("ev-up", "ready", "stream-A", false)+`]}`)
		default:
			_, _ = io.WriteString(w, `{"items":[]}`)
		}
	}))
	t.Cleanup(srv.Close)
	s, h, store := testServerWith(t, Options{Providers: oauth.NewSet(oauth.WithBaseURL(srv.URL))})
	return s, h, store, login(t, h), connectAccount(t, store, s.box, db.PlatformYouTube, "chan")
}

// Studio's events reach the calendar read-only and without a key, polled at most once per 15 minutes per account,
// and a failed refresh serves the last answer marked stale instead of retrying every request.
func TestStudioEventsAreCachedForFifteenMinutesNeverCarryAKeyAndSurviveAFailedRefresh(t *testing.T) {
	var calls atomic.Int64
	var fail atomic.Bool
	s, h, store, sign, acct := ysFixture(t, &calls, &fail)
	ytpDest(t, store, acct, "main", ytpKeyA, "")
	clock := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	get := func() string {
		r := jsonRequest(t, http.MethodGet, "/api/v1/youtube/studio-events", nil)
		sign(r)
		w := do(t, h, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	body := get()
	if !strings.Contains(body, `"id":"ev-up"`) || !strings.Contains(body, "studio.youtube.com/video/ev-up/livestreaming") {
		t.Fatalf("body = %s", body)
	}
	for _, leak := range []string{ytpKeyA, ytpKeyB, "streamName", "ingestionAddress", "rtmp://"} {
		if strings.Contains(body, leak) {
			t.Errorf("the studio events carry %q: %s", leak, body)
		}
	}
	first := calls.Load()
	if first != 3 {
		t.Errorf("a refresh made %d calls, want the three the quota note says", first)
	}
	clock = clock.Add(14 * time.Minute)
	get()
	get()
	if calls.Load() != first {
		t.Errorf("polled again inside 15 minutes: %d calls", calls.Load()-first)
	}
	clock = clock.Add(2 * time.Minute)
	fail.Store(true)
	body = get()
	if !strings.Contains(body, `"id":"ev-up"`) || !strings.Contains(body, `"stale":["chan"]`) {
		t.Errorf("a failed refresh lost the last answer or did not say stale: %s", body)
	}
	after := calls.Load()
	get()
	if calls.Load() != after {
		t.Error("a failed refresh was retried on the next request instead of waiting out the interval")
	}
}

func proveYouTubeStudioEvents(t *testing.T) proofResult {
	var calls atomic.Int64
	var fail atomic.Bool
	_, h, store, sign, acct := ysFixture(t, &calls, &fail)
	ytpDest(t, store, acct, "main", ytpKeyA, "")
	read := createScopedToken(t, h, sign, "ledger-youtube-studio", db.ScopeRead)
	const path = "/api/v1/youtube/studio-events"
	code, admin := rawBodyOf(t, h, sign, path)
	if code != http.StatusOK || strings.Contains(admin, ytpKeyA) {
		t.Errorf("admin studio events: %d, key leaked: %v", code, strings.Contains(admin, ytpKeyA))
	}
	code, low := rawBodyOf(t, h, bearer(read), path)
	if code != http.StatusForbidden {
		t.Errorf("the read token's studio events answered %d, want 403", code)
	}
	return proofResult{
		Pattern:   patternDriven(t, h, http.MethodGet, path),
		High:      bodyOf(t, h, sign, "/api/v1/destinations"),
		Read:      low,
		Sentinels: []string{ytpKeyA},
	}
}
