// Package chat is polyemesis's unified cross-platform chat: one pane, one
// send box, four platforms.
//
// Shape: every platform is an Adapter behind one interface, and the Hub
// supervises them. The interface fell out of the Twitch IRC adapter, which is
// the reference implementation and the only one of the four that is a real
// push stream — the others are made to look like one. Reconnection, backoff,
// deduplication, persistence and event publishing all live in the Hub, so an
// adapter is only ever "read this platform and hand over messages".
//
// Isolation is the load-bearing property. YouTube running out of API quota at
// four in the afternoon must not take Twitch chat down with it, so each
// adapter runs in its own goroutine with its own state, its own backoff and a
// recover() around it: a panicking adapter restarts, and nothing else notices.
// The Hub never holds a lock while calling into an adapter.
//
// On testing: the IRC transport cannot be exercised against Twitch offline,
// and this package does not pretend otherwise — there is no test here that
// proves polyemesis can talk to irc.chat.twitch.tv. What is tested is
// everything that has ever actually been wrong: the line and tag parser, the
// handshake and PING/PONG sequence over an in-memory pipe, the normalisation
// of each platform's payload, the YouTube quota pacer, and the Hub's failure
// isolation with deliberately broken fake adapters. The parts that need a
// socket to a real platform are verified by connecting to one.
package chat

import (
	"context"
	"errors"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/events"
)

// Adapter is one platform's chat connection.
//
// Deliberately three methods. Anything a platform can do that another cannot —
// sending, deleting, reporting quota — is a separate optional interface, so
// that a platform which simply reads chat does not have to implement stubs
// about capabilities it will never have. This is the same split that
// MetadataPusher and ManualKey use in internal/oauth.
type Adapter interface {
	Platform() db.Platform
	// Account is the platform_accounts.account_ref this adapter is connected
	// as. It travels on every message so two accounts on one platform stay
	// distinguishable.
	Account() string
	// Run reads chat until ctx ends, handing each message to sink.
	//
	// Returning nil means the platform ended the conversation cleanly (the
	// broadcast finished); returning an error means the Hub reconnects after a
	// backoff, unless the error is Fatal.
	Run(ctx context.Context, sink Sink) error
}

// Sink receives messages from an adapter.
//
// Deliver must not block for long. It is called from the adapter's read loop,
// and an IRC socket that stops being read is an IRC socket Twitch closes: the
// Hub's implementation therefore does its persistence off the calling
// goroutine and drops rather than waits when a consumer is saturated.
type Sink interface {
	Deliver(Message)
}

// SinkFunc adapts a function to a Sink.
type SinkFunc func(Message)

// Deliver implements Sink.
func (f SinkFunc) Deliver(m Message) { f(m) }

// Sender is the optional capability for a platform polyemesis can talk back
// to. Discover it with the Hub's fan-out Send; never type-assert an Adapter at
// a call site, because receive-only is a legitimate answer that has to be
// handled once.
type Sender interface {
	Adapter
	// Send posts text as the connected account.
	//
	// It returns the message to render locally, or the zero Message when the
	// platform will deliver our own message back through the normal feed and a
	// synthesised copy would show up twice. A returned message carrying the
	// platform's real id is better than either: dedupe then suppresses the
	// platform's echo when it arrives.
	Send(ctx context.Context, text string) (Message, error)
}

// Deleter is the optional capability for removing a message, which is what the
// unified pane needs when a moderator deletes something on one platform and
// polyemesis is displaying it on all of them.
type Deleter interface {
	Adapter
	Delete(ctx context.Context, messageID string) error
}

// Retractor is the optional half of Sink, for a platform that reports its OWN
// deletions back to us.
//
// This exists because the instruction polyemesis gives an operator -- "use the
// platform's dashboard" for anything it cannot do itself -- used to desynchronise
// the pane the moment they followed it. A moderator deleted a message on Twitch,
// Twitch said so, and polyemesis kept showing it to the operator (and to any
// overlay fed from the pane) until retention aged it out up to two hours later.
//
// Optional on the SINK rather than a method on Sink, so SinkFunc and every test
// double stay valid. Adapters reach it through retract below rather than
// asserting inline, so there is one place that knows the sink might not care.
type Retractor interface {
	// Retract reports that the platform removed messageID. Idempotent: a
	// platform may say so more than once, and a retraction for something we
	// never saw is normal rather than an error.
	Retract(messageID string)
}

// retract tells the sink a message is gone, when the sink can hear it.
//
// A sink that is not a Retractor is a legitimate answer -- a test double
// collecting messages does not need to model deletion -- so this is a silent
// no-op rather than a failure.
func retract(sink Sink, messageID string) {
	if messageID == "" {
		return
	}
	if r, ok := sink.(Retractor); ok {
		r.Retract(messageID)
	}
}

// RetractAll is the sink half of a platform clearing its whole chat, or timing
// out a user, which removes every message that user has sent.
//
// Separate from Retract because the platforms address it differently: Twitch's
// CLEARCHAT names a USER (or nobody at all, meaning the entire room), never a
// message. Collapsing that into a list of message ids would mean guessing which
// messages the platform meant, and guessing wrong deletes something a viewer
// can still see.
type RetractorAll interface {
	// RetractUser removes every message from one author. An empty author means
	// the platform cleared the entire chat.
	RetractUser(authorID string)
}

// retractUser tells the sink every message from one author is gone, or -- with
// an empty authorID -- that the whole room was cleared.
func retractUser(sink Sink, authorID string) {
	if r, ok := sink.(RetractorAll); ok {
		r.RetractUser(authorID)
	}
}

// Healther is the optional self-report. An adapter that knows more about its
// own condition than "running or not" — YouTube and its quota budget, Kick and
// its webhook reachability — implements this, and the Hub prefers its answer to
// its own.
type Healther interface {
	Health() Health
}

// State is a chat connection's condition, as the operator would describe it.
type State string

const (
	// StateConnecting is the initial dial, and every reconnect.
	StateConnecting State = "connecting"
	// StateLive is connected and receiving.
	StateLive State = "live"
	// StateDegraded is running but limited, and always accompanied by a Detail
	// saying how: paused on quota, receiving no webhooks, polling slowly. It is
	// distinct from failed because the operator's action is different — this is
	// "something to know", not "something to fix now".
	StateDegraded State = "degraded"
	// StateFailed is not connected, with a reason.
	StateFailed State = "failed"
	// StateStopped is a deliberate stop: the broadcast ended, or the operator
	// detached the account.
	StateStopped State = "stopped"
)

// Health is an adapter's own view of itself.
type Health struct {
	State State `json:"state"`
	// Detail is one sentence for a human. It must never contain a token, a
	// stream key or a client secret; adapters build it from their own words,
	// not from a request they were about to send.
	Detail string `json:"detail,omitempty"`
	// Quota is the platform's API budget where one exists and matters. Only
	// YouTube populates it, and only because exhausting it silently kills chat
	// for the rest of the day.
	Quota *QuotaStatus `json:"quota,omitempty"`
}

// Status is one platform's chat connection as reported to the UI.
type Status struct {
	Platform db.Platform `json:"platform"`
	Account  string      `json:"account,omitempty"`
	Channel  string      `json:"channel,omitempty"`
	State    State       `json:"state"`
	Detail   string      `json:"detail,omitempty"`
	// Since is when this state began, so the UI can say "reconnecting for 4
	// minutes" rather than "reconnecting" forever.
	Since    time.Time `json:"since"`
	Received int64     `json:"received"`
	Sent     int64     `json:"sent"`
	// Restarts counts reconnections. A number that keeps climbing while the
	// state reads live is the signature of a flapping connection, which
	// otherwise looks fine at every instant you check it.
	Restarts  int64        `json:"restarts"`
	LastError string       `json:"lastError,omitempty"`
	CanSend   bool         `json:"canSend"`
	Quota     *QuotaStatus `json:"quota,omitempty"`
}

// SendResult is one platform's outcome from a fan-out send. Partial failure is
// the normal case — one platform's token expired, another's rate limit is
// bitten, the rest went out — so this reports per platform rather than
// returning a single error that would throw away the sends that worked.
type SendResult struct {
	Platform db.Platform `json:"platform"`
	Account  string      `json:"account,omitempty"`
	OK       bool        `json:"ok"`
	// Skipped separates "this platform cannot send" from "this send failed".
	// The first is a permanent property the operator should see once; the
	// second is worth retrying.
	Skipped bool   `json:"skipped,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// ErrFatal marks a failure that reconnecting cannot fix — a rejected token, a
// scope that was never granted. The Hub stops retrying and leaves the reason on
// the status, because a wrong password retried every thirty seconds for six
// hours is how a rate limit turns into an IP ban.
var ErrFatal = errors.New("chat connection cannot be retried")

// Fatal wraps an error as unretryable.
func Fatal(err error) error {
	if err == nil {
		return nil
	}
	return fatal{err}
}

type fatal struct{ err error }

func (f fatal) Error() string { return f.err.Error() }
func (f fatal) Unwrap() error { return f.err }
func (f fatal) Is(target error) bool {
	return target == ErrFatal
}

// IsFatal reports whether retrying is pointless.
func IsFatal(err error) bool { return errors.Is(err, ErrFatal) }

// Publisher is the event bus, narrowed to what this package uses so a test does
// not need one. *events.Broker satisfies it.
type Publisher interface {
	Publish(t events.Type, data any)
}

// Store persists the bounded history. *db.DB satisfies it.
type Store interface {
	AppendChatMessages(msgs []db.ChatMessage) (int, error)
}

// Retraction is what the UI is told when messages disappear, whether because a
// moderator used polyemesis or because they used the platform's own dashboard.
//
// It carries a LIST rather than one id because a timeout removes everything one
// author said, and sending N events for one moderator action would let the pane
// render a half-applied timeout.
type Retraction struct {
	Platform db.Platform `json:"platform"`
	Account  string      `json:"account,omitempty"`
	// MessageIDs is what polyemesis was actually holding and has now dropped.
	// It is not "every message the platform removed" -- anything already out of
	// the history ring cannot be named, and claiming otherwise would be a lie
	// the UI then has to render.
	MessageIDs []string `json:"messageIds"`
	// AuthorID is set when the platform named a user rather than a message, so
	// a client keeping its own buffer can apply the same rule to messages this
	// server no longer holds.
	AuthorID string `json:"authorId,omitempty"`
	// All marks the platform clearing the entire room.
	All bool `json:"all,omitempty"`
}

// Remover is the optional half of Store for deleting one stored message. A
// Store without it simply keeps the row until retention takes it, which is
// survivable: the live pane has already dropped the message. *db.DB satisfies it.
type Remover interface {
	DeleteChatMessage(p db.Platform, account, messageID string) error
}

// Purger is the optional half of Store that bounds the table. A Store without
// it simply grows, which is the right default for a caller that has its own
// retention policy. *db.DB satisfies it.
type Purger interface {
	PurgeChatMessages(cutoff time.Time, keep int) (int, error)
}

// ToDB converts a message for storage. Exported because the API layer replays
// history out of the database and needs the round trip to be one obvious pair
// of functions rather than two divergent ones.
func ToDB(m Message) db.ChatMessage {
	return db.ChatMessage{
		Platform:    m.Platform,
		Account:     m.Account,
		MessageID:   m.ID,
		Channel:     m.Channel,
		AuthorID:    m.Author.ID,
		AuthorName:  m.Author.Name,
		AuthorColor: m.Author.Color,
		Moderator:   m.Author.Moderator,
		Subscriber:  m.Author.Subscriber,
		Broadcaster: m.Author.Broadcaster,
		Text:        m.Text,
		Badges:      mustJSON(m.Author.Badges),
		Emotes:      mustJSON(m.Emotes),
		ReplyToID:   m.ReplyToID,
		ReplyTo:     m.ReplyTo,
		Echo:        m.Echo,
		At:          m.At,
	}
}

// FromDB converts a stored message back. A row whose JSON columns will not
// decode yields the message without its badges or emotes rather than an error:
// the text is what the reader came for.
func FromDB(r db.ChatMessage) Message {
	m := Message{
		ID:       r.MessageID,
		Platform: r.Platform,
		Account:  r.Account,
		Channel:  r.Channel,
		Author: Author{
			ID:          r.AuthorID,
			Name:        r.AuthorName,
			Color:       r.AuthorColor,
			Moderator:   r.Moderator,
			Subscriber:  r.Subscriber,
			Broadcaster: r.Broadcaster,
		},
		Text:      r.Text,
		ReplyToID: r.ReplyToID,
		ReplyTo:   r.ReplyTo,
		Echo:      r.Echo,
		At:        r.At,
	}
	_ = decodeJSON(r.Badges, &m.Author.Badges)
	_ = decodeJSON(r.Emotes, &m.Emotes)
	return m
}
