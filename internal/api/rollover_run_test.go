package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/hooks"
)

// The manual run is the operator's button. These tests pin what it reports: a rollover the platform refused is an
// error, the platform call is not cancelled by the browser going away, and a credential in the platform's error text
// never reaches the timeline, the result, the event stream or the HTTP body.

// runTokenFixture is a credential-shaped string built at runtime, so the fixture never looks like a literal secret
// to the scanner. The redaction tests look for it in every place a failure is written.
func runTokenFixture() string {
	return "ya29." + strings.Repeat("Q", 24)
}

// TestAManualRunThatThePlatformRefusesReturnsAnErrorNotASuccess is the medium finding. The sweep path and the manual
// path share act, and act records a refusal rather than returning it, so before the fix RunNow answered nil and the
// handler answered 202 "rolling over" for a bind that YouTube had refused. The operator must get the error.
func TestAManualRunThatThePlatformRefusesReturnsAnErrorNotASuccess(t *testing.T) {
	r := newRolloverRig(t)
	r.addDest(1, db.PlatformYouTube, "live-1")
	r.yt.err = errors.New("the replacement would not start")

	err := r.c.RunNow(context.Background(), 1)
	if !errors.Is(err, errRolloverFailed) {
		t.Fatalf("RunNow after a platform refusal = %v, want errRolloverFailed", err)
	}
	if !hasEvent(r.announced, hooks.TriggerRolloverFailed) {
		t.Errorf("the refusal was not also announced on the timeline and the webhook")
	}
}

// TestAManualRunKeepsGoingWhenTheBrowserDisconnectsMidRollover is the low finding on request context. The handler
// passes the request context, so a disconnect cancelled the platform call between create and bind. RunNow must
// detach from the request's cancellation and the platform must see a live context.
func TestAManualRunKeepsGoingWhenTheBrowserDisconnectsMidRollover(t *testing.T) {
	r := newRolloverRig(t)
	r.addDest(1, db.PlatformYouTube, "live-1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := r.c.RunNow(ctx, 1); err != nil {
		t.Fatalf("RunNow with a cancelled request context = %v, want the rollover to run", err)
	}
	if len(r.yt.rolls) != 1 {
		t.Fatalf("rollovers = %d, want 1", len(r.yt.rolls))
	}
	if r.yt.ctxErr != nil {
		t.Errorf("the platform call saw a cancelled context (%v); a disconnect must not abandon a rollover mid-way", r.yt.ctxErr)
	}
}

// TestAManualRunNeverWritesACredentialFromAPlatformErrorAnywhere is the low finding on redaction. The platform's error
// text can carry a bearer credential (a statusError includes a body snippet). It must be masked before it reaches the
// result row, the timeline, the announced event, the returned error and the HTTP body.
func TestAManualRunNeverWritesACredentialFromAPlatformErrorAnywhere(t *testing.T) {
	r := newRolloverRig(t)
	r.addDest(1, db.PlatformYouTube, "live-1")
	tok := runTokenFixture()
	r.yt.err = fmt.Errorf("refused: Authorization: Bearer %s", tok)

	err := r.c.RunNow(context.Background(), 1)
	if err == nil {
		t.Fatal("RunNow after a refusal returned nil")
	}
	seen := []string{err.Error()}
	seen = append(seen, r.store.results...)
	for _, ev := range r.store.timelineRows() {
		seen = append(seen, fmt.Sprintf("%+v", ev))
	}
	for _, ev := range r.announced {
		seen = append(seen, fmt.Sprintf("%+v", ev))
	}
	for i, text := range seen {
		if strings.Contains(text, tok) {
			t.Errorf("sink %d carries the platform credential: %q", i, text)
		}
	}
}

// TestAManualRunThatSucceedsSaysSoOnceItHasFinished checks the success answer. The call is synchronous, so by the time
// the handler answers the rollover is done. Answering 202 "rolling over" describes a rollover still to come, which
// is not what happened, so the answer is 200 with "rolled over".
func TestAManualRunThatSucceedsSaysSoOnceItHasFinished(t *testing.T) {
	r := newRolloverRig(t)
	r.addDest(1, db.PlatformYouTube, "live-1")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/destinations/1/rollover/run", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	(&Server{rollover: r.c}).handleRunRollover(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 once the rollover has finished; body %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "rolled over") {
		t.Errorf("body = %s, want status rolled over", w.Body.String())
	}
}

// TestAManualRunWhoseLiveStartReadFailsNeverWritesACredentialToTheResponse covers the path before the rollover
// proper. The live-start read runs first, and its error is not inside act, so it reaches the handler's 500 branch.
// That branch must redact too.
func TestAManualRunWhoseLiveStartReadFailsNeverWritesACredentialToTheResponse(t *testing.T) {
	r := newRolloverRig(t)
	r.addDest(1, db.PlatformYouTube, "live-1")
	tok := runTokenFixture()
	r.yt.startErr = fmt.Errorf("liveBroadcasts.list: Authorization: Bearer %s", tok)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/destinations/1/rollover/run", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	(&Server{rollover: r.c}).handleRunRollover(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 for a failed live-start read; body %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), tok) {
		t.Errorf("the 500 body carries the platform credential: %s", w.Body.String())
	}
}

// TestAManualRunRefusedByThePlatformAnswers502WithTheRedactedReason checks the HTTP mapping. A refusal by the
// platform is an upstream failure, so the answer is 502 with the reason, and the body carries no credential.
func TestAManualRunRefusedByThePlatformAnswers502WithTheRedactedReason(t *testing.T) {
	r := newRolloverRig(t)
	r.addDest(1, db.PlatformYouTube, "live-1")
	tok := runTokenFixture()
	r.yt.err = fmt.Errorf("refused: Authorization: Bearer %s", tok)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/destinations/1/rollover/run", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	(&Server{rollover: r.c}).handleRunRollover(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 for a platform refusal; body %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), tok) {
		t.Errorf("the 502 body carries the platform credential: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "refused") {
		t.Errorf("the 502 body does not say why: %s", w.Body.String())
	}
}
