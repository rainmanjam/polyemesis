package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/events"
)

// tokenRevoked is the store-backed replacement for the in-process revoked map
// (#706). Its three arms are three different decisions, and only one of them is
// exercised by an end-to-end revoke test, so they are pinned here directly.
func TestTokenRevokedAsksTheStoreAndFailsClosed(t *testing.T) {
	s, _, store := testServer(t, config.Config{})

	tok, _, err := store.CreateAPIToken("live", string(db.ScopeAdmin))
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	if s.tokenRevoked(tok.ID) {
		t.Error("a token that is still in the store reports as revoked; every " +
			"socket opened with a working credential would be closed on its " +
			"first ping")
	}

	// A SESSION principal carries no token id, and passes zero here on every
	// ping. It must not become a database read, and must never read as revoked.
	if s.tokenRevoked(0) {
		t.Error("a session socket (token id 0) reports as revoked")
	}

	if err := store.DeleteAPIToken(tok.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !s.tokenRevoked(tok.ID) {
		t.Error("a deleted token does not report as revoked -- the socket " +
			"opened with it keeps streaming, which is the whole of #706")
	}

	// FAIL CLOSED. An unreadable store is not a reason to keep streaming to a
	// client whose credential can no longer be checked. Closing the database
	// under the server is the only way to reach the error arm without a fake.
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !s.tokenRevoked(tok.ID) {
		t.Error("an unreadable store reports the token as surviving; this arm " +
			"fails OPEN, and a store this socket cannot read is one requireAuth " +
			"cannot read either")
	}
}

// requireScope with no principal in the context is a 401, not a pass. #710.
//
// Mounted without requireAuth, or after it in the wrong order, the old code
// waved the request through with no scope enforcement at all and no signal that
// it had. Every live group carries requireAuth too, so this changes no shipped
// behaviour -- it removes the affordance, and that is only worth having if
// something watches it.
func TestRequireScopeRefusesARequestWithNoPrincipal(t *testing.T) {
	s, _, _ := testServer(t, config.Config{})

	reached := false
	h := s.requireScope(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached = true
	}))

	w := httptest.NewRecorder()
	// No requireAuth ahead of it, so the context carries no principal: exactly
	// the mis-mounting the guard exists for.
	h.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/v1/sources/1", nil))

	if reached {
		t.Error("requireScope passed a request carrying no principal straight " +
			"through to the handler, with no scope enforcement anywhere")
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

// Every event type must have a stated policy. #716.
//
// wsPolicy fails closed on an unrecognised type, which is right -- but
// wsPassthrough is iota, so it was ZERO, and it reached the fail-closed default
// through having no case of its own. Everything ordinary a socket carries
// travels as a passthrough, so the whole feed depended on a default that a
// later reordering of the constants would silently claim.
func TestEveryEventClassHasItsOwnCaseInTheWebSocketPolicy(t *testing.T) {
	ev := events.Event{Type: events.TypeLog, Data: map[string]any{"line": "hello"}}
	// READ-SCOPED, because an admin socket returns before the switch is
	// consulted at all -- so only a read-scoped socket exercises the policy.
	out, send := eventView(ev, true)
	if !send {
		t.Fatal("an ordinary passthrough event is dropped by the policy: this " +
			"is a silent, total loss of the log/status/levels/stats feed, and " +
			"it is what an unstated policy for wsPassthrough costs")
	}
	if out.Type != ev.Type {
		t.Errorf("passthrough rewrote the event type: %q -> %q", ev.Type, out.Type)
	}

	// The other half of the device: a type nobody stated is still refused.
	if _, send := eventView(events.Event{Type: "a-type-no-build-has-ever-seen"}, true); send {
		t.Error("an unclassified event type is forwarded; the fail-closed " +
			"default has been lost")
	}
}

// The SECOND fail-closed arm: a policy CONSTANT with no case. #716.
//
// The arm above refuses an event type nobody classified. This one refuses a
// classification nobody implemented -- the switch's default, which used to
// `return ev, true` and would therefore have sent a read-scoped socket the
// ADMIN shape of any event whose new policy constant someone forgot to handle.
// That is the one direction a widening must never take.
//
// The only way to reach it is to be the mistake: a policy value this build has
// no case for, planted in the table and taken back out. Reaching it is the
// point -- a guard nobody has watched fail is a guard nobody should trust.
func TestAPolicyConstantWithNoCaseWithholdsTheEvent(t *testing.T) {
	const planted = events.Type("planted-for-the-fail-closed-default")
	const noSuchPolicy = wsPolicy(126) // no case in eventView, by construction

	wsEventPolicy[planted] = noSuchPolicy
	t.Cleanup(func() { delete(wsEventPolicy, planted) })

	ev := events.Event{Type: planted, Data: map[string]any{"streamKey": "live_abc"}}
	out, send := eventView(ev, true)
	if send {
		t.Fatalf("a policy constant with no case sent the event through unredacted "+
			"to a read-scoped socket: %+v.\n"+
			"    Adding a wsPolicy value and forgetting its case here is a silent "+
			"WIDENING -- the read socket receives the admin shape -- and it is the "+
			"one direction that must never happen by omission.", out)
	}
	if out.Type != "" {
		t.Errorf("the withheld event is not zeroed: %+v", out)
	}
}
