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

	"github.com/rainmanjam/polyemesis/internal/activity"
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
	// TopicOBS is the OBS bridge (#16): what OBS told polyemesis, over
	// obs-websocket or the OBS script, and whether the link is up.
	TopicOBS Topic = "obs"
	// TopicPoll is the cross-platform chat poll (#14, internal/polls).
	TopicPoll Topic = "poll"
	// TopicActivity is viewer and platform activity -- follows, subs, tips,
	// raids (internal/activity). Named "activity" and not "alert": the alert
	// topic is the operator's (internal/alerts) and stays that.
	TopicActivity Topic = "activity"
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
	TypeRolloverStarted       Type = Type(hooks.TriggerRolloverStarted)
	TypeRolloverCompleted     Type = Type(hooks.TriggerRolloverCompleted)
	TypeRolloverFailed        Type = Type(hooks.TriggerRolloverFailed)
	TypeDestinationDelay      Type = "destination.delay"
	TypeIngestPublished       Type = Type(hooks.TriggerIngestPublished)
	TypeIngestDisconnected    Type = Type(hooks.TriggerIngestDisconnected)
	TypeFailoverSwitched      Type = Type(hooks.TriggerFailoverSwitched)
	TypeBroadcastLive         Type = "broadcast.live"
	TypeBroadcastComplete     Type = "broadcast.complete"
	TypeBroadcastFault        Type = Type(hooks.TriggerBroadcastFault)
	TypeRecordingStarted      Type = "recording.started"
	TypeRecordingStopped      Type = "recording.stopped"
	TypeOffloadStuck          Type = Type(hooks.TriggerOffloadStuck)
	TypeChatMessage           Type = "chat.message"
	TypeChatRetract           Type = "chat.retract"
	TypeChatModeration        Type = "chat.moderation"
	TypeChatDisplay           Type = "chat.display"
	TypeChatUndisplay         Type = "chat.undisplay"
	TypePollStarted           Type = "poll.started"
	TypePollVote              Type = "poll.vote"
	TypePollEnded             Type = "poll.ended"
	TypePollDisplay           Type = "poll.display"
	TypeAlert                 Type = "alert"
	// TypeActivity is one viewer or platform activity; data.kind says which.
	// ONE type for every kind, so a platform source never edits the catalogue:
	// see activity.Kind. TypeActivityDisplay is its overlay projection.
	TypeActivity        Type = "activity"
	TypeActivityDisplay Type = "activity.display"
	TypeStreamGap       Type = "stream.gap"
	TypeOBSConnection   Type = "obs.connection"
	TypeOBSStream       Type = "obs.stream"
	TypeOBSScene        Type = "obs.scene"
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

// activityReadable is the activity row's ReadIf: a read token is sent the
// platform's own report that the stream went online or offline, which names no
// viewer and carries no viewer's words, and nothing else. A follow names a
// person; a tip, a resub and a redemption carry what they wrote. Anything that
// is not an activity.Event is withheld: fail closed.
func activityReadable(env Envelope) bool {
	a, ok := env.Data.(activity.Event)
	return ok && a.Kind == activity.KindStream
}

// catalogue is THE ONE PLACE an event type is declared to exist.
//
// Everything else is derived from it or checked against it: the ?topics=
// filter, the read-scope policy, docs/EVENTS.md's table (TestTheDocsListExactly
// TheCatalogue) and the requirement that each type is shown, by a test that
// drives its real producer, to actually be emitted (TestEveryCatalogued
// TypeHasAnEmissionTest). A type for a feature that does not exist yet --
// viewer samples (#4) -- is added HERE, by that feature, together with the test
// that proves its producer emits it. Until then it is documented under "Coming
// with …" and nowhere else. Polls (#14) did exactly that. The failover degraded
// reason (#6) needed no new type: it is the "degraded: " reason on
// failover.switched.
var catalogue = []Spec{
	// Destinations. Status is readable by a read token over REST and /ws
	// already; none of these carries a URL or a stream key, and every free-text
	// field is redacted by Publish.
	{TypeDestinationUp, TopicDestination, Granted, "A destination started delivering.", nil},
	{TypeDestinationDown, TopicDestination, Granted, "A destination stopped: failed, stalled, disabled or removed; reason says which, cause and failure say why.", nil},
	{TypeDestinationHeld, TopicDestination, Granted, "Polyemesis stopped retrying a destination after repeated quick rejections; nothing happens until Retry.", nil},
	{TypeDestinationRestarted, TopicDestination, Granted, "A destination's process was respawned: after it exited, or because a live level change restarted it; reason says which.", nil},
	{TypeDestinationLevel, TopicDestination, Granted, "A level (gain or mute) change reached a destination, applied live or by a restart.", nil},
	{TypeDestinationRolledOver, TopicDestination, Granted, "A file destination continued into a new file because the configured one already held footage.", nil},
	{TypeRolloverStarted, TopicDestination, Granted, "A planned rollover began: a replacement broadcast is being created for a live one. reason says which trigger fired.", nil},
	{TypeRolloverCompleted, TopicDestination, Granted, "A planned rollover finished: the replacement is live and the destination points at it. Connecting Twitch is a reconnect; reason says which.", nil},
	{TypeRolloverFailed, TopicDestination, Granted, "A planned rollover stopped. The old broadcast is still on air unless reason says it could not be ended; no ingest was touched.", nil},
	{TypeDestinationDelay, TopicDestination, Granted, "A destination's broadcast delay changed phase: buffering, on-air, draining, drained, cut, dropping, or dumped; data carries the delay and the buffer, and a dump says what it took.", nil},

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
	{TypeOffloadStuck, TopicRecording, Granted, "Offload stopped retrying a file on a storage target; nothing moves until Retry now. reason names the target, the file and the last error.", nil},

	// Chat is content a viewer wrote, under their name, and a moderation action
	// names the viewer it was taken against. A read token is refused the REST
	// scrollback (the readDenied flag) and the live /ws copy (wsDrop); this
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
	// The poll widget's projection: question, option labels and totals, and
	// nothing else -- no platform, so a Twitch-only scene shows no rival's
	// name, and no voter. Withheld from read tokens like every overlay row;
	// they get the same numbers, with the breakdown, from the poll topic.
	{TypePollDisplay, TopicOverlay, Withheld, "A poll as the on-stream widget shows it: question, options and totals, with no platform breakdown.", nil},

	// Polls (#14). Granted: the payload is the operator's question, the
	// options and per-platform COUNTS. Who voted for what stays inside
	// internal/polls and has no field on any payload, so a read token -- like
	// the dashboard -- sees the result and never a voter's id or name.
	{TypePollStarted, TopicPoll, Granted, "A poll opened: question, options, duration, and where it was announced.", nil},
	{TypePollVote, TopicPoll, Granted, "A poll's running tallies changed; sent at most once a second, never once per vote.", nil},
	{TypePollEnded, TopicPoll, Granted, "A poll closed, at its time or early: the final tallies with the per-platform breakdown.", nil},

	// Operator alerts, fired and resolved, after alerts.Event.Redacted. A read
	// token gets the ones GET /timeline already shows it and no others: the
	// audit half of the alert catalogue is not on the timeline, and a stream
	// that sent it would widen what a read token can see.
	{TypeAlert, TopicAlert, Partly, "An operator alert fired or resolved.", alertOnTheTimeline},

	// Viewer and platform activity (internal/activity). Two rows for fourteen
	// kinds, so that adding a platform's source never edits the catalogue, the
	// docs table or the overlay's closed set. The activity row names viewers and
	// carries text they wrote -- the reason chat is withheld -- so a read token
	// gets only the stream kind, which names nobody. The display row is the
	// overlay's projection: judged by automod, stripped of every id.
	{TypeActivity, TopicActivity, Partly, "A viewer or platform activity -- a follow, sub, tip, raid or the platform's own stream report; data.kind says which.", activityReadable},
	{TypeActivityDisplay, TopicOverlay, Withheld, "An activity as the alerts overlay draws it: judged by automod and stripped to what an overlay shows.", nil},

	// The OBS bridge (internal/obsbridge). What OBS reported, never what it was
	// asked: none of these carries the obs-websocket address or its password,
	// and the connection's detail is a fixed sentence chosen by polyemesis, not
	// an error string from the network. Granted, because a read token already
	// sees every destination these lead to start or stop.
	{TypeOBSConnection, TopicOBS, Granted, "Polyemesis's link to obs-websocket connected, dropped, or was refused.", nil},
	{TypeOBSStream, TopicOBS, Granted, "OBS started or stopped streaming, as reported by obs-websocket or the OBS script.", nil},
	{TypeOBSScene, TopicOBS, Granted, "OBS switched its program scene; preset names the routing rule the scene triggers, if any.", nil},

	// The stream's own bookkeeping. Carries no data from anywhere else.
	{TypeStreamGap, TopicStream, Granted, "Events this connection asked to replay are no longer held; reconcile over REST.", nil},
}

// overlayVisible is THE CLOSED SET an overlay key may read. Not derived from a
// topic or from ReadScope: what a key pasted into an OBS URL can see is decided
// here, by name, and TestOnlyTheOverlayRowsAreOverlayVisible pins it.
var overlayVisible = map[Type]bool{
	TypeChatDisplay:   true,
	TypeChatUndisplay: true,
	TypePollDisplay:   true,
	// The alerts overlay's projection (ActivityDisplay): names and amounts a
	// viewer already sees on stream, no id of any kind, and text only once
	// automod has let it stand.
	TypeActivityDisplay: true,
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

// PollDisplay is a poll as the on-stream widget draws it. AN ALLOWLIST, like
// DisplayLine: question, option labels and totals. There is deliberately no
// platform (the widget is logo-free and is shown on every overlay, including a
// Twitch-only one) and no voter of any kind.
type PollDisplay struct {
	// ID tells one poll from the next, so a widget resets rather than mixing
	// two polls' bars. Opaque.
	ID       string              `json:"id"`
	Question string              `json:"question"`
	Options  []PollDisplayOption `json:"options"`
	Total    int                 `json:"total"`
	// State is "open", "closing" (counting the last votes automod is still
	// judging) or "ended".
	State   string     `json:"state"`
	EndsAt  time.Time  `json:"endsAt"`
	EndedAt *time.Time `json:"endedAt,omitempty"`
}

// PollDisplayOption is one bar of the widget.
type PollDisplayOption struct {
	Label string `json:"label"`
	Votes int    `json:"votes"`
}

// ActivityDisplay is an activity as the alerts overlay draws it. AN ALLOWLIST,
// like DisplayLine: no actor id, account, source id, recipient id, breakdown
// or moderation field exists to be filled in. Kind is one of the overlay kinds
// (activity.OverlayKinds) and nothing else is ever published on this row.
type ActivityDisplay struct {
	// ID is opaque and per-process: a hash of the event's key, so the
	// platform's ids and the operator's account refs stay off the wire.
	ID       string `json:"id"`
	Platform string `json:"platform"`
	Kind     string `json:"kind"`
	// Name is who did it ("" when anonymous, or when the name was blanked
	// because it failed the blocked-terms check; the overlay says "Someone").
	Name   string           `json:"name"`
	Amount *activity.Amount `json:"amount,omitempty"`
	Tier   string           `json:"tier,omitempty"`
	Months int              `json:"months,omitempty"`
	Count  int              `json:"count,omitempty"`
	// Recipient is set only when Count is 1: a gift bomb names no one.
	Recipient string `json:"recipient,omitempty"`
	Title     string `json:"title,omitempty"`
	Viewers   int    `json:"viewers,omitempty"`
	Level     int    `json:"level,omitempty"`
	// Message is the viewer's own words, present only if automod released it
	// and the operator left overlay messages on.
	Message string    `json:"message,omitempty"`
	Test    bool      `json:"test,omitempty"`
	At      time.Time `json:"at"`
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
