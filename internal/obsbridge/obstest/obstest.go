// Package obstest is a mock obs-websocket v5 server for tests.
//
// It is NOT OBS, and nothing proven against it is proven against OBS. What it
// does is speak the protocol as obs-websocket's own documentation and its
// Crypto.cpp define it: Hello with a challenge and salt, an Identify checked
// the way the plugin checks it (base64(sha256(base64(sha256(password+salt)) +
// challenge))), close code 4009 on a wrong password, Identified, the two
// requests the bridge sends, and events pushed on demand. A client that gets
// the hashing wrong is refused here exactly as OBS would refuse it, because
// the check below is written from the plugin's source rather than from the
// client's.
package obstest

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// Server is a running mock.
type Server struct {
	t        testing.TB
	srv      *httptest.Server
	password string

	mu sync.Mutex
	// conns are the identified connections.
	conns []*websocket.Conn
	// Streaming and Scene answer GetStreamStatus and GetCurrentProgramScene.
	streaming bool
	scene     string
	// identifies counts successful Identify messages; refusals counts 4009s.
	identifies int
	refusals   int
	// subscriptions is the eventSubscriptions the last client asked for.
	subscriptions int
	// requests are the requestTypes received, in order.
	requests []string
	// connected is signalled after every successful Identify.
	connected chan struct{}
}

// New starts a mock that requires password ("" for none).
func New(t testing.TB, password string) *Server {
	t.Helper()
	s := &Server{t: t, password: password, scene: "Starting Soon", connected: make(chan struct{}, 16)}
	up := websocket.Upgrader{Subprotocols: []string{"obswebsocket.json"}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		s.serve(c)
	}))
	t.Cleanup(s.Close)
	return s
}

// URL is the ws:// address to configure.
func (s *Server) URL() string { return "ws" + strings.TrimPrefix(s.srv.URL, "http") }

// Close stops the server and every connection.
func (s *Server) Close() {
	s.DropAll()
	s.srv.Close()
}

// Hello's challenge and salt are fixed per mock: the client cannot know them
// in advance any more than it can know OBS's, and fixing them keeps a failure
// reproducible.
const (
	challenge = "+IxH4CnCiqpX1rM9scsNynZzbOe4KhDeYcTNS3PDaeY="
	salt      = "lM1GncleQOaCu9lT1yeUZhFYnqhsLLP1G5lAGo3ixaI="
)

// expectedAuth is Utils::Crypto::GenerateSecret then CheckAuthenticationString,
// transcribed.
func expectedAuth(password string) string {
	sec := sha256.Sum256([]byte(password + salt))
	secret := base64.StdEncoding.EncodeToString(sec[:])
	h := sha256.Sum256([]byte(secret + challenge))
	return base64.StdEncoding.EncodeToString(h[:])
}

type msg struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
}

func send(c *websocket.Conn, op int, d any) error {
	raw, _ := json.Marshal(d)
	return c.WriteJSON(msg{Op: op, D: raw})
}

func (s *Server) serve(c *websocket.Conn) {
	hello := map[string]any{"obsStudioVersion": "30.2.2", "obsWebSocketVersion": "5.5.2", "rpcVersion": 1}
	if s.password != "" {
		hello["authentication"] = map[string]string{"challenge": challenge, "salt": salt}
	}
	if send(c, 0, hello) != nil {
		_ = c.Close()
		return
	}
	var m msg
	if c.ReadJSON(&m) != nil || m.Op != 1 {
		_ = c.Close()
		return
	}
	var id struct {
		RPCVersion         int    `json:"rpcVersion"`
		Authentication     string `json:"authentication"`
		EventSubscriptions int    `json:"eventSubscriptions"`
	}
	_ = json.Unmarshal(m.D, &id)
	if s.password != "" && id.Authentication != expectedAuth(s.password) {
		s.mu.Lock()
		s.refusals++
		s.mu.Unlock()
		_ = c.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(4009, "Authentication failed."), time.Now().Add(time.Second))
		_ = c.Close()
		return
	}
	if send(c, 2, map[string]int{"negotiatedRpcVersion": 1}) != nil {
		_ = c.Close()
		return
	}
	s.mu.Lock()
	s.identifies++
	s.subscriptions = id.EventSubscriptions
	s.conns = append(s.conns, c)
	s.mu.Unlock()
	s.connected <- struct{}{}

	for {
		if c.ReadJSON(&m) != nil {
			return
		}
		if m.Op != 6 {
			continue
		}
		var req struct {
			RequestType string `json:"requestType"`
			RequestID   string `json:"requestId"`
		}
		_ = json.Unmarshal(m.D, &req)
		s.mu.Lock()
		s.requests = append(s.requests, req.RequestType)
		streaming, scene := s.streaming, s.scene
		s.mu.Unlock()
		resp := map[string]any{"requestType": req.RequestType, "requestId": req.RequestID,
			"requestStatus": map[string]any{"result": true, "code": 100}}
		switch req.RequestType {
		case "GetStreamStatus":
			resp["responseData"] = map[string]any{"outputActive": streaming, "outputReconnecting": false}
		case "GetCurrentProgramScene":
			resp["responseData"] = map[string]any{"sceneName": scene, "sceneUuid": "u", "currentProgramSceneName": scene}
		default:
			resp["requestStatus"] = map[string]any{"result": false, "code": 204}
		}
		s.mu.Lock()
		_ = send(c, 7, resp)
		s.mu.Unlock()
	}
}

// WaitConnected blocks until a client has identified, or fails the test.
func (s *Server) WaitConnected(d time.Duration) {
	s.t.Helper()
	select {
	case <-s.connected:
	case <-time.After(d):
		s.t.Fatalf("no client identified with the mock obs-websocket within %s", d)
	}
}

// SetState sets what the two sync requests answer.
func (s *Server) SetState(streaming bool, scene string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streaming, s.scene = streaming, scene
}

// event pushes one event to every identified client.
func (s *Server) event(eventType string, data any) {
	raw, _ := json.Marshal(data)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		_ = send(c, 5, map[string]any{"eventType": eventType, "eventIntent": 1 << 6, "eventData": json.RawMessage(raw)})
	}
}

// StreamState pushes StreamStateChanged with one ObsOutputState.
func (s *Server) StreamState(outputState string) {
	active := outputState == "OBS_WEBSOCKET_OUTPUT_STARTED" || outputState == "OBS_WEBSOCKET_OUTPUT_RECONNECTING" ||
		outputState == "OBS_WEBSOCKET_OUTPUT_RECONNECTED" || outputState == "OBS_WEBSOCKET_OUTPUT_STOPPING"
	s.mu.Lock()
	switch outputState {
	case "OBS_WEBSOCKET_OUTPUT_STARTED":
		s.streaming = true
	case "OBS_WEBSOCKET_OUTPUT_STOPPED":
		s.streaming = false
	}
	s.mu.Unlock()
	s.event("StreamStateChanged", map[string]any{"outputActive": active, "outputState": outputState})
}

// Scene pushes CurrentProgramSceneChanged.
func (s *Server) Scene(name string) {
	s.mu.Lock()
	s.scene = name
	s.mu.Unlock()
	s.event("CurrentProgramSceneChanged", map[string]any{"sceneName": name, "sceneUuid": "uuid-" + name})
}

// DropAll closes every client connection without a close frame, the way a
// crashed OBS or a pulled cable ends one.
func (s *Server) DropAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		_ = c.UnderlyingConn().Close()
	}
	s.conns = nil
}

// Counts reports successful identifies and 4009 refusals.
func (s *Server) Counts() (identifies, refusals int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.identifies, s.refusals
}

// Subscriptions is the eventSubscriptions bitmask the last client asked for.
func (s *Server) Subscriptions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.subscriptions
}

// Requests are the requestTypes received so far.
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}
