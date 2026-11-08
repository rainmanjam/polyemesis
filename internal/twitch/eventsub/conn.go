package eventsub

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Frame is one thing read off the socket: a decoded Message, a frame that was
// not an EventSub message (Bad), or the end of the connection (Err, which is
// a *websocket.CloseError when Twitch closed it).
type Frame struct {
	Msg Message
	Bad bool
	Err error
}

// Conn is an EventSub WebSocket. A reader goroutine feeds Frames; nothing is
// ever written to the server (Twitch closes the socket with 4001 on any
// inbound traffic but a pong, and gorilla answers pings itself).
type Conn struct {
	ws     *websocket.Conn
	frames chan Frame
	done   chan struct{}
	once   sync.Once
}

// MaxFrameBytes bounds one frame. Real notifications are a few kilobytes.
const MaxFrameBytes = 512 << 10

// Dial opens the socket and starts reading. dialer may be nil.
func Dial(ctx context.Context, rawurl string, dialer *websocket.Dialer) (*Conn, error) {
	if dialer == nil {
		dialer = &websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	}
	ws, resp, err := dialer.DialContext(ctx, rawurl, http.Header{})
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err != nil {
		// The URL of a reconnect carries a session token; say only that it failed.
		return nil, ErrDial
	}
	ws.SetReadLimit(MaxFrameBytes)
	c := &Conn{ws: ws, frames: make(chan Frame, 16), done: make(chan struct{})}
	go c.read()
	return c, nil
}

// ErrDial is a connection that could not be opened.
var ErrDial = errString("could not open the Twitch EventSub connection")

type errString string

func (e errString) Error() string { return string(e) }

func (c *Conn) read() {
	defer close(c.frames)
	for {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			c.send(Frame{Err: err})
			return
		}
		m, perr := ParseMessage(data)
		if perr != nil {
			c.send(Frame{Bad: true})
			continue
		}
		if !c.send(Frame{Msg: m}) {
			return
		}
	}
}

func (c *Conn) send(f Frame) bool {
	select {
	case c.frames <- f:
		return true
	case <-c.done:
		return false
	}
}

// Frames is the stream of frames; it is closed when the connection ends.
func (c *Conn) Frames() <-chan Frame { return c.frames }

// Close drops the connection. Closing the session deletes its subscriptions.
func (c *Conn) Close() {
	c.once.Do(func() {
		close(c.done)
		_ = c.ws.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
		_ = c.ws.Close()
	})
}
