// The setup cache: what a subscriber joining mid-stream needs replayed before
// the live messages make any sense.
//
// Separated from the server because it is a self-contained value type. Nothing
// here touches a socket, a lock or a Server, which means the E-RTMP multitrack
// slotting -- the part that is genuinely intricate -- can be tested without a
// handshake.
package rtmpserver

import (
	"fmt"
	"sync"
	"time"

	"github.com/bluenviron/gortmplib/pkg/message"
)

// stream is one SOURCE SLOT: the live publisher's setup messages and everyone
// currently reading it.
//
// Keyed by PublisherKey, NOT by the string the publisher typed. A source has
// several valid keys at once — the current token, the previous one during a
// rotation grace window, and any grandfathered legacy key — and they all mean
// the same programme. Keying this table by the raw string put a publisher who
// used a still-valid old key into a different bucket from the FFmpeg subscribed
// under the new one: admitted, counted as publishing, UI green, bytes fanned
// out to nobody. A refusal would have been better, because a refusal shows red
// in OBS.
type stream struct {
	// mu guards everything below, so the per-message forwarding path does not
	// have to take the SERVER-WIDE lock.
	//
	// pump took s.mu for every RTMP message and fanned out under it, which put
	// a few hundred acquisitions a second of the admission lock on the video
	// path. The contention was small -- an uncontended mutex is tens of
	// nanoseconds -- but it coupled two things that have nothing to do with
	// each other: whether a new publisher can be admitted should not queue
	// behind an existing one's frames.
	//
	// LOCK ORDER: Server.mu then stream.mu, never the reverse. Everything that
	// needs both looks the stream up under s.mu and then takes this.
	mu sync.Mutex

	// setup is replayed, in order, to every new subscriber. Order matters:
	// metadata before sequence headers is what a decoder expects.
	setup []message.Message
	// slots maps a setup message's identity to its position in setup, so a
	// republished sequence start overwrites the one it supersedes rather than
	// being appended after it. Without this the replay list grew for the life of
	// the broadcast and ended with stale configuration ahead of current.
	slots map[string]int
	subs  map[*subscriber]struct{}
	// emptySince is when subs last became empty, or the zero time while
	// something is reading. See the mid-session readiness check in pump.
	emptySince time.Time
}

// subscriberCount is how many consumers are reading this stream. Callers hold
// Server.mu; this takes the stream's own lock inside it, which is the order
// everything that needs both uses.
func (st *stream) subscriberCount() int {
	st.mu.Lock()
	defer st.mu.Unlock()
	return len(st.subs)
}

// resetSetup forgets the previous session's stream configuration.
//
// setup and slots are ONE structure in two fields — slots holds indices into
// setup — so they are cleared together, here, rather than at the call site.
// Clearing only setup left every index dangling and the next sequence start
// wrote past the end of an empty slice, panicking the whole listener on the
// ordinary event of an encoder reconnecting.
func (st *stream) resetSetup() {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.setup = nil
	st.slots = map[string]int{}
	// The empty-subscriber clock belongs to a SESSION, not to the slot.
	//
	// A stream that sat with no subscriber for a while keeps its emptySince, and
	// resetSetup runs as a publisher is admitted. Left alone, a publisher whose
	// subscriber dropped a moment after admission would be disconnected on its
	// very first message against a clock that started before it existed, instead
	// of getting the fifteen seconds the grace is for.
	st.emptySince = time.Time{}
}

// cacheSetup records a stream-configuration message for replay to late
// subscribers, replacing any earlier message occupying the same slot.
//
// CALLER MUST HOLD st.mu. Unlike resetSetup, which locks internally, this runs
// on the per-message path where pump already holds the lock for the fan-out.
func (st *stream) cacheSetup(msg message.Message) {
	slot, ok := setupSlot(msg)
	if !ok || !isSetup(msg) {
		return
	}
	if st.slots == nil {
		st.slots = map[string]int{}
	}
	if at, seen := st.slots[slot]; seen {
		st.setup[at] = msg
		return
	}
	st.slots[slot] = len(st.setup)
	st.setup = append(st.setup, msg)
}

// isSetup reports whether a message is stream setup that a subscriber joining
// later cannot do without.
//
// A subscriber that arrives mid-stream has missed the metadata and the codec
// sequence headers, and without them it has a byte stream it cannot interpret.
// Caching these and replaying them on subscribe is what makes the order of
// "encoder connects" and "FFmpeg connects" not matter — and it WILL vary, since
// an encoder reconnecting mid-session is routine.
//
// Deliberately generous: replaying a message that was not strictly needed costs
// a few bytes once, while missing one costs a subscriber that never decodes.
func isSetup(msg message.Message) bool {
	switch m := msg.(type) {
	case *message.DataAMF0:
		return true // onMetaData and friends
	case *message.Video:
		return m.Type == message.VideoTypeConfig
	case *message.Audio:
		return m.AACType == message.AudioAACTypeConfig
	case *message.VideoExSequenceStart, *message.AudioExSequenceStart,
		*message.AudioExMultichannelConfig:
		return true // Enhanced RTMP setup, including multitrack channel config
	// THE WRAPPER, which is how every track after the first one arrives.
	//
	// E-RTMP multitrack does not send a bare AudioExSequenceStart per track: it
	// sends AudioExMultitrack carrying a TrackID and a Wrapped message, and the
	// sequence start for tracks 2..N is inside that. Matching only the unwrapped
	// types cached the LEGACY track's config and nothing else, so a late-joining
	// subscriber got decoder config for one track and never for the rest — and
	// ffprobe, which is exactly such a subscriber, hung forever instead of
	// failing, because it was still waiting to identify streams it had the data
	// for but no configuration for.
	//
	// That is the whole multitrack feature failing for anything that attaches
	// after the publisher, which is the normal case: the engine's ingest child
	// subscribes when the source is enabled, and the operator hits Start in OBS
	// whenever they like.
	case *message.AudioExMultitrack:
		return isSetup(m.Wrapped)
	case *message.VideoExMultitrack:
		return isSetup(m.Wrapped)
	}
	return false
}

// setupSlot identifies WHICH piece of setup a message is, so a republished one
// replaces its predecessor instead of being appended beside it.
//
// Encoders resend configuration: OBS repeats sequence starts, and any publisher
// that changes a track mid-stream sends a fresh one. Appending blindly grew the
// replay list for the lifetime of the broadcast and handed every new subscriber
// a longer and longer prologue, ending with stale configuration replayed BEFORE
// the current one. Slot-keyed, the list stays at one entry per track per kind
// and always holds the newest.
func setupSlot(msg message.Message) (string, bool) {
	switch m := msg.(type) {
	case *message.DataAMF0:
		// Per NAME, not one shared "meta" slot.
		//
		// Every AMF0 data message landed in the same slot, so any mid-stream
		// one REPLACED the cached onMetaData -- and a cue point is an ordinary
		// thing for an encoder to send. Every subscriber attaching afterwards
		// then got that cue point replayed where its metadata should have been,
		// including the engine's own FFmpeg, whose first act on connecting is
		// to identify the streams.
		return "meta-" + dataAMF0Name(m), true
	case *message.Video:
		return "video", true
	case *message.Audio:
		return "audio", true
	case *message.VideoExSequenceStart:
		return "video-ex", true
	case *message.AudioExSequenceStart:
		return "audio-ex", true
	case *message.AudioExMultichannelConfig:
		return "audio-ex-channels", true
	// Per TRACK, which is the point: two tracks' sequence starts are different
	// setup, not the same setup sent twice.
	case *message.AudioExMultitrack:
		inner, ok := setupSlot(m.Wrapped)
		return fmt.Sprintf("audio-mt-%d-%s", m.TrackID, inner), ok
	case *message.VideoExMultitrack:
		inner, ok := setupSlot(m.Wrapped)
		return fmt.Sprintf("video-mt-%d-%s", m.TrackID, inner), ok
	}
	return "", false
}

// dataAMF0Name is the event an AMF0 data message carries: "onMetaData",
// "onCuePoint", "onTextData".
//
// The payload is a name followed by its arguments, except that publishers
// conventionally wrap it: OBS sends ["@setDataFrame", "onMetaData", {...}].
// The wrapper is a delivery instruction rather than the event, so it is skipped
// and the next string is the answer.
//
// An unnamed or empty payload gets a stable slot of its own rather than being
// folded in with onMetaData, because the one thing that must not happen is two
// different events sharing a slot.
func dataAMF0Name(m *message.DataAMF0) string {
	for _, v := range m.Payload {
		s, ok := v.(string)
		if !ok || s == "" {
			continue
		}
		if s == "@setDataFrame" {
			continue
		}
		return s
	}
	return "unnamed"
}
