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
	"context"
	"fmt"
	"log/slog"
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
}

// NewRecorder returns a recorder holding at most capacity records.
func NewRecorder(capacity int, secrets *alerts.SecretSet) *Recorder {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &Recorder{buf: make([]Record, capacity), secrets: secrets}
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
	r.buf[r.next] = rec
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
	r.seen++
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
		// Numbers, bools, times. Anything with a String or Error method is
		// handled above; what is left cannot carry a credential without having
		// been formatted first, and formatting is what the string cases cover.
		return v
	}
}

// Records returns what is held, oldest first.
func (r *Recorder) Records() []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		out := make([]Record, r.next)
		copy(out, r.buf[:r.next])
		return out
	}
	out := make([]Record, 0, len(r.buf))
	out = append(out, r.buf[r.next:]...)
	out = append(out, r.buf[:r.next]...)
	return out
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
