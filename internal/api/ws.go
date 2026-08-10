package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"github.com/rainmanjam/polyemesis/internal/events"
)

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = 25 * time.Second
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 32 * 1024,
	// Same-origin only. The session cookie is SameSite=Lax, but WebSocket
	// upgrades are not covered by SameSite in every browser, so the origin
	// check is the actual defence against cross-site socket hijacking.
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true // non-browser client
		}
		return sameHost(origin, r.Host)
	},
}

func sameHost(origin, host string) bool {
	for _, prefix := range []string{"http://", "https://"} {
		if len(origin) > len(prefix) && origin[:len(prefix)] == prefix {
			return origin[len(prefix):] == host
		}
	}
	return false
}

// handleWS streams live telemetry: status, audio levels, logs and stats.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	// The principal, captured BEFORE the upgrade while there is still a request
	// to read it from. Everything written to this socket goes through eventView
	// with this flag; writeEvent used to be a bare json.Marshal with no
	// principal anywhere in its signature, which is why every event reached
	// every socket in its admin shape.
	//
	// A SNAPSHOT of the SCOPE, and knowingly so: requireAuth and requireScope
	// have already run on the upgrade request, and the scope is not re-derived
	// afterwards, so a token whose scope is DOWNGRADED mid-session keeps this
	// value until the socket closes. That residual is now bounded rather than
	// unbounded, and the bound is stated below.
	readOnly := isReadScopedToken(r)

	// The token this socket was opened with, or 0 for a session (cookie)
	// principal (#159).
	//
	// REVOCATION IS THE OPERATOR'S ONLY LEVER after a credential leaks, and
	// until now it did not reach an open socket at all: the principal was
	// resolved once, here, and a socket opened a second before the revoke went
	// on streaming status, logs and levels indefinitely. Deleting the row
	// stopped the NEXT request and did nothing about the one already in flight
	// forever.
	//
	// TOKEN PRINCIPALS ONLY. A session socket is deliberately skipped: the
	// console opens two per tab, sessions are already revoked wholesale by
	// TokenEpoch on a password change, and re-evaluating them here would close
	// the operator's dashboard as a side effect of routine session rotation --
	// turning a security fix into a UI fault that nobody would connect to it.
	//
	// The scope is NOT re-evaluated on the tick, only the existence of the
	// token, and that is a deliberate narrowing rather than an oversight.
	// internal/db has no scope-update path -- api_tokens are created, listed,
	// looked up and deleted, and that is all -- so there is no downgrade to
	// catch. If one is added, this is where it goes, and the set below is the
	// structure it layers onto.
	var tokenID int64
	if p, ok := principalFrom(r.Context()); ok && p.token != nil {
		tokenID = p.token.ID
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.Debug("websocket upgrade failed", "err", err)
		return
	}
	defer conn.Close()

	sub := s.bus.Subscribe()
	defer sub.Close()

	// Send the current state immediately, so a freshly opened page is
	// populated without waiting for the next tick.
	initial := []events.Event{
		{Type: events.TypeStatus, Time: time.Now(), Data: s.eng().Status()},
		{Type: events.TypeSource, Time: time.Now(), Data: s.eng().SourceInfo()},
		{Type: events.TypeStats, Time: time.Now(), Data: map[string]any{
			"system":  s.eng().Monitor().System(),
			"bitrate": s.eng().Monitor().Bitrate(),
		}},
	}
	// The initial burst goes through the same policy as the stream. It did not,
	// and that alone would have kept the socket principal-independent for the
	// first three frames of every connection -- which are the frames a page
	// renders from.
	for _, ev := range initial {
		if err := writeEvent(conn, ev, readOnly); err != nil {
			return
		}
	}

	// The read pump exists to service pongs and to notice a closed socket;
	// the client is not expected to send anything meaningful.
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn.SetReadLimit(4096)
		_ = conn.SetReadDeadline(time.Now().Add(pongWait))
		conn.SetPongHandler(func(string) error {
			return conn.SetReadDeadline(time.Now().Add(pongWait))
		})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	ping := time.NewTicker(s.pingEvery())
	defer ping.Stop()

	for {
		select {
		case <-done:
			return
		case <-r.Context().Done():
			return
		case ev, ok := <-sub.C:
			if !ok {
				return
			}
			if err := writeEvent(conn, ev, readOnly); err != nil {
				return
			}
		case <-ping.C:
			// Revocation, on the tick that already exists (#159).
			//
			// BEFORE the ping write, not after: the point is to stop talking to
			// this client, and pinging a socket that is about to be closed for
			// policy reasons is one more frame than it should get.
			//
			// One read-locked map lookup, no database, no allocation, per socket
			// per ping period. The exposure window is therefore ONE PING PERIOD
			// -- 25 seconds -- where it used to be the lifetime of the
			// connection. That is a bound rather than an elimination and it is
			// the honest description of what this buys.
			//
			// The close frame is sent and its error ignored on purpose. A client
			// that has already gone away cannot be told why, and the return
			// below closes the connection either way; treating the write failure
			// as a reason not to return would keep the socket that this branch
			// exists to end.
			if s.isRevoked(tokenID) {
				_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
				_ = conn.WriteMessage(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.ClosePolicyViolation,
						"the API token this socket was opened with has been revoked"))
				return
			}
			_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// writeEvent renders one event for this socket's principal and sends it.
//
// The nil return on a withheld event is deliberate: a frame the policy refuses
// is not an error, and treating it as one would close the socket of any
// read-scoped client the moment an unclassified event was published -- turning
// a fail-closed redaction into a fail-closed connection.
func writeEvent(conn *websocket.Conn, ev events.Event, readOnly bool) error {
	view, ok := eventView(ev, readOnly)
	if !ok {
		return nil
	}
	b, err := json.Marshal(view)
	if err != nil {
		return err
	}
	if err := conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, b)
}
