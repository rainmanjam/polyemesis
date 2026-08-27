package meters

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/routing"
)

// Verdict is the pass/fail a destination's loudness earns against its target.
type Verdict string

const (
	// VerdictUnknown covers both "no target to judge against" and "not enough
	// programme yet". Neither is a failure, and a monitor that renders them as
	// one teaches operators to ignore the badge.
	VerdictUnknown Verdict = "unknown"
	VerdictPass    Verdict = "pass"
	VerdictWarn    Verdict = "warn"
	VerdictFail    Verdict = "fail"
)

func severity(v Verdict) int {
	switch v {
	case VerdictPass:
		return 1
	case VerdictWarn:
		return 2
	case VerdictFail:
		return 3
	default:
		return 0
	}
}

// worse returns the more serious of two verdicts, treating unknown as the
// least serious so a missing half of the answer never outranks a real one.
func worse(a, b Verdict) Verdict {
	if severity(b) > severity(a) {
		return b
	}
	return a
}

const (
	// ToleranceLU is how far from target still counts as compliant. EBU R128
	// allows ±1 LU on a live programme; anything tighter would flag every
	// well-mixed stream on the planet and be switched off within a week.
	ToleranceLU = 1.0
	// WarnToleranceLU is the band beyond that: audible, worth saying, but not
	// yet the sort of delivery a platform visibly re-levels.
	WarnToleranceLU = 2.0
	// MinIntegrationSeconds is how much programme must have passed before an
	// integrated reading earns a verdict. Integrated loudness is gated and
	// cumulative, so the first few seconds of any stream read wildly off — and
	// a monitor that shows FAIL for the first twenty seconds of every broadcast
	// is a monitor nobody looks at by the third one.
	MinIntegrationSeconds = 20.0
	// TruePeakFailOverDB is how far past the ceiling a true peak has to go
	// before it stops being a warning. A tenth of a decibel over is arithmetic;
	// a decibel over is a limiter that is not doing its job.
	TruePeakFailOverDB = 1.0
)

// TargetSource records where a target came from, so the UI can say "because
// you asked for it" or "because that is what YouTube does" rather than showing
// a number with no provenance.
type TargetSource string

const (
	TargetNone     TargetSource = "none"
	TargetProfile  TargetSource = "profile"
	TargetPlatform TargetSource = "platform"
)

// Target is what a destination's loudness is judged against.
type Target struct {
	LUFS         float64      `json:"lufs"`
	TruePeakDBTP float64      `json:"truePeakDbtp"`
	ToleranceLU  float64      `json:"toleranceLu"`
	Source       TargetSource `json:"source"`
	// Reason is the provenance in words, for the tooltip.
	Reason string `json:"reason,omitempty"`
}

// Configured reports whether there is anything to judge against.
func (t Target) Configured() bool { return t.Source != TargetNone }

// Sig is the target folded into a restart hash, so changing a destination's
// loudness target restarts its analyser and nothing else.
func (t Target) Sig() string {
	return fmt.Sprintf("%s/%.2f/%.2f/%.2f", t.Source, t.LUFS, t.TruePeakDBTP, t.ToleranceLU)
}

// TargetFor resolves the target for one destination.
//
// The profile's own Loudness wins, because it is the only place an operator
// stated an intention. Failing that the platform table supplies what that
// platform's own normalizer aims at, which is a far better default than
// silence — but it is labelled TargetPlatform so nobody mistakes an assumption
// for a decision. A platform with no opinion (a local file, a custom RTMP
// endpoint) gets no target at all, and its report says so instead of inventing
// a number to fail against.
func TargetFor(l *routing.Loudness, platform routing.Platform) Target {
	if l != nil && l.TargetLUFS != 0 {
		tp := l.TruePeakDB
		if tp == 0 {
			tp = routing.DefaultTruePeakDB
		}
		return Target{
			LUFS: l.TargetLUFS, TruePeakDBTP: tp, ToleranceLU: ToleranceLU,
			Source: TargetProfile,
			Reason: "this destination's own loudness target",
		}
	}
	pol := routing.PolicyFor(platform)
	if pol.TargetLUFS != 0 {
		return Target{
			LUFS: pol.TargetLUFS, TruePeakDBTP: routing.DefaultTruePeakDB,
			ToleranceLU: ToleranceLU, Source: TargetPlatform,
			Reason: fmt.Sprintf("what %s's own normalizer aims at", pol.Name),
		}
	}
	return Target{Source: TargetNone, Reason: "no loudness target is configured for this destination"}
}

// Evaluate judges one frame, returning the verdict, how far off target the
// integrated loudness is, and the sentence explaining it.
func (t Target) Evaluate(f Frame) (Verdict, float64, string) {
	if !t.Configured() {
		return VerdictUnknown, 0, "no loudness target is configured, so nothing is being judged"
	}
	tol := t.ToleranceLU
	if tol <= 0 {
		tol = ToleranceLU
	}

	// The true-peak half stands on its own: it is instantaneous, so it is
	// meaningful from the first frame, and an over is worth saying long before
	// there is enough programme for an integrated verdict.
	peak := VerdictPass
	peakReason := ""
	if over := f.TruePeakDBTP - t.TruePeakDBTP; over > 0 {
		peak = VerdictWarn
		if over > TruePeakFailOverDB {
			peak = VerdictFail
		}
		peakReason = fmt.Sprintf("true peak %+.1f dBTP is %.1f dB over the %+.1f dBTP ceiling",
			f.TruePeakDBTP, over, t.TruePeakDBTP)
	}

	if !f.Integrated || f.Seconds < MinIntegrationSeconds {
		reason := fmt.Sprintf("measuring: integrated loudness needs about %.0f more seconds of programme",
			math.Max(0, MinIntegrationSeconds-f.Seconds))
		if peakReason != "" {
			// The peak still gets to speak. Suppressing it until the
			// integrated window fills would hide a clipping mix for the first
			// twenty seconds of every broadcast, which is exactly when a
			// misconfigured gain is most likely to be caught.
			return peak, 0, peakReason + "; " + reason
		}
		return VerdictUnknown, 0, reason
	}

	dev := f.IntegratedLUFS - t.LUFS
	abs := math.Abs(dev)
	loud := VerdictPass
	switch {
	case abs > WarnToleranceLU:
		loud = VerdictFail
	case abs > tol:
		loud = VerdictWarn
	}

	reason := fmt.Sprintf("%+.1f LUFS integrated against a %+.1f LUFS target (%+.1f LU)",
		f.IntegratedLUFS, t.LUFS, dev)
	if abs <= tol {
		reason = fmt.Sprintf("%+.1f LUFS integrated, within %.0f LU of the %+.1f LUFS target",
			f.IntegratedLUFS, tol, t.LUFS)
	}
	if peakReason != "" {
		reason += "; " + peakReason
	}
	return worse(loud, peak), dev, reason
}

// Report is one destination's loudness state, as the dashboard renders it and
// the WebSocket carries it.
type Report struct {
	DestinationID int64  `json:"destinationId"`
	Destination   string `json:"destination"`
	Frame
	Target      Target    `json:"target"`
	Verdict     Verdict   `json:"verdict"`
	DeviationLU float64   `json:"deviationLu"`
	Reason      string    `json:"reason"`
	At          time.Time `json:"at"`
	// Error is set when the analyser itself could not run. A destination whose
	// meter is broken must never read as a destination that is compliant.
	Error string `json:"error,omitempty"`
}

// Observe folds one parsed frame into a report.
func Observe(id int64, name string, t Target, f Frame, at time.Time) Report {
	v, dev, reason := t.Evaluate(f)
	return Report{
		DestinationID: id, Destination: name, Frame: f,
		Target: t, Verdict: v, DeviationLU: dev, Reason: reason, At: at,
	}
}

// Starting is the placeholder report for an analyser that has just been
// spawned and has not printed anything yet. Without it a destination would
// simply be missing from the list, which reads as "not monitored" rather than
// "monitored, waiting".
func Starting(id int64, name string, t Target, at time.Time) Report {
	return Report{
		DestinationID: id, Destination: name, Target: t,
		Verdict: VerdictUnknown, Reason: "the loudness analyser is starting", At: at,
	}
}

// Failed is the report for an analyser that could not be started at all.
func Failed(id int64, name string, t Target, err string, at time.Time) Report {
	return Report{
		DestinationID: id, Destination: name, Target: t,
		Verdict: VerdictUnknown, Reason: "loudness is not being measured", Error: err, At: at,
	}
}

// StaleAfter is how long a report may go without a new frame before its verdict
// stops being asserted.
//
// THE MISTAKE: a publisher was killed mid-broadcast and two destinations held
// byte-identical LUFS values -- same seconds, same -12.601 -- for 65 seconds
// while a third kept measuring and walked to -21.3. The fallback feed lacked
// the tracks those two filter graphs map, so their analysers emitted no frames
// and the last reading simply stayed put. All three rendered identically, and
// with a target configured that meant a green pass on screen indefinitely for a
// destination being fed something else. The number was true once; it was being
// presented as true now. (#609)
//
// The headroom, so this never fires on a working meter: the analyser prints
// about ten frames a second (see Args), so a healthy report's At advances every
// ~100 ms. Ten seconds is a hundred missed frames, ten of the engine's
// once-a-second WebSocket pushes, and five of the meters page's two-second
// polls. It is also well past one supervisor restart cycle -- a one-second
// minimum backoff plus an FFmpeg start and a relay connect -- so an analyser
// that dies and comes back does not blink the verdict out on its way. A guard
// that cries wolf during normal operation gets ignored, which is worse than no
// guard at all.
const StaleAfter = 10 * time.Second

// Aged withdraws a verdict that nothing has re-measured.
//
// Only the verdict and the reason move. The numbers stay exactly as they were
// last measured, because the last known reading with an honest "as of" beside
// it is worth something to an operator -- and because zeroing DeviationLU would
// paint a perfect +0.0 LU, which is a worse lie than the one being fixed.
func (r Report) Aged(now time.Time) Report {
	// A report with no timestamp is not an old report. Anything built without
	// an At -- a hand-assembled fixture, a payload decoded from a peer that
	// omitted the field -- would otherwise be aged against year 1, which
	// overflows a Duration: removing this guard prints "nothing has been
	// measured for 9223372037 seconds" at an operator who has done nothing
	// wrong, and takes a working verdict off their screen to do it.
	if r.At.IsZero() {
		return r
	}
	// An unknown verdict already declines to judge, and it says why in prose an
	// operator can act on: the analyser is starting, it could not run at all,
	// or the integration window is still filling. Overwriting those with this
	// message would trade a specific answer for a vaguer one.
	if r.Verdict == VerdictUnknown {
		return r
	}
	age := now.Sub(r.At)
	if age < StaleAfter {
		return r
	}
	r.Verdict = VerdictUnknown
	r.Reason = fmt.Sprintf(
		"nothing has been measured for %.0f seconds: this destination's analyser has stopped producing frames, so the numbers below are the last ones it took and not the audio going out now",
		age.Seconds())
	return r
}

// Store holds the latest report per destination.
//
// Reports are published as they arrive, but a browser that connects mid-stream
// has to be given the current state from somewhere, and re-deriving it would
// mean waiting for the next frame.
//
// Reads age what they hand out (see Aged). Aging on the way out rather than at
// the call sites is deliberate: Report.At sat in this struct read by nobody --
// not this Store, not Engine.Loudness, not the UI -- and a stale reading was
// available to every one of them. There is now no way to take a report out of
// here without the clock having been applied to it.
type Store struct {
	mu      sync.RWMutex
	reports map[int64]Report
}

// NewStore creates an empty store.
func NewStore() *Store { return &Store{reports: map[int64]Report{}} }

// Put records the latest report for a destination.
func (s *Store) Put(r Report) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reports[r.DestinationID] = r
}

// Drop forgets one destination.
func (s *Store) Drop(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.reports, id)
}

// Keep forgets every destination not in ids, which is how a report for a
// destination that has been deleted stops being pushed to browsers forever.
func (s *Store) Keep(ids map[int64]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.reports {
		if !ids[id] {
			delete(s.reports, id)
		}
	}
}

// Get returns one report, aged against the clock.
func (s *Store) Get(id int64) (Report, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.reports[id]
	return r.Aged(time.Now()), ok
}

// All returns every report, aged against the clock and ordered by destination
// id so the dashboard does not reshuffle itself on every push.
func (s *Store) All() []Report {
	now := time.Now()
	s.mu.RLock()
	out := make([]Report, 0, len(s.reports))
	for _, r := range s.reports {
		out = append(out, r.Aged(now))
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].DestinationID < out[j].DestinationID })
	return out
}
