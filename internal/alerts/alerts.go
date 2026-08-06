// Package alerts turns "something happened that an operator would want to know
// about" into webhook deliveries.
//
// Three ideas keep it out of the streaming path's way. First, the engine only
// ever calls Publish, which is a non-blocking send onto a bounded queue: a
// webhook endpoint that takes thirty seconds to answer cannot slow a reconcile
// down, and a queue that fills drops alerts rather than applying backpressure
// to the thing that raised them. Second, everything an operator would see
// twice is coalesced: a destination that flaps every two seconds produces one
// message that says "12 times", not twelve messages. Third, nothing that
// reaches a payload has ever been near a stream key — see redact.go, which is
// applied on the way in rather than trusted to every caller.
//
// The state machines are deliberately separated from the goroutines that drive
// them. Watcher turns snapshots into events, coalescer turns events into
// deliveries, and both take the current time as an argument, so the debounce,
// the rate limit and the flap detection are all testable without a clock, a
// server or an engine.
package alerts

import "time"

// Type names what happened. Rules subscribe by type, so these strings are part
// of the stored configuration and must not be renamed.
type Type string

const (
	// TypeDestinationDown fires when a destination the operator enabled has
	// stopped delivering — the process is failed, or restarting, or the engine
	// could not build it at all.
	TypeDestinationDown Type = "destination.down"
	// TypeDestinationRecovered closes out a TypeDestinationDown.
	TypeDestinationRecovered Type = "destination.recovered"
	// TypeDestinationFallingBehind fires when a destination stops keeping up
	// with realtime, which is earlier and more useful than it going down.
	//
	// The measurement is FFmpeg's own speed ratio for that child. Almost every
	// destination here is `-c:v copy`, so there is barely any encoding work to
	// be slow at -- a passthrough sitting under 1.0 means FFmpeg is blocking on
	// the socket write to the platform. That is the same question a platform
	// health API would answer, arrived at from this side, for every destination
	// including a pasted stream key or a custom RTMP URL that no API can see.
	//
	// It reports the measurement and hedges the cause on purpose. Speed is a
	// symptom: a congested uplink, a platform throttling us and a slow disk
	// under a file destination all look identical from here, and naming the
	// wrong one sends somebody to fix something that is not broken.
	TypeDestinationFallingBehind Type = "destination.falling_behind"
	// TypeDestinationCaughtUp closes out a TypeDestinationFallingBehind.
	//
	// Deliberately NOT TypeDestinationRecovered, which already exists and pairs
	// with TypeDestinationDown. One message closing out two different
	// conditions would leave a reader unable to tell which had ended.
	TypeDestinationCaughtUp Type = "destination.caught_up"
	// TypeIngestLost fires when the source stops arriving.
	TypeIngestLost Type = "ingest.lost"
	// TypeIngestRecovered closes out a TypeIngestLost.
	TypeIngestRecovered Type = "ingest.recovered"
	// TypeFailoverSwitched fires on every source switch. An operator who
	// discovers at the end of a broadcast that they streamed the backup all
	// night is the failure this exists to prevent.
	TypeFailoverSwitched Type = "failover.switched"
	// TypeClipping fires when an ingest channel is sitting on the ceiling.
	TypeClipping Type = "audio.clipping"
	// TypeDiskLow fires when the recording volume is running out.
	TypeDiskLow Type = "disk.low"
	// TypeDiskRecovered closes out a TypeDiskLow.
	TypeDiskRecovered Type = "disk.recovered"
	// TypeLoudnessOut fires when a destination has been outside its loudness
	// target long enough that it is a mix problem rather than a quiet passage.
	TypeLoudnessOut Type = "loudness.out_of_compliance"
	// TypeLoudnessRecovered closes out a TypeLoudnessOut.
	TypeLoudnessRecovered Type = "loudness.recovered"
	// TypeLoginFailed fires when sign-in attempts from one address have passed
	// the throttle's free allowance. Not on the first failure: a login that
	// failed once is somebody mistyping their own password, and a channel that
	// says so out loud is a channel that has been muted before the first real
	// incident. It is also not raised on the throttled branch, where the
	// request is already being answered with a 429 before the password is read
	// -- publishing there would let an attacker set the event rate.
	TypeLoginFailed Type = "auth.login.failed"
	// TypeLoginSucceeded fires on every accepted sign-in. There is exactly one
	// account on an install, so this is the only signal an operator has that
	// somebody else is holding their password or their session cookie, neither
	// of which can be revoked individually. It carries how many failures
	// preceded it, which is the question a reader of TypeLoginFailed asks next.
	TypeLoginSucceeded Type = "auth.login.succeeded"
	// TypePasswordChanged fires when the admin password is replaced. Loud on
	// purpose. The false positive is one message the operator was expecting;
	// the false negative is never finding out that somebody locked them out of
	// their own server, because SetPassword ends every session as it goes.
	TypePasswordChanged Type = "auth.password.changed"
	// TypeAPITokenCreated fires when an API token is minted. A token acts as
	// the admin, is limited to no part of the API and never expires, and its
	// plaintext exists exactly once -- it is what somebody establishes to
	// survive the password change that would otherwise evict them.
	TypeAPITokenCreated Type = "auth.token.created"
	// TypeAPITokenRevoked fires when an API token is destroyed. It exists
	// because minting is alerted and the pair is what an operator reads: a
	// "created" with no matching "revoked" is a credential still out there, and
	// a "revoked" nobody performed is somebody closing a door behind them.
	// Without it the channel records only half of a token's life.
	TypeAPITokenRevoked Type = "auth.token.revoked"
	// TypeSettingsChanged fires when a settings save actually altered
	// something. It names the sections that changed and never a value; see
	// changedSections in internal/api/audit.go for why that is not
	// squeamishness.
	TypeSettingsChanged Type = "settings.changed"
	// TypeClipCaptured fires when a clip is cut from the replay buffer. Info,
	// and the quietest of the security set on purpose: on a busy stream this is
	// somebody doing their job, repeatedly. It is here because a clip is the
	// one operation that takes content OFF the server -- the alert is not
	// "something broke", it is a record that material left.
	TypeClipCaptured Type = "clip.captured"
	// TypeTest is what the "send a test message" button raises. It is never
	// coalesced and never filtered, because a test that a rule quietly swallows
	// teaches the operator nothing.
	TypeTest Type = "test"
)

// AllTypes is every subscribable type, in the order a settings page should
// list them: the streaming events first, because that is what an operator
// scanning the picker came for, then the security and configuration ones.
// TypeTest is absent on purpose: it is not something to subscribe to.
//
// The security types are APPENDED rather than interleaved, and they are in
// this list rather than in a catalogue of their own. Both of those are load
// bearing. Appending keeps every existing row of the picker where the operator
// last saw it; being in the list at all is what stops Rule.Normalized deleting
// a subscription to one of them, because Normalized drops anything KnownType
// does not recognise and db.scanAlertRule runs Normalized on every read.
//
// The cost is stated rather than hidden: a rule with an empty Events list means
// "everything", so an install that has never touched its subscriptions starts
// receiving these on upgrade. See docs/MONITORING.md for why that is the change
// we take rather than the one we migrate around.
func AllTypes() []Type {
	return []Type{
		TypeDestinationDown, TypeDestinationRecovered,
		TypeIngestLost, TypeIngestRecovered,
		TypeFailoverSwitched,
		TypeClipping,
		TypeDiskLow, TypeDiskRecovered,
		TypeLoudnessOut, TypeLoudnessRecovered,
		TypeLoginFailed, TypeLoginSucceeded,
		TypePasswordChanged, TypeAPITokenCreated, TypeAPITokenRevoked,
		TypeSettingsChanged, TypeClipCaptured,
		// Appended rather than filed beside the other destination events, which
		// is where they belong by meaning. The prefix above is pinned by test
		// precisely so that a picker row never moves under an operator who has
		// learned where it is, and grouping these correctly would move eight of
		// them. Meaning loses to stability here.
		TypeDestinationFallingBehind, TypeDestinationCaughtUp,
	}
}

// KnownType reports whether t is subscribable.
func KnownType(t Type) bool {
	for _, k := range AllTypes() {
		if k == t {
			return true
		}
	}
	return false
}

// Severity is how loudly to say it.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// rank orders severities so a rule can filter on a floor. An unknown severity
// ranks as info rather than being discarded: an alert nobody classified is
// still an alert.
func (s Severity) rank() int {
	switch s {
	case SeverityCritical:
		return 2
	case SeverityWarning:
		return 1
	default:
		return 0
	}
}

// AtLeast reports whether s is as severe as floor.
func (s Severity) AtLeast(floor Severity) bool { return s.rank() >= floor.rank() }

// Field is one labelled detail, rendered as an embed field by Discord and an
// attachment field by Slack. It is a slice rather than a map so the order the
// engine chose survives to the message.
type Field struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Event is one thing worth telling somebody about.
type Event struct {
	Type     Type     `json:"type"`
	Severity Severity `json:"severity"`
	// Key identifies the SUBJECT, not the occurrence: every "destination 3 is
	// down" carries the same key, and that is what lets a flapping destination
	// coalesce into one message instead of two hundred.
	Key    string    `json:"key"`
	Title  string    `json:"title"`
	Text   string    `json:"text,omitempty"`
	Fields []Field   `json:"fields,omitempty"`
	At     time.Time `json:"at"`
}

// WithField appends a detail. It is a method rather than a struct literal
// because most call sites add fields conditionally.
func (e Event) WithField(name, value string) Event {
	if value == "" {
		return e
	}
	e.Fields = append(append([]Field(nil), e.Fields...), Field{Name: name, Value: value})
	return e
}
