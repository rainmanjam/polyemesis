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
	// TypeTest is what the "send a test message" button raises. It is never
	// coalesced and never filtered, because a test that a rule quietly swallows
	// teaches the operator nothing.
	TypeTest Type = "test"
)

// AllTypes is every subscribable type, in the order a settings page should
// list them. TypeTest is absent on purpose: it is not something to subscribe
// to.
func AllTypes() []Type {
	return []Type{
		TypeDestinationDown, TypeDestinationRecovered,
		TypeIngestLost, TypeIngestRecovered,
		TypeFailoverSwitched,
		TypeClipping,
		TypeDiskLow, TypeDiskRecovered,
		TypeLoudnessOut, TypeLoudnessRecovered,
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
