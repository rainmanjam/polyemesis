package eventsub

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Endpoints. Both are overridable by the caller; these are Twitch's.
const (
	DefaultWSURL     = "wss://eventsub.wss.twitch.tv/ws"
	DefaultHelixBase = "https://api.twitch.tv/helix"
)

// KeepaliveSeconds is what is asked of Twitch with the keepalive_timeout_seconds
// query parameter (valid 10 to 600). Thirty keeps a dead socket from lingering
// without making a healthy one chatty.
const KeepaliveSeconds = 30

// MessageType is metadata.message_type.
type MessageType string

// The six message types of the WebSocket reference.
const (
	TypeWelcome      MessageType = "session_welcome"
	TypeKeepalive    MessageType = "session_keepalive"
	TypeNotification MessageType = "notification"
	TypeReconnect    MessageType = "session_reconnect"
	TypeRevocation   MessageType = "revocation"
)

// Close codes Twitch sends (WebSocket reference, "Close message").
const (
	CloseInternal       = 4000
	CloseInbound        = 4001 // the client sent a frame other than a pong
	ClosePingPong       = 4002
	CloseUnused         = 4003 // no subscription within the welcome window
	CloseReconnectGrace = 4004 // the 30 s after session_reconnect ran out
	CloseNetworkTimeout = 4005
	CloseNetworkError   = 4006
	CloseBadReconnect   = 4007
)

// Metadata identifies a message. MessageID is what a delivery is deduplicated
// on: Twitch sends at least once and resends under the SAME id.
type Metadata struct {
	MessageID           string `json:"message_id"`
	MessageType         string `json:"message_type"`
	MessageTimestamp    string `json:"message_timestamp"`
	SubscriptionType    string `json:"subscription_type"`
	SubscriptionVersion string `json:"subscription_version"`
}

// Time is the message timestamp, or the zero time when it does not parse.
func (m Metadata) Time() time.Time { return ParseTime(m.MessageTimestamp) }

// Message is one decoded text frame.
type Message struct {
	Metadata Metadata        `json:"metadata"`
	Payload  json.RawMessage `json:"payload"`
}

// Type is the message type as a MessageType.
func (m Message) Type() MessageType { return MessageType(m.Metadata.MessageType) }

// ErrMalformed is a frame that is not an EventSub message.
var ErrMalformed = errors.New("eventsub: not an EventSub message")

// ParseMessage decodes one frame. It never echoes the frame in its error: a
// payload carries viewer text.
func ParseMessage(b []byte) (Message, error) {
	var m Message
	if err := json.Unmarshal(b, &m); err != nil {
		return Message{}, fmt.Errorf("%w (%d bytes)", ErrMalformed, len(b))
	}
	if m.Metadata.MessageType == "" || m.Metadata.MessageID == "" {
		return Message{}, fmt.Errorf("%w (%d bytes, no metadata)", ErrMalformed, len(b))
	}
	return m, nil
}

// SessionInfo is payload.session of a welcome or reconnect message.
type SessionInfo struct {
	ID                      string  `json:"id"`
	Status                  string  `json:"status"`
	KeepaliveTimeoutSeconds *int    `json:"keepalive_timeout_seconds"`
	ReconnectURL            *string `json:"reconnect_url"`
}

// Session decodes payload.session.
func (m Message) Session() (SessionInfo, error) {
	var p struct {
		Session SessionInfo `json:"session"`
	}
	if err := json.Unmarshal(m.Payload, &p); err != nil || p.Session.ID == "" {
		return SessionInfo{}, fmt.Errorf("%w (session)", ErrMalformed)
	}
	return p.Session, nil
}

// SubscriptionInfo is payload.subscription of a notification or revocation.
type SubscriptionInfo struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Type    string `json:"type"`
	Version string `json:"version"`
	Cost    int    `json:"cost"`
}

// Revocation decodes a revocation's subscription.
func (m Message) Revocation() (SubscriptionInfo, error) {
	var p struct {
		Subscription SubscriptionInfo `json:"subscription"`
	}
	if err := json.Unmarshal(m.Payload, &p); err != nil {
		return SubscriptionInfo{}, fmt.Errorf("%w (revocation)", ErrMalformed)
	}
	if p.Subscription.Type == "" {
		p.Subscription.Type = m.Metadata.SubscriptionType
	}
	return p.Subscription, nil
}

// Event returns payload.event verbatim.
func (m Message) Event() (json.RawMessage, error) {
	var p struct {
		Event json.RawMessage `json:"event"`
	}
	if err := json.Unmarshal(m.Payload, &p); err != nil || len(p.Event) == 0 || string(p.Event) == "null" {
		return nil, fmt.Errorf("%w (event)", ErrMalformed)
	}
	return p.Event, nil
}

// Revocation statuses (revocation message reference).
const (
	RevokedAuthorization = "authorization_revoked"
	RevokedUserRemoved   = "user_removed"
	RevokedVersionGone   = "version_removed"
)

// ParseTime reads Twitch's RFC 3339 timestamps (nanosecond precision), or
// returns the zero time.
func ParseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// ErrReconnectHost is a reconnect_url Twitch should never have sent.
var ErrReconnectHost = errors.New("eventsub: the reconnect address is not a Twitch host")

// ValidReconnectURL accepts a wss URL whose host is twitch.tv or a subdomain.
// The URL is used as given: Twitch says not to modify it, and following a
// redirect to anywhere else would hand the connection to a stranger.
func ValidReconnectURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "wss" {
		return ErrReconnectHost
	}
	h := strings.ToLower(u.Hostname())
	if h == "twitch.tv" || strings.HasSuffix(h, ".twitch.tv") {
		return nil
	}
	return ErrReconnectHost
}
