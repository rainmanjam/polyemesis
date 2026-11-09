package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// ysGate, when set, holds every YouTube answer until it is closed.
var ysGate atomic.Pointer[chan struct{}]

func ysFixture(t *testing.T, calls *atomic.Int64, fail *atomic.Bool) (*Server, http.Handler, *db.DB, func(*http.Request), int64) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if g := ysGate.Load(); g != nil {
			<-*g
		}
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

// Requests that arrive together while the cache is cold share ONE refresh: three units, not three times three.
func TestConcurrentStudioEventReadsShareOneRefresh(t *testing.T) {
	var calls atomic.Int64
	var fail atomic.Bool
	_, h, _, sign, _ := ysFixture(t, &calls, &fail)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := jsonRequest(t, http.MethodGet, "/api/v1/youtube/studio-events", nil)
			sign(r)
			do(t, h, r)
		}()
	}
	wg.Wait()
	if n := calls.Load(); n != 3 {
		t.Errorf("eight simultaneous reads made %d YouTube calls, want one refresh of 3", n)
	}
}

// A caller that disconnects mid-refresh must not poison the cache for 15 minutes with its cancelled context.
func TestADisconnectingFirstCallerDoesNotCacheAnErrorForTheNextFifteenMinutes(t *testing.T) {
	var calls atomic.Int64
	var fail atomic.Bool
	s, _, _, _, _ := ysFixture(t, &calls, &fail)
	accts, _ := s.store.ListPlatformAccounts()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the first caller is already gone
	entry := s.studioEventsFor(ctx, accts[0], s.clock())
	if entry.err != "" || len(entry.events) == 0 {
		t.Errorf("entry = %+v: the refresh was cut short by the first caller's context", entry)
	}
}

// Cache hits never wait behind another account's slow refresh.
func TestACacheHitDoesNotWaitBehindAnotherAccountsSlowRefresh(t *testing.T) {
	var calls atomic.Int64
	var fail atomic.Bool
	s, _, store, _, a1 := ysFixture(t, &calls, &fail)
	a2 := connectAccount(t, store, s.box, db.PlatformYouTube, "chan2")
	accts, _ := s.store.ListPlatformAccounts()
	byID := map[int64]db.PlatformAccount{}
	for _, a := range accts {
		byID[a.ID] = a
	}
	clock := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	s.studioEventsFor(context.Background(), byID[a1], clock)
	s.studioEventsFor(context.Background(), byID[a2], clock)
	// account 1's entry goes stale; its refresh will hang
	s.ytStudioMu.Lock()
	e := s.ytStudio[a1]
	e.at = clock.Add(-time.Hour)
	s.ytStudio[a1] = e
	s.ytStudioMu.Unlock()
	gate := make(chan struct{})
	ysGate.Store(&gate)
	t.Cleanup(func() { ysGate.Store(nil) })
	done := make(chan struct{})
	go func() { s.studioEventsFor(context.Background(), byID[a1], clock); close(done) }()
	time.Sleep(100 * time.Millisecond) // the refresh is in flight
	got := make(chan studioCacheEntry, 1)
	go func() { got <- s.studioEventsFor(context.Background(), byID[a2], clock) }()
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Error("a cache hit waited behind another account's slow refresh")
	}
	close(gate)
	<-done
}
