// Package diag is the debug mode: a runtime log-level switch, a bounded
// recorder, and the scrubbing that has to sit between them.
//
// WHAT THIS IS FOR, AND WHY THAT DECIDES EVERYTHING ELSE. The recording is
// meant to leave the machine — an operator turns it on, reproduces a problem,
// and sends the result to somebody who does not have their server. That makes
// this a REDISTRIBUTION CHANNEL, not a log viewer, and every field in it is a
// disclosure decision rather than a formatting one.
//
// polyemesis has put stream keys in its own logs more than once. From the 0.7.0
// security notes alone: a key straddling the 300-byte error snippet printed
// unmasked; a key reaching server.log on the give-up path; ScrubDestinationText
// collecting a destination's secrets differently from its sibling. Every one of
// those was survivable because the log stayed on the operator's box. An export
// button changes the blast radius of the next one from "my server" to "an email
// attachment somebody keeps."
//
// SO THE SCRUBBING HAPPENS ON THE WAY IN, NOT ON THE WAY OUT. Scrubbing at
// export would mean the ring holds plaintext credentials in memory for the whole
// session, where a core dump, a memory inspection, or a second bug that prints
// the ring reaches them. Everything in the buffer has already been through the
// secret set.
//
// AND THE SECRET SET IS THE MECHANISM; alerts.Redact IS THE BACKSTOP. That is
// not a preference, it is what internal/alerts says about itself: "The correct
// reading of a Redact call is 'the declared secrets are already gone; this is
// the best-effort pass over what is left'." TestRedactIsCalledOnlyFromTheAllowlist
// fails the build on callers who forget it. So Recorder takes a *SecretSet built
// from the DECLARED secrets, and the residual pass runs after it rather than
// instead of it.
package diag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
)

// DefaultCapacity is how many records the ring holds.
//
// BOUNDED, AND THE BOUND IS THE FEATURE. "Record everything" on a box streaming
// for eight hours is a memory leak with a switch on it, and the useful window is
// almost always the last few minutes before somebody noticed. A ring that
// forgets the oldest line is the behaviour an operator already expects from
// every log they have ever read.
const DefaultCapacity = 5000

// Switch is the runtime log level, shared with the handler the process logs
// through.
//
// A LEVEL VAR RATHER THAN A REBUILT LOGGER. The alternative -- construct a new
// slog.Logger when the level changes -- means every component holding the old
// one keeps the old level, and those are handed out at startup to the engine,
// the notifier, the supervisor and everything else. slog.LevelVar exists for
// exactly this: one pointer, read on every record, changed from anywhere.
type Switch struct {
	lvl *slog.LevelVar
	// base is the level the process was started with, so turning debug mode off
	// returns to what the operator configured rather than to a guess.
	base slog.Level
}

// NewSwitch returns a Switch sitting at base.
func NewSwitch(base slog.Level) *Switch {
	v := new(slog.LevelVar)
	v.Set(base)
	return &Switch{lvl: v, base: base}
}

// Leveler is what slog.HandlerOptions wants.
func (s *Switch) Leveler() slog.Leveler { return s.lvl }

// Level reports the level in force.
func (s *Switch) Level() slog.Level { return s.lvl.Level() }

// Base reports the level the process was configured with.
func (s *Switch) Base() slog.Level { return s.base }

// SetDebug turns debug logging on, or returns to the configured level.
//
// Returning to BASE rather than to Info is the whole reason base is kept: an
// operator running at warn who flips debug on and off again should not silently
// end up at info, having quietly changed what their box records.
func (s *Switch) SetDebug(on bool) {
	if on {
		s.lvl.Set(slog.LevelDebug)
		return
	}
	s.lvl.Set(s.base)
}

// Enabled reports whether debug mode is on.
func (s *Switch) Enabled() bool { return s.lvl.Level() == slog.LevelDebug && s.base != slog.LevelDebug }

// Record is one captured line, already scrubbed.
type Record struct {
	At      time.Time      `json:"at"`
	Level   string         `json:"level"`
	Message string         `json:"message"`
	Attrs   map[string]any `json:"attrs,omitempty"`
	// Truncated marks a record that exceeded MaxRecordBytes and was cut. Stated
	// on the record itself rather than only counted in the capture, because the
	// engineer reading line 4,812 needs to know THAT line is short -- a total at
	// the top of the file does not tell them which ones.
	Truncated bool `json:"truncated,omitempty"`
}

// Recorder is the bounded ring, plus the secret set every record passes
// through on the way in.
type Recorder struct {
	mu      sync.Mutex
	buf     []Record
	next    int
	full    bool
	on      bool
	secrets *alerts.SecretSet
	// dropped counts records discarded because recording was off. Reported in
	// the bundle so a thin capture is visibly a thin capture rather than
	// silently one.
	seen uint64
	// sizes is the measured cost of each slot, parallel to buf, so eviction can
	// subtract what it evicts. Without it the running total only grows and would
	// report a capture as enormous long after the ring dropped the records that
	// made it so.
	sizes []int
	bytes int
	// truncated counts records that were cut to fit MaxRecordBytes, cumulative
	// like seen rather than per-slot: it describes the CAPTURE, not the bundle.
	truncated uint64
}

// NewRecorder returns a recorder holding at most capacity records.
func NewRecorder(capacity int, secrets *alerts.SecretSet) *Recorder {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &Recorder{
		buf:     make([]Record, capacity),
		sizes:   make([]int, capacity),
		secrets: secrets,
	}
}

// SetSecrets replaces the declared secrets.
//
// CALLED WHEN RECORDING STARTS, and NOT on every destination change -- an
// earlier version of this comment claimed the latter and no caller ever did it.
// The window that leaves open is a key rotated WHILE recording is already on,
// which is covered only by the residual alerts.Redact pass. Wiring this to the
// destination write would close it, and nothing does yet.
//
// The reason it matters at all: a stream key
// rotated mid-session is a secret this set has never seen: the ring would hold
// the new one in the clear while the old one -- the one nobody is using any more
// -- is the only value being masked. A set built once at startup is a set that
// is wrong by the end of the first key refresh.
func (r *Recorder) SetSecrets(s *alerts.SecretSet) {
	r.mu.Lock()
	r.secrets = s
	r.mu.Unlock()
}

// SetRecording starts or stops capture. Stopping does NOT clear what was
// captured: the operator's next action is usually to export it.
func (r *Recorder) SetRecording(on bool) {
	r.mu.Lock()
	r.on = on
	r.mu.Unlock()
}

// Recording reports whether capture is on.
func (r *Recorder) Recording() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.on
}

// Reset drops everything captured. Offered so an operator can start a clean
// reproduction rather than exporting a buffer full of unrelated history.
func (r *Recorder) Reset() {
	r.mu.Lock()
	r.next, r.full, r.seen = 0, false, 0
	r.bytes, r.truncated = 0, 0
	for i := range r.sizes {
		r.sizes[i] = 0
	}
	r.mu.Unlock()
}

// scrub applies the declared secrets and then the residual pass.
//
// BOTH, IN THIS ORDER, AND NEITHER ALONE. The set removes what polyemesis KNOWS
// is a credential -- exact literals, longest first, so a key that is a substring
// of the URL carrying it does not survive half-masked. alerts.Redact then covers
// the shapes nobody declared: a bearer token in an upstream error, a URL with
// credentials in it, a key=value pair from a platform's own message.
func (r *Recorder) scrub(s string) string {
	if s == "" {
		return s
	}
	return alerts.Redact(r.secrets.Scrub(s))
}

// Observe captures one record. Called from the handler; must not block.
func (r *Recorder) Observe(rec Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.on {
		return
	}
	rec.Message = r.scrub(rec.Message)
	if len(rec.Attrs) > 0 {
		out := make(map[string]any, len(rec.Attrs))
		for k, v := range rec.Attrs {
			// The KEY as well as the value: an attribute named after a
			// destination carries that destination's name into the export.
			out[r.scrub(k)] = scrubValue(r, v)
		}
		rec.Attrs = out
	}
	// THE CAP RUNS HERE, AFTER THE SCRUB AND NEVER BEFORE. Cutting first would
	// strand the front half of a credential as text the secret set no longer
	// matches -- it matches whole literals -- and send it to a stranger in
	// plaintext. See capRecord.
	rec, cut := capRecord(rec)
	rec.Truncated = cut
	if cut {
		r.truncated++
	}

	// Subtract what this slot held before overwriting it, so the total tracks
	// the ring rather than accumulating.
	r.bytes -= r.sizes[r.next]
	size := recordBytes(rec)
	r.sizes[r.next] = size
	r.bytes += size

	r.buf[r.next] = rec
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
	r.seen++
}

// Bytes reports the measured size of what the ring currently holds.
//
// APPROXIMATE, AND DELIBERATELY SO. It counts the payload -- messages, keys and
// values -- not the JSON syntax and indentation wrapped around them at export.
// It exists so the confirmation dialog can state a size before an operator
// sends the bundle to somebody else, and for that a number that is right to
// within the envelope overhead is the whole requirement.
func (r *Recorder) Bytes() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bytes
}

// TruncatedCount reports how many captured records were cut to fit the cap.
func (r *Recorder) TruncatedCount() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.truncated
}

// scrubValue walks a value, because slog attributes are not all strings and a
// key inside a []string or a nested map is still a key.
func scrubValue(r *Recorder, v any) any {
	switch t := v.(type) {
	case string:
		return r.scrub(t)
	case []string:
		out := make([]string, len(t))
		for i, s := range t {
			out[i] = r.scrub(s)
		}
		return out
	case error:
		if t == nil {
			return nil
		}
		return r.scrub(t.Error())
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			out[r.scrub(k)] = scrubValue(r, vv)
		}
		return out
	case fmt.Stringer:
		return r.scrub(t.String())
	default:
		// EVERYTHING ELSE IS RENDERED AND THEN SCRUBBED, and the previous comment
		// here was wrong in a way that leaked credentials. It claimed what reached
		// this branch "cannot carry a credential without having been formatted
		// first". slog.Any("detail", map[string]string{"token": key}) reaches it,
		// carries the key, and was passed through untouched: the declared set
		// never saw it and neither did the residual pass. Found by codex; present
		// on main, not introduced with the size cap.
		//
		// Rendering to JSON also gives the size cap something to measure. An
		// unrecognised value was priced at a flat 8 bytes, so a 1 MiB []byte made
		// a record measure about sixteen and capRecord waved it through as already
		// small.
		if isScalar(v) {
			return v
		}
		return r.scrub(renderForScrub(v))
	}
}

// renderForScrub turns an unrecognised value into text the exact-match set can
// still recognise its literals inside.
//
// RENDERING BEFORE SCRUBBING IS ONLY SAFE IF THE RENDERING PRESERVES THE BYTES.
// json.Marshal does not, in two ways that both hid a declared credential:
//
//	IT ESCAPES & < AND > BY DEFAULT. A camera or CDN password containing an
//	ampersand -- ordinary, and collected by urlSecrets as a declared literal --
//	became p&ssw0rd… in the text handed to Scrub, which is a strings.Replace
//	over exact literals and therefore matched nothing. SetEscapeHTML(false) is
//	the fix; the bundle is a diagnostic file, never an HTML document.
//
//	IT BASE64-ENCODES A []byte. The credential is then present, unrecognisable
//	to the scrub, and decodable in one step by whoever receives the bundle.
//	Handled ahead of the marshal, as text.
func renderForScrub(v any) string {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	return strings.TrimRight(buf.String(), "\n")
}

// isScalar reports whether a value is small, unshortenable, and incapable of
// hiding a credential inside itself.
//
// Anything with a String or Error method is handled before this is reached, so
// what is left is genuinely a number or a bool. Those are kept in their own JSON
// form rather than stringified, because an engineer filtering on `"attempt": 3`
// should not have to match `"3"`.
func isScalar(v any) bool {
	switch v.(type) {
	case nil, bool,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return true
	}
	return false
}

// Records returns what is held, oldest first.
func (r *Recorder) Records() []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.recordsLocked()
}

// recordsLocked is Records without the lock, so Snapshot can take the records
// and the counters describing them in one critical section.
func (r *Recorder) recordsLocked() []Record {
	out := make([]Record, 0, len(r.buf))
	if !r.full {
		out = append(out, r.buf[:r.next]...)
	} else {
		out = append(out, r.buf[r.next:]...)
		out = append(out, r.buf[:r.next]...)
	}
	// THE ATTRIBUTE MAPS ARE COPIED, NOT SHARED. Copying the Record struct alone
	// left every caller holding the ring's own map: mutating it changed a
	// retained record while its recorded size did not move, so the accounting
	// silently stopped describing the contents.
	for i := range out {
		if len(out[i].Attrs) == 0 {
			continue
		}
		m := make(map[string]any, len(out[i].Attrs))
		for k, v := range out[i].Attrs {
			m[k] = v
		}
		out[i].Attrs = m
	}
	return out
}

// Snapshot returns the held records together with the counters that describe
// them, taken in one critical section.
//
// BECAUSE FOUR SEPARATE READS DESCRIBED FOUR DIFFERENT MOMENTS. Build called
// Records, Bytes, Seen and TruncatedCount in turn, each taking the lock on its
// own, so an Observe landing between them produced a bundle whose stated size
// belonged to a ring state its records did not come from -- and a Reset landing
// between them produced records with a size of zero. The number an operator is
// shown before sending a file has to be that file's number.
func (r *Recorder) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Snapshot{
		Records:   r.recordsLocked(),
		Bytes:     r.bytes,
		Seen:      r.seen,
		Truncated: r.truncated,
		Capacity:  len(r.buf),
		Recording: r.on,
	}
}

// Snapshot is one consistent view of the ring. A struct rather than six return
// values because every field here is a number and positional returns would be
// swappable without the compiler noticing.
type Snapshot struct {
	Records   []Record
	Bytes     int
	Seen      uint64
	Truncated uint64
	Capacity  int
	Recording bool
}

// Seen reports how many records were captured, which may exceed what is held.
// The bundle reports both so a truncated capture says so.
func (r *Recorder) Seen() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seen
}

// Handler wraps another slog.Handler, copying every record into the recorder.
//
// A WRAPPER RATHER THAN A REPLACEMENT: the process keeps logging exactly where
// it logged before, at exactly the same level, and the recorder is a second
// consumer. Nothing about the existing output changes when debug mode is off,
// which is what makes this safe to leave wired in permanently.
type Handler struct {
	inner slog.Handler
	rec   *Recorder
	attrs []slog.Attr
	group string
}

// NewHandler wraps inner.
func NewHandler(inner slog.Handler, rec *Recorder) *Handler {
	return &Handler{inner: inner, rec: rec}
}

func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *Handler) WithAttrs(as []slog.Attr) slog.Handler {
	return &Handler{inner: h.inner.WithAttrs(as), rec: h.rec,
		attrs: append(append([]slog.Attr(nil), h.attrs...), as...), group: h.group}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{inner: h.inner.WithGroup(name), rec: h.rec, attrs: h.attrs, group: name}
}

func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	if h.rec != nil && h.rec.Recording() {
		out := Record{At: r.Time, Level: r.Level.String(), Message: r.Message}
		attrs := map[string]any{}
		for _, a := range h.attrs {
			attrs[a.Key] = a.Value.Any()
		}
		r.Attrs(func(a slog.Attr) bool {
			attrs[a.Key] = a.Value.Any()
			return true
		})
		if len(attrs) > 0 {
			out.Attrs = attrs
		}
		h.rec.Observe(out)
	}
	return h.inner.Handle(ctx, r)
}

// Capacity is the ring's size, so a caller can tell an operator "5,000 of
// 22,431 kept" rather than showing a number that looks complete and is not.
func (r *Recorder) Capacity() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.buf)
}
