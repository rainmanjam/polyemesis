package api

import (
	"encoding/json"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/events"
)

// The WebSocket's redaction policy: what a read-scoped subscriber may see, per
// event type, over a CLOSED table.
//
// The defect this closes is stated precisely, because an imprecise version was
// in circulation and would have led somewhere else: /ws is NOT unauthenticated.
// It sits inside requireAuth and requireScope (api.go), and a read scope is
// welcome there deliberately -- watching a stream go out is the monitoring use
// case API scopes exist for, and denying it would be a feature regression
// dressed as a fix.
//
// What was wrong is that the RENDERING never consulted the principal. writeEvent
// was a bare json.Marshal of whatever the broker fanned out, so every event
// reached every socket in its admin shape. Three principals, one body.
//
// FAIL-CLOSED is the other half, and it is the half that generalises. The old
// default for an event type nobody had thought about was SEND, so each new type
// was a disclosure waiting for its author to forget. Here an unclassified type
// is DROPPED for a read scope, and the build fails until somebody classifies it
// -- see TestEveryEventTypeHasAWebSocketPolicy.

// wsPolicy says what happens to one event type on a read-scoped socket.
type wsPolicy int

const (
	// wsPassthrough sends the event unchanged. Permitted ONLY where the payload
	// carries no stored credential at all, or where every leaf that could was
	// already scrubbed at the point the copy was made.
	wsPassthrough wsPolicy = iota
	// wsRedactText runs alerts.Redact over the payload's string leaves as a
	// RESIDUAL pass, preserving the wire shape. For payloads carrying text
	// somebody else authored. Best-effort, and claimed as nothing more.
	wsRedactText
	// wsDrop sends nothing to a read scope. Nothing uses it today; it is here
	// because the honest answer for some future payload is "not to this
	// principal", and a table with no way to say that would get an entry it
	// does not mean.
	wsDrop
)

// wsEventPolicy is the closed table. Every events.Type must appear, and the
// reason for each entry is written beside it so a reviewer can check the claim
// rather than the shape.
//
// Traced rather than assumed: every producer was enumerated (grep for
// Publish(events. across internal and cmd) and each payload followed to its leaf
// fields. No event type currently carries a stored credential block -- not
// db.Settings, not db.Source, not the playout token. The one that carried a
// credential in practice was TypeLog, whose text is an FFmpeg argv echo, and it
// is fixed where it is produced rather than here: supervisor's appendLog scrubs
// the process's exact secret literals before the line reaches the log ring, the
// on-disk process.log or this bus. Fixing it here instead would have left the
// other two sinks -- a file that ends up in support tarballs, and a RETAINED
// MQTT topic -- still carrying it, because neither has a principal.
var wsEventPolicy = map[events.Type]wsPolicy{
	// Scrubbed at the source, unconditionally, at the single point every copy
	// is made. See supervisor.(*Process).scrub.
	events.TypeLog:    wsPassthrough,
	events.TypeStatus: wsPassthrough,

	// Measurements. Metering frames, host CPU and bitrate points, and an EBU
	// R128 report have no stored field behind them.
	events.TypeLevels:   wsPassthrough,
	events.TypeStats:    wsPassthrough,
	events.TypeLoudness: wsPassthrough,

	// Bare signals that a list changed; both publish a nil payload. The list
	// itself is fetched over REST, where its own gate applies.
	events.TypeRecordings: wsPassthrough,
	events.TypeClips:      wsPassthrough,

	// engine.SourceInfo: the probed track layout, the video parameters, and the
	// operator's track annotations. Followed to its leaves -- it embeds no
	// db.Source and no ingest block. The annotations are operator-authored free
	// text, so the residual pass applies to them for the same reason it applies
	// to chat.
	events.TypeSource: wsRedactText,

	// Text authored elsewhere and passed through. A chat message or a caption
	// can contain anything a viewer typed, including a key pasted into the
	// wrong window.
	events.TypeChat:    wsRedactText,
	events.TypeCaption: wsRedactText,

	// A platform name, a state word, a reason string, and message ids.
	events.TypeChatState:   wsPassthrough,
	events.TypeChatRetract: wsPassthrough,
}

// eventView renders one event for one principal, returning ok=false when
// nothing may be sent.
//
// COPY BEFORE BLANK, and that is not a style note. events.Broker fans ONE Event
// VALUE to every subscriber, and its Data is an interface holding state shared
// with the producer. Redacting in place would blank the field for the admin
// socket next in the fan-out, and which socket loses would depend on map
// iteration order. Everything below rebuilds Data and assigns it to the local
// copy of the Event; nothing writes through a pointer into the payload.
func eventView(ev events.Event, readOnly bool) (events.Event, bool) {
	if !readOnly {
		return ev, true
	}
	policy, classified := wsEventPolicy[ev.Type]
	if !classified {
		// FAIL CLOSED. A type this build does not recognise is withheld rather
		// than sent. TestEveryEventTypeHasAWebSocketPolicy makes reaching this
		// a build failure rather than a silent drop, so this branch is the
		// safety net under the guard rather than the policy itself.
		return events.Event{}, false
	}
	switch policy {
	case wsDrop:
		return events.Event{}, false
	case wsRedactText:
		ev.Data = redactEventText(ev.Data)
		return ev, true
	default:
		return ev, true
	}
}

// redactEventText applies the residual pass to every string leaf of a payload
// while PRESERVING ITS WIRE SHAPE.
//
// Through the JSON form rather than a type switch, because the payload types
// come from three chat adapters, the caption pipeline and the engine, and a
// switch here would silently pass through whichever one nobody updated. Through
// the decoded tree rather than the raw bytes, because running a text redactor
// over JSON source can in principle produce something that is no longer JSON,
// and a socket frame the browser cannot parse is an outage.
//
// A field whose NAME says credential is masked outright rather than pattern-
// matched, which is the same rule alerts.Event.Redacted already applies.
func redactEventText(data any) any {
	if data == nil {
		return nil
	}
	b, err := json.Marshal(data)
	if err != nil {
		return nil
	}
	var tree any
	if err := json.Unmarshal(b, &tree); err != nil {
		return nil
	}
	return redactJSONTree(tree)
}

func redactJSONTree(v any) any {
	switch t := v.(type) {
	case string:
		return alerts.Redact(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			if alerts.SecretName(k) {
				out[k] = alerts.Mask
				continue
			}
			out[k] = redactJSONTree(e)
		}
		return out
	case []any:
		// AN ARRAY OF STRINGS IS THE ONE SHAPE WHERE ELEMENT-WISE REDACTION IS
		// THE BUG (#162), because an array of strings in a payload is an ARGV,
		// and an argv is where a credential arrives split across elements.
		//
		//	{"argv":["-headers","Authorization:","Bearer","<key>"]}
		//
		// Redact over each element in turn masks NOTHING there: "Authorization:"
		// has no value after the separator, "Bearer" has no token after it, and
		// "<key>" has no label before it. Over the JOIN it masks both. That is
		// the whole of #162 -- Redact's grammar is `label SEP value`, and the
		// split severs every one of its rules -- and the same argv reaches
		// /processes and the log ring, where #150 fixed it STRUCTURALLY with
		// supervisor.Spec.Secrets rather than with more patterns.
		//
		// There is no exact set to consult here: this tree is decoded from
		// somebody else's payload and nothing declared what in it is secret. So
		// the array is treated as ALL-OR-NOTHING, which is sound in the only
		// direction that matters:
		//
		//	Redact replaces SUBSTRINGS. Every element is a substring of the
		//	space-joined text, and the join only ever ADDS context (it can
		//	complete a `label SEP value` that the split broke; it cannot break
		//	one, because every rule stops at whitespace or at a word boundary
		//	and a space is both). So an UNCHANGED join proves every element is
		//	unchanged, and there is nothing to mask.
		//
		// A CHANGED join proves the opposite -- something in this array is a
		// credential -- but not WHICH element, and guessing is the grammar that
		// already failed. The array collapses to a single mask.
		//
		// The cost is a wire-shape change on exactly the arrays that would
		// otherwise leak, which is the correct trade for a read-scoped socket
		// and is measured rather than assumed: TestReadScopedFramesAreUnchanged
		// drives today's real Source, Chat and Caption payloads through this
		// function and asserts byte-identity.
		if joined, ok := joinedStrings(t); ok && alerts.Redact(joined) != joined {
			return []any{alerts.Mask}
		}
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = redactJSONTree(e)
		}
		return out
	default:
		return v
	}
}

// joinedStrings joins the STRING elements of a decoded JSON array with a
// single space, reporting false when there are none.
//
// Only the direct string elements: a nested object or array carries its own
// structure and is handled by the recursion, and flattening it into this text
// would make an unrelated neighbour's contents decide whether this array is
// masked.
//
// Non-string elements are SKIPPED rather than disqualifying the array, and that
// asymmetry is deliberate. Requiring an array to be wholly strings before the
// rule applies was the reviewer's suggestion and it is the wrong trade here: one
// nested object anywhere in an argv would switch the rule off and hand the
// remaining elements back to the per-element path, which is the path that masks
// nothing on a split header. The cost of the choice made instead is that a mixed
// array whose strings happen to join into a match collapses when it perhaps need
// not -- an over-mask, in the safe direction, on a shape nothing in this build
// produces. See the "a non-string neighbour must not switch the rule off" row in
// TestRedactJSONTreeDoesNotLeakAnArgvHeader.
//
// The separator is a space and not the empty string on purpose. Joining with
// nothing would GLUE two innocent elements into a match -- ["price", "=", "9"]
// is not a credential and "price=9" reads like one to a `label SEP value`
// grammar -- so an empty separator would over-mask, while a space reproduces
// exactly the text the process would have had on one command line.
func joinedStrings(arr []any) (string, bool) {
	var b strings.Builder
	n := 0
	for _, e := range arr {
		s, ok := e.(string)
		if !ok {
			continue
		}
		if n > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(s)
		n++
	}
	return b.String(), n > 0
}
