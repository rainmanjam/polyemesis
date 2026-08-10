package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/rainmanjam/polyemesis/internal/events"
)

// sessionSocket opens a /ws socket the way the console does: on the session
// cookie, with no API token anywhere.
func sessionSocket(t *testing.T, srv *httptest.Server, sign func(*http.Request)) *websocket.Conn {
	t.Helper()
	req := jsonRequest(t, http.MethodGet, "/api/v1/ws", nil)
	sign(req)
	header := http.Header{}
	for _, ck := range req.Cookies() {
		header.Add("Cookie", ck.Name+"="+ck.Value)
	}
	return dialWS(t, srv, header)
}

// TestChangingThePasswordClosesAnOpenSessionSocket is the SESSION half of #159,
// driven end to end.
//
// The token half of this fix -- close a socket whose API token was revoked --
// shipped first, and it deliberately skipped session principals with the
// reasoning that "sessions are already revoked wholesale by TokenEpoch on a
// password change". That sentence is true of REQUESTS and was false of an open
// socket: internal/api/ws.go never read the epoch, so the only reference to it
// was the comment claiming the coverage. Measured on the build that shipped
// that comment, through this exact fixture:
//
//	old session cookie on GET /api/v1/status  -> 401
//	the /ws socket opened with that same cookie -> STILL DELIVERING telemetry
//
// A password change is what an operator does when they believe somebody else is
// holding their session. handleChangePassword says the epoch bump "has to
// actually end that session". For the one principal whose only revocation lever
// IS a password change, it did not end the thing that was still streaming.
//
// Both halves are asserted, in this order, because the second is only
// interesting if the first holds: the 401 proves the epoch really did move, so
// a socket that stays open cannot be explained by a password change that did
// not take.
func TestChangingThePasswordClosesAnOpenSessionSocket(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())
	s := serverUnderTest(t, h)
	s.revokedMu.Lock()
	s.wsPingEvery = wsTestPing
	s.revokedMu.Unlock()

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	c := sessionSocket(t, srv, sign)

	// The positive control, as in TestRevokingATokenClosesItsOpenSocket: the
	// socket is LIVE before the password change, so a close afterwards cannot be
	// explained by /ws being broken.
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := c.ReadMessage(); err != nil {
		t.Fatalf("the session socket delivered nothing before the password change, so this "+
			"test cannot tell a revocation from a dead socket: %v", err)
	}

	chg := jsonRequest(t, http.MethodPost, "/api/v1/auth/password",
		map[string]string{"current": testPassword, "new": testPassword + "-rotated"})
	sign(chg)
	if w := do(t, h, chg); w.Code != http.StatusOK {
		t.Fatalf("change password: status %d: %s", w.Code, w.Body.String())
	}

	// HALF ONE: the epoch bump reaches ordinary requests. This passed on the
	// build that left the socket open, and it is here so that a failure of the
	// assertion below is unambiguous.
	stale := jsonRequest(t, http.MethodGet, "/api/v1/status", nil)
	sign(stale)
	if w := do(t, h, stale); w.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/v1/status with the pre-change session cookie: status %d, want 401. "+
			"The epoch bump is not reaching requests either, so the socket assertion below "+
			"would be measuring the wrong thing.", w.Code)
	}

	// HALF TWO: and it reaches the socket that was already open.
	code := drainUntilClosed(t, c, 10*wsTestPing+2*time.Second)
	if code != websocket.ClosePolicyViolation {
		t.Fatalf("close code = %d, want %d (policy violation).\n\n"+
			"A password change bumped the session epoch -- the request above got a 401 -- "+
			"and this socket, opened with the very cookie that just stopped working, was "+
			"still connected. A revocation that leaves a live telemetry stream open is the "+
			"operator's emergency lever failing (#159).",
			code, websocket.ClosePolicyViolation)
	}
}

// TestAnUnrelatedSettingsChangeLeavesASessionSocketAlone is the regression this
// fix could most easily have caused, and the reason the epoch is compared
// rather than the session re-verified.
//
// The console opens two sockets per tab. If the check on the tick were "is this
// cookie still the newest one", every ordinary re-issue would close the
// operator's dashboard and it would read as a fault in the product rather than
// as a security control. The epoch moves only on a password change, so ordinary
// traffic -- including writes through the same session -- must leave the socket
// completely alone.
func TestAnUnrelatedSettingsChangeLeavesASessionSocketAlone(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())
	s := serverUnderTest(t, h)
	s.revokedMu.Lock()
	s.wsPingEvery = wsTestPing
	s.revokedMu.Unlock()

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	c := sessionSocket(t, srv, sign)

	// A real write through the same session, several ping periods before the
	// assertion.
	get := jsonRequest(t, http.MethodGet, "/api/v1/settings", nil)
	sign(get)
	w := do(t, h, get)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/settings: status %d: %s", w.Code, w.Body.String())
	}
	var settings map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &settings); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	put := jsonRequest(t, http.MethodPut, "/api/v1/settings", settings)
	sign(put)
	if w := do(t, h, put); w.Code != http.StatusOK {
		t.Fatalf("PUT /api/v1/settings: status %d: %s", w.Code, w.Body.String())
	}

	time.Sleep(4 * wsTestPing)
	s.bus.Publish(events.TypeChatState, map[string]any{"state": "still here"})

	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		_, msg, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("the session socket was closed by ordinary authenticated traffic (%v). "+
				"Only a password change may end a session socket; anything looser turns the "+
				"console into a dashboard that drops out at random.", err)
		}
		var ev struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(msg, &ev) == nil && ev.Type == string(events.TypeChatState) {
			return
		}
	}
}
