// Package eventstream is the real-time events API: the catalogue of event types
// GET /api/v1/events streams, the envelope each one travels in, and the hub
// that numbers, redacts, remembers and fans them out.
//
// IT IS A TAP, NOT A NEW SOURCE. Every event here is derived by something that
// already existed for another consumer -- the webhook watcher's UP/DOWN edges,
// the alert notifier, the chat hub's bus messages, the lifecycle coordinator --
// and is published here at the same point it is published there. A second
// derivation would be a second opinion about whether a destination went down,
// and two opinions are how a script and a dashboard come to disagree.
//
// THE ENVELOPE IS THE WEBHOOK ENVELOPE. Same field names, same meanings, same
// redaction (hooks.Event.redacted, applied here by Hub.Publish): a consumer that
// already reads `.destination.name` and `.cause` from a webhook body reads them
// from an event the same way. What a webhook has and this does not is what
// belongs to one HTTP delivery -- sequence, missed, test -- and what this has
// and a webhook does not is `type` (a webhook calls it `trigger`) and `data`,
// for the families no webhook carries.
package eventstream

import (
	"sort"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/failreason"
	"github.com/rainmanjam/polyemesis/internal/hooks"
)

// SpecVersion is the envelope contract version, for the same reason
// hooks.SpecVersion exists: a consumer is written against it.
const SpecVersion = "1"

// Type names one kind of event. These strings are what a consumer switches on,
// so renaming one breaks every script that listens for it. Do not rename them.
type Type string

// Topic groups types for the ?topics= filter.
type Topic string

// Topics. A consumer filters by these rather than by type, because "everything
// about destinations" is the question and the list of destination types grows.
const (
	TopicDestination Topic = "destination"
	TopicIngest      Topic = "ingest"
	TopicFailover    Topic = "failover"
	TopicBroadcast   Topic = "broadcast"
	TopicRecording   Topic = "recording"
	TopicChat        Topic = "chat"
	TopicAlert       Topic = "alert"
	TopicStream      Topic = "stream"
	// TopicOverlay is the display projection of chat that an OBS overlay key
	// may read: see DisplayLine and OverlayVisible.
	TopicOverlay Topic = "overlay"
)

// Event types. The destination and ingest names are the webhook trigger names
// on purpose: one fact, one name, on every surface.
const (
	TypeDestinationUp         Type = Type(hooks.TriggerDestinationUp)
	TypeDestinationDown       Type = Type(hooks.TriggerDestinationDown)
	TypeDestinationHeld       Type = Type(hooks.TriggerDestinationHeld)
	TypeDestinationRestarted  Type = "destination.restarted"
	TypeDestinationLevel      Type = "destination.level"
	TypeDestinationRolledOver Type = Type(hooks.TriggerDestinationRolledOver)
	TypeDestinationDelay      Type = "destination.delay"
	TypeIngestPublished       Type = Type(hooks.TriggerIngestPublished)
	TypeIngestDisconnected    Type = Type(hooks.TriggerIngestDisconnected)
	TypeFailoverSwitched      Type = "failover.switched"
	TypeBroadcastLive         Type = "broadcast.live"
	TypeBroadcastComplete     Type = "broadcast.complete"
	TypeBroadcastFault        Type = Type(hooks.TriggerBroadcastFault)
	TypeRecordingStarted      Type = "recording.started"
	TypeRecordingStopped      Type = "recording.stopped"
	TypeChatMessage           Type = "chat.message"
	TypeChatRetract           Type = "chat.retract"
	TypeChatModeration        Type = "chat.moderation"
	TypeChatDisplay           Type = "chat.display"
	TypeChatUndisplay         Type = "chat.undisplay"
	TypeAlert                 Type = "alert"
	TypeStreamGap             Type = "stream.gap"
)

// Access says whether a read-scoped API token receives a type. The zero value
// is Withheld, so a Spec written without thinking about it withholds rather
// than discloses.
type Access int

const (
	// Withheld is never sent to a read-scoped token.
	Withheld Access = iota
	// Granted is sent to every principal.
	Granted
	// Partly is decided per event by the Spec's ReadIf, which must exist; a
	// Partly row without one sends nothing to a read token.
	Partly
)

// Spec is one row of the catalogue.
type Spec struct {
	Type  Type
	Topic Topic
	// ReadScope is what a read-scoped token gets. The reason is written beside
	// each row so a reviewer can check the claim rather than the shape.
	ReadScope Access
	// Summary is the one line docs/EVENTS.md must carry for this type.
	Summary string
	// ReadIf decides a Partly row per event, on the REDACTED envelope.
	ReadIf func(Envelope) bool
}

// alertOnTheTimeline is the alert row's ReadIf: a read token is sent exactly
// the alerts it can already read on GET /timeline -- the stream's operational
// transitions -- and none of the audit trail (sign-ins, token and settings
// changes, rule edits, debug exports, upgrades), which is not on the timeline.
// Anything that is not an alerts.Event is withheld: fail closed.
func alertOnTheTimeline(env Envelope) bool {
	a, ok := env.Data.(alerts.Event)
	return ok && alerts.IsTimelineType(a.Type)
}

// catalogue is THE ONE PLACE an event type is declared to exist.
//
// Everything else is derived from it or checked against it: the ?topics=
// filter, the read-scope policy, docs/EVENTS.md's table (TestTheDocsListExactly
// TheCatalogue) and the requirement that each type is shown, by a test that
// drives its real producer, to actually be emitted (TestEveryCatalogued
// TypeHasAnEmissionTest). A type for a feature that does not exist yet --
// polls (#14), viewer samples (#4), the failover degraded reason (#6) -- is
// added HERE, by that feature, together with the test that proves its producer
// emits it. Until then it is documented under "Coming with …" and nowhere else.
var catalogue = []Spec{
	// Destinations. Status is readable by a read token over REST and /ws
	// already; none of these carries a URL or a stream key, and every free-text
	// field is redacted by Publish.
	{TypeDestinationUp, TopicDestination, Granted, "A destination started delivering.", nil},
	{TypeDestinationDown, TopicDestination, Granted, "A destination stopped: failed, stalled, disabled or removed; reason says which, cause and failure say why.", nil},
	{TypeDestinationHeld, TopicDestination, Granted, "Polyemesis stopped retrying a destination after repeated quick rejections; nothing happens until Retry.", nil},
	{TypeDestinationRestarted, TopicDestination, Granted, "A destination's process was respawned after it exited.", nil},
	{TypeDestinationLevel, TopicDestination, Granted, "A level (gain or mute) change reached a destination, applied live or by a restart.", nil},
	{TypeDestinationRolledOver, TopicDestination, Granted, "A file destination continued into a new file because the configured one already held footage.", nil},
	{TypeDestinationDelay, TopicDestination, Granted, "A destination's broadcast delay changed phase: buffering, on-air, draining, drained, cut, or dropping; data carries the delay and the buffer.", nil},

	// Ingest and failover.
	{TypeIngestPublished, TopicIngest, Granted, "Data started arriving on a programme's ingest.", nil},
	{TypeIngestDisconnected, TopicIngest, Granted, "Nothing has arrived on a programme's ingest for the disconnect window.", nil},
	{TypeFailoverSwitched, TopicFailover, Granted, "A programme switched between its primary, backup and slate sources.", nil},

	// Go-live and stop: the platform broadcast's own lifecycle.
	{TypeBroadcastLive, TopicBroadcast, Granted, "A platform broadcast was moved on air (or into testing) by polyemesis.", nil},
	{TypeBroadcastComplete, TopicBroadcast, Granted, "A platform broadcast was ended by polyemesis.", nil},
	{TypeBroadcastFault, TopicBroadcast, Granted, "A platform refused to move a broadcast; the stream itself is unaffected.", nil},

	// Recording.
	{TypeRecordingStarted, TopicRecording, Granted, "A programme's recorder started writing.", nil},
	{TypeRecordingStopped, TopicRecording, Granted, "A programme's recorder stopped writing; reason says why.", nil},

	// Chat is content a viewer wrote, under their name, and a moderation action
	// names the viewer it was taken against. A read token is refused the REST
	// scrollback (readScopeDeniedPatterns) and the live /ws copy (wsDrop); this
	// is a third door to the same content and gets the same answer.
	{TypeChatMessage, TopicChat, Withheld, "A chat message arrived from any connected platform.", nil},
	{TypeChatRetract, TopicChat, Withheld, "Chat messages that were delivered are gone: deleted, hidden, or swept by a ban.", nil},
	{TypeChatModeration, TopicChat, Withheld, "A moderation action was taken, by an operator or by automod.", nil},

	// The overlay's projection of chat (internal/overlay). A line is published
	// only once automod has judged the message and let it stand, carries no
	// author id, account, channel or moderation data, and is the one thing an
	// overlay key can read (OverlayVisible). Withheld from read tokens because
	// it is still chat a viewer wrote under their name.
	{TypeChatDisplay, TopicOverlay, Withheld, "A chat line cleared for on-screen display: judged by automod and stripped to what an overlay shows.", nil},
	{TypeChatUndisplay, TopicOverlay, Withheld, "Chat lines that were cleared for display are gone and must leave the screen.", nil},

	// Operator alerts, fired and resolved, after alerts.Event.Redacted. A read
	// token gets the ones GET /timeline already shows it and no others: the
	// audit half of the alert catalogue is not on the timeline, and a stream
	// that sent it would widen what a read token can see.
	{TypeAlert, TopicAlert, Partly, "An operator alert fired or resolved.", alertOnTheTimeline},

	// The stream's own bookkeeping. Carries no data from anywhere else.
	{TypeStreamGap, TopicStream, Granted, "Events this connection asked to replay are no longer held; reconcile over REST.", nil},
}

// overlayVisible is THE CLOSED SET an overlay key may read. Not derived from a
// topic or from ReadScope: what a key pasted into an OBS URL can see is decided
// here, by name, and TestOnlyTheOverlayRowsAreOverlayVisible pins it.
var overlayVisible = map[Type]bool{
	TypeChatDisplay:   true,
	TypeChatUndisplay: true,
}

// OverlayVisible reports whether an overlay key may be sent t.
func OverlayVisible(t Type) bool { return overlayVisible[t] }

// DisplayLine is one chat line as an overlay shows it. AN ALLOWLIST, not a
// chat.Message with fields blanked: anything not named here cannot reach an
// overlay, whatever is added to the message later. There is deliberately no
// author id, account, channel, reply target or moderation field.
type DisplayLine struct {
	// ID is opaque and per-process: a hash of the platform's message key, so
	// the platform's ids and the operator's account refs stay off the wire.
	ID       string `json:"id"`
	Platform string `json:"platform"`
	// Name is the author's display name, which every viewer already sees.
	Name        string         `json:"name"`
	Color       string         `json:"color,omitempty"`
	Badges      []DisplayBadge `json:"badges,omitempty"`
	Moderator   bool           `json:"moderator,omitempty"`
	Subscriber  bool           `json:"subscriber,omitempty"`
	Broadcaster bool           `json:"broadcaster,omitempty"`
	Text        string         `json:"text"`
	Action      bool           `json:"action,omitempty"`
	At          time.Time      `json:"at"`
}

// DisplayBadge is a badge as the platform shows it publicly.
type DisplayBadge struct {
	ID      string `json:"id"`
	Version string `json:"version,omitempty"`
	Label   string `json:"label,omitempty"`
}

// DisplayRemoval takes lines off the screen. One platform per removal, so a
// platform-filtered overlay is not even told how many lines another platform
// lost.
type DisplayRemoval struct {
	Platform string   `json:"platform"`
	IDs      []string `json:"ids"`
}

// Catalogue returns every emitted type, in the order docs/EVENTS.md lists them.
func Catalogue() []Spec { return append([]Spec(nil), catalogue...) }

// Lookup returns the catalogue row for t.
func Lookup(t Type) (Spec, bool) {
	for _, s := range catalogue {
		if s.Type == t {
			return s, true
		}
	}
	return Spec{}, false
}

// Topics returns every topic that has at least one type, sorted.
func Topics() []Topic {
	seen := map[Topic]bool{}
	var out []Topic
	for _, s := range catalogue {
		if !seen[s.Topic] {
			seen[s.Topic] = true
			out = append(out, s.Topic)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Envelope is one event as it goes on the wire, in the SSE data field.
//
// Flat, like hooks.Envelope, and for the same reason: `.destination.name` is the
// deepest a jq one-liner should have to reach.
type Envelope struct {
	SpecVersion string `json:"specVersion"`
	// ID is the SSE event id, repeated in the body so a consumer that logs
	// bodies can still resume. Empty on stream.gap, which is about this
	// connection rather than an event in the sequence.
	ID     string           `json:"id,omitempty"`
	Type   Type             `json:"type"`
	At     time.Time        `json:"at"`
	Source *hooks.SourceRef `json:"source,omitempty"`
	// Destination carries no URL and no stream key, and never will: it is the
	// webhook's DestinationRef, guarded by hooks' payload_test.go.
	Destination *hooks.DestinationRef `json:"destination,omitempty"`
	Reason      string                `json:"reason,omitempty"`
	Error       string                `json:"error,omitempty"`
	// Cause, Held and KeyChanged are the webhook's flat failure fields, so a
	// script reading one reads the other.
	Cause      failreason.Cause `json:"cause,omitempty"`
	Held       bool             `json:"held,omitempty"`
	KeyChanged bool             `json:"keyChanged,omitempty"`
	// Failure is the whole classified reason -- failreason.Reason's JSON, the
	// contract #20 pinned -- for a destination.down or destination.held that
	// had one. Its line is redacted again here.
	Failure *failreason.Reason `json:"failure,omitempty"`
	// Data is the type-specific payload for the families no webhook carries.
	Data any `json:"data,omitempty"`

	// retracted marks a ring entry -- a chat.display -- that a later
	// chat.undisplay took off screen. Never serialised; set only on the ring's
	// copy, under the hub's lock, and read by the overlay policy so a
	// reconnecting overlay is never replayed a line that was removed.
	retracted bool
}

// FromHook converts a webhook edge into an envelope. The one conversion, so the
// two surfaces cannot drift apart field by field.
func FromHook(ev hooks.Event) Envelope {
	src := ev.Source
	env := Envelope{
		Type:   Type(ev.Trigger),
		At:     ev.At,
		Source: &src,
		Reason: ev.Reason,
		Error:  ev.Error,
		Cause:  ev.Cause, Held: ev.Held, KeyChanged: ev.KeyChanged,
	}
	if ev.Destination != nil {
		d := *ev.Destination
		env.Destination = &d
	}
	return env
}
