package api

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/events"
)

// wsTestPing is short enough to keep the suite fast and long enough that a
// socket gets its initial burst out before the first tick.
const wsTestPing = 40 * time.Millisecond

// tokenIDByName finds the id of a minted token, which the create response does
// not return alongside the plaintext.
func tokenIDByName(t *testing.T, h http.Handler, sign func(*http.Request), name string) int64 {
	t.Helper()
	r := jsonRequest(t, http.MethodGet, "/api/v1/auth/tokens", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("list tokens: status %d: %s", w.Code, w.Body.String())
	}
	var toks []db.APIToken
	if err := json.Unmarshal(w.Body.Bytes(), &toks); err != nil {
		t.Fatalf("decode token list: %v: %s", err, w.Body.String())
	}
	for _, tok := range toks {
		if tok.Name == name {
			return tok.ID
		}
	}
	t.Fatalf("no token named %q in %v", name, toks)
	return 0
}

// dialWS opens a socket against a live server, with an optional bearer.
func dialWS(t *testing.T, srv *httptest.Server, header http.Header) *websocket.Conn {
	t.Helper()
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/ws"
	c, _, err := websocket.DefaultDialer.Dial(u, header)
	if err != nil {
		t.Fatalf("dial /api/v1/ws: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// The two non-code results drainUntilClosed can report.
//
// They are separated because they mean opposite things and both used to come
// back as -1: a TIMEOUT is the socket still being open, which is the failure
// these tests exist to catch, while any other read error is the socket having
// ended without a close frame -- which is a different fault entirely and is
// worth naming when it happens, because "close code = -1" sent a reader looking
// for a revocation that had in fact occurred.
const (
	wsStillOpen = -1
	wsAbruptEnd = -2
)

// drainUntilClosed reads until the socket closes or the deadline passes. It
// returns the close code, wsStillOpen or wsAbruptEnd.
func drainUntilClosed(t *testing.T, c *websocket.Conn, within time.Duration) int {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(within))
	for {
		if _, _, err := c.ReadMessage(); err != nil {
			var ce *websocket.CloseError
			if ok := asCloseError(err, &ce); ok {
				return ce.Code
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return wsStillOpen
			}
			return wsAbruptEnd
		}
	}
}

func asCloseError(err error, out **websocket.CloseError) bool {
	ce, ok := err.(*websocket.CloseError)
	if ok {
		*out = ce
	}
	return ok
}

// TestRevokingATokenClosesItsOpenSocket is #159, driven end to end.
//
// The defect: /ws resolves its principal ONCE, at upgrade, and never again. So
// the operator's only lever after a credential leaks -- revoke the token -- did
// not reach a socket that was already open. Deleting the row stopped the next
// REQUEST and did nothing about a connection that had already become a
// long-lived stream of status, logs and audio levels.
//
// This drives the real router, mints a real token, opens a real socket with it,
// revokes it through the real route, and asserts the socket CLOSES with a
// policy-violation code rather than merely going quiet. "Goes quiet" would pass
// against a server that had simply stopped publishing.
func TestRevokingATokenClosesItsOpenSocket(t *testing.T) {
	for _, scope := range []string{db.ScopeRead, db.ScopeAdmin} {
		t.Run(scope, func(t *testing.T) {
			h, _, sign := renditionServer(t, defaultTools())
			s := serverUnderTest(t, h)
			s.revokedMu.Lock()
			s.wsPingEvery = wsTestPing
			s.revokedMu.Unlock()

			const name = "leaked"
			tok := createScopedToken(t, h, sign, name, scope)
			id := tokenIDByName(t, h, sign, name)

			srv := httptest.NewServer(s.Handler())
			defer srv.Close()

			c := dialWS(t, srv, http.Header{"Authorization": {"Bearer " + tok}})

			// The positive control. Before the revoke the socket is LIVE, so a
			// close afterwards cannot be explained by the socket never having
			// worked. Without this the test would pass against a build where
			// /ws was broken outright.
			_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
			if _, _, err := c.ReadMessage(); err != nil {
				t.Fatalf("the socket delivered nothing before the revoke, so this test "+
					"cannot tell a revocation from a dead socket: %v", err)
			}

			r := jsonRequest(t, http.MethodDelete,
				"/api/v1/auth/tokens/"+strconv.FormatInt(id, 10), nil)
			sign(r)
			if w := do(t, h, r); w.Code != http.StatusOK {
				t.Fatalf("revoke: status %d: %s", w.Code, w.Body.String())
			}

			// The bound is ONE ping period. Ten of them is generous enough for a
			// loaded CI box and still fails loudly against a build that never
			// closes.
			code := drainUntilClosed(t, c, 10*wsTestPing+2*time.Second)
			if code != websocket.ClosePolicyViolation {
				t.Fatalf("close code = %d, want %d (policy violation). A revoked token's "+
					"socket must be CLOSED, not merely starved: a client that is still "+
					"connected is still authorised as far as anything else can tell.",
					code, websocket.ClosePolicyViolation)
			}
		})
	}
}

// TestRevokingATokenLeavesASessionSocketAlone is the other half, and it is the
// regression this fix could most easily have caused.
//
// The console opens two sockets per tab on a COOKIE, not a token. What must
// hold is that revoking an API TOKEN -- any token, all of them -- never touches
// a socket opened on a session. The two credentials are separate and a token
// revocation is not a statement about the operator's own browser.
//
// The rationale that used to sit here said session principals are "deliberately
// not re-evaluated on the tick". That was true when it was written and is false
// now: this same PR made the tick read the session's epoch, because a password
// change that left a socket streaming was the operator's emergency lever
// failing. The behaviour this test pins did not change; only the reason did.
// Corrected rather than deleted, because a stale rationale on a correct test is
// how the next reader concludes the tick does less than it does.
//
// TestChangingThePasswordClosesAnOpenSessionSocket is the other side: a token
// revocation leaves this socket alone, a password change does not.
func TestRevokingATokenLeavesASessionSocketAlone(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())
	s := serverUnderTest(t, h)
	s.revokedMu.Lock()
	s.wsPingEvery = wsTestPing
	s.revokedMu.Unlock()

	const name = "leaked"
	_ = createScopedToken(t, h, sign, name, db.ScopeAdmin)
	id := tokenIDByName(t, h, sign, name)

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	// A cookie socket, the way the console opens one. The dialer needs the
	// session cookie on the upgrade request, which is what sign attaches.
	req := jsonRequest(t, http.MethodGet, "/api/v1/ws", nil)
	sign(req)
	header := http.Header{}
	for _, ck := range req.Cookies() {
		header.Add("Cookie", ck.Name+"="+ck.Value)
	}
	c := dialWS(t, srv, header)

	r := jsonRequest(t, http.MethodDelete,
		"/api/v1/auth/tokens/"+strconv.FormatInt(id, 10), nil)
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("revoke: status %d: %s", w.Code, w.Body.String())
	}

	// Past several ticks, the session socket is still delivering. Publishing on
	// the bus rather than waiting for ambient traffic, so "still open" is
	// asserted by a frame arriving rather than by the absence of a close.
	time.Sleep(4 * wsTestPing)
	s.bus.Publish(events.TypeChatState, map[string]any{"state": "still here"})

	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		_, msg, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("the SESSION socket was closed by a token revocation (%v). The "+
				"console opens two of these per tab and holds no API token; closing them "+
				"here turns a security fix into a dashboard that drops out whenever an "+
				"operator tidies up their tokens.", err)
		}
		var ev struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(msg, &ev) == nil && ev.Type == string(events.TypeChatState) {
			return
		}
	}
}

// A DELETION MADE ANYWHERE IS SEEN BY AN OPEN SOCKET. #706.
//
// This test used to assert the opposite, and was right to: revocation's second
// half was an in-process set, and absence from it meant only "this process has
// not seen that token deleted". A deletion by another process, by
// handleChangePassword's DeleteAllAPITokens, or by an operator with sqlite3 was
// invisible to it -- so a socket opened with that token kept streaming.
//
// The old test closed with "if something else now writes to it, this test's
// premise is stale". Something else did: the set is gone, and the /ws tick asks
// the store. So the property inverts, and the stronger one is what is pinned
// here -- a token deleted BEHIND the handler's back still ends its socket.
//
// The second half is unchanged and still matters: requireAuth asks the database
// on every request, so this check is only ever the thing that closes a socket
// EARLY. It is not an authorisation source and must not become one.
func TestADeletionMadeOutsideTheHandlerStillEndsTheSocket(t *testing.T) {
	h, store, sign := renditionServer(t, defaultTools())
	s := serverUnderTest(t, h)

	const name = "deleted-behind-our-back"
	tok := createScopedToken(t, h, sign, name, db.ScopeAdmin)
	id := tokenIDByName(t, h, sign, name)

	// Delete through the STORE, bypassing the handler -- which is what another
	// process, a hand-edited database, or a password change looks like from in
	// here. The last of those is the one that was silently missed.
	if err := store.DeleteAPIToken(id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !s.tokenRevoked(id) {
		t.Fatal("a token deleted outside handleRevokeAPIToken is not seen as revoked, " +
			"so a socket opened with it would keep streaming. That is #706: the check " +
			"must read the store, not a set that one handler writes.")
	}

	// And a token that still exists is NOT reported revoked, or the check would
	// close every socket and pass this file by being uselessly strict.
	live := "still-here"
	createScopedToken(t, h, sign, live, db.ScopeAdmin)
	if s.tokenRevoked(tokenIDByName(t, h, sign, live)) {
		t.Error("a live token was reported revoked")
	}

	// The request is still refused, because requireAuth asks the database.
	r := jsonRequest(t, http.MethodGet, "/api/v1/status", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	if w := do(t, h, r); w.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/v1/status with a deleted token: status %d, want 401. This "+
			"check closes sockets early; it must never become the thing that decides "+
			"authorisation, which is still requireAuth's job.", w.Code)
	}
}
