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
		{Type: events.TypeStatus, Time: time.Now(), Data: s.eng.Status()},
		{Type: events.TypeSource, Time: time.Now(), Data: s.eng.SourceInfo()},
		{Type: events.TypeStats, Time: time.Now(), Data: map[string]any{
			"system":  s.eng.Monitor().System(),
			"bitrate": s.eng.Monitor().Bitrate(),
		}},
	}
	for _, ev := range initial {
		if err := writeEvent(conn, ev); err != nil {
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

	ping := time.NewTicker(pingPeriod)
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
			if err := writeEvent(conn, ev); err != nil {
				return
			}
		case <-ping.C:
			_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func writeEvent(conn *websocket.Conn, ev events.Event) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if err := conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, b)
}
