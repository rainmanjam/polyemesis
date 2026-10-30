package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/eventstream"
)

// The route-coverage ledger's view of the overlay key's two routes. The sweep
// sees only their 401 (it holds no overlay key); these counterparts open them
// WITH the key, on the fixture whose destination key is planted, and show the
// overlay principal's bytes carry none of it -- against the admin's expert view,
// which does.

// ledgerOverlayKey creates an overlay through the real API and returns its key.
func ledgerOverlayKey(t *testing.T, h http.Handler, sign func(*http.Request)) string {
	t.Helper()
	return createOverlay(t, h, sign, "ledger", nil).OBSKey
}

func proveOverlayConfig(t *testing.T) proofResult {
	h, _, sign := runningDestServer(t)
	key := ledgerOverlayKey(t, h, sign)
	const path = "/api/v1/overlay/config"
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set(OverlayKeyHeader, key)
	w := do(t, h, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"preset"`) {
		t.Errorf("the overlay key did not reach its own config (%d), so its bytes prove nothing: %s",
			w.Code, truncateForFailure(w.Body.String()))
	}
	return proofResult{
		Pattern: patternDriven(t, h, http.MethodGet, path),
		High:    bodyOf(t, h, sign, "/api/v1/destinations/1/expert"),
		Read:    w.Body.String(),
	}
}

func proveOverlayDisplayStream(t *testing.T) proofResult {
	h, _, sign := runningDestServer(t)
	s := serverUnderTest(t, h)
	key := ledgerOverlayKey(t, h, sign)

	hub := eventstream.NewHub()
	prev := s.stream
	s.stream = hub
	defer func() { s.stream = prev }()
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	const path = "/api/v1/overlay/events"
	req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	req.Header.Set(OverlayKeyHeader, key)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("open the overlay stream: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		_ = res.Body.Close()
		t.Fatalf("the overlay stream answered %d", res.StatusCode)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !hub.HasSubscribers() {
		if time.Now().After(deadline) {
			t.Fatal("the overlay handler never subscribed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	plantEvents(hub)
	hub.Publish(eventstream.Envelope{Type: eventstream.TypeChatDisplay, Data: eventstream.DisplayLine{
		ID: "ledger", Platform: "twitch", Name: "viewer", Text: "hello from the ledger"}})

	var b strings.Builder
	done := make(chan struct{})
	go func() { _, _ = io.Copy(&b, res.Body); close(done) }()
	time.Sleep(750 * time.Millisecond)
	_ = res.Body.Close()
	<-done
	body := b.String()
	if !strings.Contains(body, "event: "+string(eventstream.TypeChatDisplay)) {
		t.Errorf("the overlay stream delivered nothing the proof planted, so the absence of "+
			"the sentinel in it proves nothing:\n%s", truncateForFailure(body))
	}
	return proofResult{
		Pattern: patternDriven(t, h, http.MethodGet, path),
		High:    bodyOf(t, h, sign, "/api/v1/destinations/1/expert"),
		Read:    body,
	}
}
