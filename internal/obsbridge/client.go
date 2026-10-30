package obsbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

// The obs-websocket client: dial, Hello, Identify, Identified, then read
// events until the link drops, and dial again with backoff for as long as the
// configuration stands.
//
// EVERY STATE SENTENCE BELOW IS A CONSTANT. A dial error carries the address
// and the OS's words for what happened to it; an error from the handshake can
// carry whatever the other end wrote. None of that reaches the status page or
// an obs.connection event. It reaches the log at Debug, address included,
// because the operator who set the address is the one reading the log -- and
// the password reaches neither: it is used once, inside AuthString, and
// nothing in this file formats it.

const (
	detailDialing      = "connecting to obs-websocket"
	detailConnected    = "connected to obs-websocket"
	detailUnreachable  = "obs-websocket did not answer at the configured address; is OBS running, and is the WebSocket server enabled in Tools, WebSocket Server Settings?"
	detailDropped      = "the connection to obs-websocket dropped; reconnecting"
	detailNeedPassword = "obs-websocket asks for a password and none is set; set it in Settings, OBS bridge"
	detailBadPassword  = "obs-websocket refused the password; check it against OBS's Tools, WebSocket Server Settings"
	detailNotOBS       = "whatever answered at the configured address does not speak obs-websocket 5"
)

// Timeouts. handshakeTimeout bounds the dial and each handshake read;
// pingEvery and readTimeout detect a peer that vanished without closing, such
// as a laptop lid shut on OBS: the read would otherwise wait forever.
const (
	handshakeTimeout = 10 * time.Second
	pingEvery        = 15 * time.Second
	readTimeout      = 45 * time.Second
	// maxFrame bounds one message from OBS. The largest thing the bridge asks
	// for is a scene name; a megabyte is generous for anything OBS sends
	// unprompted and small enough that a hostile peer cannot balloon memory.
	maxFrame = 1 << 20
)

// errRefused and errNotOBS end a session in a way that waits longer before
// the next attempt; anything else is "unreachable".
var (
	errRefused = errors.New("refused")
	errNotOBS  = errors.New("not obs-websocket")
)

func (b *Bridge) runClient(ctx context.Context, url, password string) {
	wait := b.retryMin
	for {
		b.setConn(ConnConnecting, detailDialing, nil)
		connected, err := b.session(ctx, url, password)
		if ctx.Err() != nil {
			return
		}
		switch {
		case connected:
			// A link that was up starts the backoff again from the bottom:
			// the next drop is a new incident, not the tenth of an old one.
			wait = b.retryMin
			b.setConn(ConnUnreachable, detailDropped, nil)
		case errors.Is(err, errRefused) || errors.Is(err, errNotOBS):
			// Waiting will not fix a wrong password. Retrying at the ceiling
			// keeps the state honest if OBS's setting is changed back, without
			// hammering a server that is refusing us.
			wait = b.retryMax
		default:
			b.setConn(ConnUnreachable, detailUnreachable, nil)
		}
		b.log.Debug("OBS bridge: obs-websocket session ended", "url", url, "err", err, "retryIn", wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if !connected {
			wait = min(wait*2, b.retryMax)
		}
	}
}

// session runs one connection from dial to drop. connected reports whether it
// got as far as Identified.
func (b *Bridge) session(ctx context.Context, url, password string) (connected bool, err error) {
	d := websocket.Dialer{
		HandshakeTimeout: handshakeTimeout,
		Subprotocols:     []string{Subprotocol},
		Proxy:            http.ProxyFromEnvironment,
	}
	dctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	conn, resp, err := d.DialContext(dctx, url, nil)
	cancel()
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.Close() }()
	conn.SetReadLimit(maxFrame)

	// The connection is closed when ctx ends, which is what unblocks a read.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	hello, err := handshake(conn, password)
	if err != nil {
		switch {
		case errors.Is(err, errNeedPassword):
			b.setConn(ConnRefused, detailNeedPassword, nil)
			return false, errRefused
		case errors.Is(err, errRefused):
			b.setConn(ConnRefused, detailBadPassword, nil)
			return false, err
		case errors.Is(err, errNotOBS):
			b.setConn(ConnIncompatible, detailNotOBS, nil)
			return false, err
		}
		return false, err
	}
	b.setConn(ConnConnected, detailConnected, hello)

	// Ask where OBS is now. The answers arrive as Sync events: they tell the
	// bridge the state it reconnected into and act on nothing.
	for _, rt := range []string{ReqGetStreamStatus, ReqGetCurrentProgramScene} {
		msg, ferr := frame(OpRequest, Request{RequestType: rt, RequestID: rt})
		if ferr == nil {
			ferr = conn.WriteMessage(websocket.TextMessage, msg)
		}
		if ferr != nil {
			return true, ferr
		}
	}

	_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(readTimeout)) })
	pingDone := make(chan struct{})
	defer close(pingDone)
	go func() {
		t := time.NewTicker(pingEvery)
		defer t.Stop()
		for {
			select {
			case <-pingDone:
				return
			case <-t.C:
				if conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(handshakeTimeout)) != nil {
					return
				}
			}
		}
	}()

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return true, err
		}
		_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
		if ev, ok := decodeFrame(raw); ok {
			// Handle refuses only a disabled bridge or a malformed fact; the
			// first cannot happen while this client runs and the second is
			// OBS sending something odd, which is not worth dropping the link.
			if herr := b.Handle(ev); herr != nil {
				b.log.Debug("OBS bridge: ignored an obs-websocket message", "err", herr)
			}
		}
	}
}

var errNeedPassword = fmt.Errorf("%w: password required", errRefused)

// handshake runs Hello -> Identify -> Identified.
func handshake(conn *websocket.Conn, password string) (*Hello, error) {
	_ = conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
	var m Message
	if err := conn.ReadJSON(&m); err != nil {
		return nil, fmt.Errorf("%w: reading Hello: %v", errNotOBS, err) //nolint:errorlint // one wrapped sentinel is the contract
	}
	if m.Op != OpHello {
		return nil, fmt.Errorf("%w: first message was op %d", errNotOBS, m.Op)
	}
	var h Hello
	if err := json.Unmarshal(m.D, &h); err != nil || h.RPCVersion < RPCVersion {
		return nil, fmt.Errorf("%w: unusable Hello", errNotOBS)
	}
	id := Identify{RPCVersion: RPCVersion, EventSubscriptions: Subscriptions}
	if h.Authentication != nil {
		if password == "" {
			return nil, errNeedPassword
		}
		id.Authentication = AuthString(password, h.Authentication.Salt, h.Authentication.Challenge)
	}
	msg, err := frame(OpIdentify, id)
	if err != nil {
		return nil, err
	}
	if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
		return nil, err
	}
	m = Message{}
	if err := conn.ReadJSON(&m); err != nil {
		var ce *websocket.CloseError
		if errors.As(err, &ce) && ce.Code == CloseAuthenticationFailed {
			return nil, errRefused
		}
		return nil, err
	}
	if m.Op != OpIdentified {
		return nil, fmt.Errorf("%w: expected Identified, got op %d", errNotOBS, m.Op)
	}
	_ = conn.SetReadDeadline(time.Time{})
	return &h, nil
}

// decodeFrame turns one message from OBS into a fact, if it is one of the few
// the bridge reads. Everything else -- including any event OBS sends that was
// not subscribed to -- is ignored.
func decodeFrame(raw []byte) (Event, bool) {
	var m Message
	if json.Unmarshal(raw, &m) != nil {
		return Event{}, false
	}
	switch m.Op {
	case OpEvent:
		var e EventFrame
		if json.Unmarshal(m.D, &e) != nil {
			return Event{}, false
		}
		switch e.EventType {
		case EventStreamStateChanged:
			var s StreamState
			if json.Unmarshal(e.EventData, &s) != nil {
				return Event{}, false
			}
			switch s.OutputState {
			case OutputStarted:
				return Event{Kind: KindStreamStarted, Via: ViaWebSocket}, true
			case OutputStopped:
				return Event{Kind: KindStreamStopped, Via: ViaWebSocket}, true
			}
		case EventCurrentProgramSceneChanged:
			var p ProgramScene
			if json.Unmarshal(e.EventData, &p) != nil || p.Name() == "" {
				return Event{}, false
			}
			return Event{Kind: KindSceneChanged, Scene: p.Name(), Via: ViaWebSocket}, true
		}
	case OpRequestResponse:
		var r RequestResponse
		if json.Unmarshal(m.D, &r) != nil || !r.RequestStatus.Result {
			return Event{}, false
		}
		switch r.RequestType {
		case ReqGetStreamStatus:
			var s StreamStatus
			if json.Unmarshal(r.ResponseData, &s) != nil {
				return Event{}, false
			}
			k := KindStreamStopped
			if s.OutputActive {
				k = KindStreamStarted
			}
			return Event{Kind: k, Via: ViaWebSocket, Sync: true}, true
		case ReqGetCurrentProgramScene:
			var p ProgramScene
			if json.Unmarshal(r.ResponseData, &p) != nil || p.Name() == "" {
				return Event{}, false
			}
			return Event{Kind: KindSceneChanged, Scene: p.Name(), Via: ViaWebSocket, Sync: true}, true
		}
	}
	return Event{}, false
}
