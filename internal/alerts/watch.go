package alerts

import (
	"fmt"
	"sort"
	"strconv"
	"time"
)

// Watcher defaults.
const (
	// DefaultDownFor is how long a thing must be down before it is worth
	// waking somebody for. It is longer than a supervisor restart cycle on
	// purpose: FFmpeg reconnecting to an RTMP endpoint is normal operation, not
	// an incident.
	DefaultDownFor = 20 * time.Second
	// DefaultClipDBFS is the ceiling. Digital full scale is 0; -0.1 is where a
	// limiter is already working and an encoder is about to make it audible.
	DefaultClipDBFS = -0.1
	// DefaultClipHits is how many consecutive observations must be on the
	// ceiling. One sample is a snare hit; several in a row is a level problem.
	DefaultClipHits = 3
	// DefaultDiskFloorBytes and DefaultDiskFloorPercent are the free-space
	// floors. Either one triggers: a small volume runs out of percent slowly
	// and a large one runs out of gigabytes slowly, and both end the same way.
	DefaultDiskFloorBytes   = uint64(2) << 30
	DefaultDiskFloorPercent = 5.0
	// DefaultSpeedFloor and DefaultFallingBehindFor gate
	// TypeDestinationFallingBehind. See the fields on WatchConfig: both are
	// argued rather than measured, and want revisiting against a real
	// broadcast's speed distribution.
	DefaultSpeedFloor       = 0.95
	DefaultFallingBehindFor = 30 * time.Second
	// DefaultSpeedWindow is how much history the delivery rate is measured
	// over. FFmpeg reports progress every half second, so the output time a
	// snapshot carries is up to 0.5s stale; over 20 seconds that is at most
	// 2.5% of error, comfortably inside the 5% between realtime and the floor.
	// A shorter window would put ordinary sampling jitter at the floor.
	DefaultSpeedWindow = 20 * time.Second
	// DefaultLoudnessFor is how long a destination must be out of tolerance.
	// EBU R128 integrates over the whole programme, so a minute of drift is a
	// mix that is wrong rather than a quiet passage.
	DefaultLoudnessFor = 90 * time.Second
)

// WatchConfig tunes the thresholds. A zero value takes every default.
type WatchConfig struct {
	DownFor          time.Duration
	ClipDBFS         float64
	ClipHits         int
	DiskFloorBytes   uint64
	DiskFloorPercent float64
	LoudnessFor      time.Duration
	// SpeedFloor is the encoder speed below which a destination is judged not
	// to be keeping up, and FallingBehindFor is how long it must stay there.
	//
	// Both are placeholders chosen by argument rather than measurement, and
	// should be revisited against a real broadcast: 1.0 is the target so a few
	// percent under is ordinary jitter, and 30s is long enough to ride out a
	// keyframe-alignment stall while still mattering inside a short stream.
	SpeedFloor       float64
	FallingBehindFor time.Duration
	// SpeedWindow is how far back the delivery rate looks. See
	// DefaultSpeedWindow.
	SpeedWindow time.Duration
}

func (c WatchConfig) normalized() WatchConfig {
	if c.DownFor <= 0 {
		c.DownFor = DefaultDownFor
	}
	if c.ClipDBFS == 0 {
		c.ClipDBFS = DefaultClipDBFS
	}
	if c.ClipHits <= 0 {
		c.ClipHits = DefaultClipHits
	}
	if c.DiskFloorBytes == 0 {
		c.DiskFloorBytes = DefaultDiskFloorBytes
	}
	if c.DiskFloorPercent == 0 {
		c.DiskFloorPercent = DefaultDiskFloorPercent
	}
	if c.LoudnessFor <= 0 {
		c.LoudnessFor = DefaultLoudnessFor
	}
	if c.SpeedFloor <= 0 {
		c.SpeedFloor = DefaultSpeedFloor
	}
	if c.FallingBehindFor <= 0 {
		c.FallingBehindFor = DefaultFallingBehindFor
	}
	if c.SpeedWindow <= 0 {
		c.SpeedWindow = DefaultSpeedWindow
	}
	return c
}

// DestState is one destination as the watcher judges it.
type DestState struct {
	ID      int64
	Name    string
	Enabled bool
	// Running is the engine's verdict, not the process's: a destination with no
	// process because the engine could not compile its graph is down.
	Running  bool
	Platform string
	Error    string
	// OutTimeMS is the output timestamp from this destination's latest FFmpeg
	// progress block, in milliseconds, and 0 when there is no process or it
	// has not moved any media yet. The watcher derives the delivery rate from
	// how far it advances between snapshots.
	//
	// THERE IS NO SPEED FIELD, ON PURPOSE. FFmpeg's speed= is output time over
	// wall time SINCE THE PROCESS STARTED -- a cumulative average, not a rate --
	// and it is carried in the same progress block that stops arriving when a
	// sink stops reading, because a blocked write blocks FFmpeg's whole loop.
	// Judged on that number, a stalled destination read a frozen ~1.00x for as
	// long as it was stalled; the alert fired only after the heal, when the
	// average had been dragged under the floor, and then held for the best part
	// of an hour while the average crawled back, so caught_up never came and
	// the next stall had nothing left to fire. The field is gone so nothing can
	// judge on it again.
	OutTimeMS int64
	// DropFrames and DupFrames are cumulative counts, carried for context in
	// the event rather than thresholded. They are the pair that tells an
	// operator WHICH way a destination is unwell: drops mean FFmpeg is
	// discarding to keep up, so the output is congested; dups mean it is
	// padding, so the source is starving. Same symptom, opposite fixes.
	DropFrames int64
	DupFrames  int64
}

// FailoverState is the source-selector tier, absent when it is off.
type FailoverState struct {
	Active   string
	Reason   string
	Switches int
}

// LoudnessState is one destination's compliance verdict.
type LoudnessState struct {
	ID     int64
	Name   string
	Failed bool
	Reason string
	LUFS   float64
	Target float64
}

// PeakState is one ingest channel's peak in dBFS.
type PeakState struct {
	Track   int
	Channel int
	PeakDB  float64
}

// DiskState is the recording volume.
type DiskState struct {
	FreeBytes  uint64
	TotalBytes uint64
	// Halted is the recorder's own free-space guard having already stopped
	// writing, which is a fact rather than a threshold and always alerts.
	Halted bool
	Reason string
}

// Snapshot is everything the watcher judges, in one struct so the engine hands
// over a value and the transition logic never reaches back into it.
type Snapshot struct {
	At time.Time
	// IngestConfigured is false when there is nothing to be lost — no ingest is
	// running, so silence is the expected state and not an incident.
	IngestConfigured bool
	IngestLive       bool
	IngestError      string
	Destinations     []DestState
	Failover         *FailoverState
	Loudness         []LoudnessState
	Peaks            []PeakState
	Disk             DiskState
}

// downState tracks how long something has been wrong and whether that has
// already been said out loud.
type downState struct {
	since time.Time
	fired bool
}

// observe folds one bad/good observation and reports which edge was crossed.
func (d *downState) observe(bad bool, now time.Time, after time.Duration) (fire, recover bool) {
	if !bad {
		if d.fired {
			d.since, d.fired = time.Time{}, false
			return false, true
		}
		d.since = time.Time{}
		return false, false
	}
	if d.since.IsZero() {
		d.since = now
	}
	if !d.fired && !now.Before(d.since.Add(after)) {
		d.fired = true
		return true, false
	}
	return false, false
}

// rateWindow measures how fast a destination's output time is advancing
// against the wall clock, over recent history only.
//
// This is the number FFmpeg's speed= looks like it is and is not: that one is
// averaged over the whole run. Measured here, a stall reads as zero while it
// is happening, and the rate returns to 1.0 one window after the stall ends --
// not an hour later.
type rateWindow struct {
	samples []rateSample
}

type rateSample struct {
	at    time.Time
	outMS int64
}

// observe records one output time and returns the rate over the window, with
// ok false while there is not yet enough history to say.
//
// Nothing is measured until the output time is non-zero: a child that has not
// moved any media yet has no rate, and judging one would flag every
// destination for the first seconds of every broadcast. Output time going
// BACKWARDS is a respawn -- a new process counts from zero -- and starts the
// measurement over.
func (r *rateWindow) observe(now time.Time, outMS int64, window time.Duration) (rate float64, ok bool) {
	if n := len(r.samples); outMS <= 0 || (n > 0 && outMS < r.samples[n-1].outMS) {
		r.reset()
		if outMS <= 0 {
			return 0, false
		}
	}
	r.samples = append(r.samples, rateSample{at: now, outMS: outMS})
	// Keep the newest sample at or before the window's start as the baseline,
	// and drop everything older. The span is then at least the window when the
	// history allows it.
	cutoff := now.Add(-window)
	for len(r.samples) >= 2 && !r.samples[1].at.After(cutoff) {
		r.samples = r.samples[1:]
	}
	first := r.samples[0]
	span := now.Sub(first.at)
	if span < window/2 {
		return 0, false
	}
	return float64(outMS-first.outMS) / float64(span.Milliseconds()), true
}

func (r *rateWindow) reset() { r.samples = r.samples[:0] }

// Watcher turns a stream of snapshots into events.
//
// It holds the "how long has this been true" state that a single snapshot
// cannot carry, and nothing else: given the same sequence of snapshots it emits
// the same sequence of events, which is what makes the flap and threshold
// behaviour a table test.
type Watcher struct {
	cfg  WatchConfig
	dest map[int64]*downState
	// slow is keyed the same way as dest but tracked separately, because a
	// destination can be up and falling behind at the same time -- that is the
	// entire point of the condition -- and one downState cannot hold two
	// independent "how long has this been true" clocks.
	slow map[int64]*downState
	// rate is the delivery-rate history slow is judged on, keyed the same way.
	rate map[int64]*rateWindow
	loud map[int64]*downState
	// clipHits counts consecutive observations on the ceiling, per channel.
	clipHits map[string]int
	ingest   downState
	disk     downState

	switches     int
	haveSwitches bool

	// src is the programme this watcher speaks for; see SetSource.
	src SourceRef
}

// SetSource names the programme whose snapshots this watcher judges.
//
// ONE NOTIFIER PER ENGINE, AND EVERY ONE OF THEM USED TO SAY "Ingest lost". On
// a two-programme install that is two alerts with the same title, the same
// key and no field saying which programme had gone, delivered through the
// same rule to the same channel -- and the key being the same is worse than
// unhelpful, because a receiver that deduplicates on it drops the second
// programme's outage as a repeat of the first. The destination events escaped
// this only because a destination id is unique across the install.
//
// Re-stamped every sweep, like hooks.Watcher.SetSource and for its reason: a
// source is renamed long after its engine is built. The zero SourceRef is an
// unscoped watcher -- the keys and titles it always produced -- which is what
// a watcher built by hand in a test gets.
func (w *Watcher) SetSource(src SourceRef) { w.src = src }

// subject scopes a key that names something every programme has (its ingest,
// its failover tier, its audio) to this programme. Keys that already name
// something unique across the install -- a destination id -- do not come
// through here.
func (w *Watcher) subject(key string) string {
	if w.src.ID == 0 {
		return key
	}
	return key + ":" + strconv.FormatInt(w.src.ID, 10)
}

// titled appends the programme's name to a title that would otherwise read
// the same for every programme.
func (w *Watcher) titled(title string) string {
	if w.src.Name == "" {
		return title
	}
	return title + ": " + w.src.Name
}

// NewWatcher creates a watcher with cfg's thresholds.
func NewWatcher(cfg WatchConfig) *Watcher {
	return &Watcher{
		cfg:      cfg.normalized(),
		dest:     map[int64]*downState{},
		slow:     map[int64]*downState{},
		rate:     map[int64]*rateWindow{},
		loud:     map[int64]*downState{},
		clipHits: map[string]int{},
	}
}

// Observe judges one snapshot and returns everything worth saying about it.
func (w *Watcher) Observe(s Snapshot) []Event {
	now := s.At
	if now.IsZero() {
		now = time.Now()
	}
	var out []Event
	out = append(out, w.watchIngest(s, now)...)
	out = append(out, w.watchDestinations(s, now)...)
	out = append(out, w.watchFailover(s, now)...)
	out = append(out, w.watchClipping(s, now)...)
	out = append(out, w.watchDisk(s, now)...)
	out = append(out, w.watchLoudness(s, now)...)
	// Every event but the install's own carries the programme, so a script
	// can route on it and a person can read it without opening the console.
	for i := range out {
		if w.src.ID != 0 && !out[i].Type.InstallScoped() {
			out[i] = out[i].
				WithField("sourceId", strconv.FormatInt(w.src.ID, 10)).
				WithField("sourceName", w.src.Name)
		}
	}
	return out
}

func (w *Watcher) watchIngest(s Snapshot, now time.Time) []Event {
	if !s.IngestConfigured {
		// Nothing to lose. Reset rather than hold the timer, so enabling an
		// ingest does not immediately fire on a stale clock.
		w.ingest = downState{}
		return nil
	}
	fire, recovered := w.ingest.observe(!s.IngestLive, now, w.cfg.DownFor)
	switch {
	case fire:
		ev := Event{
			Type: TypeIngestLost, Severity: SeverityCritical, Key: w.subject("ingest"),
			Title: w.titled("Ingest lost"),
			Text:  "No data has arrived on the ingest for " + short(now.Sub(w.ingest.since)) + ".",
			At:    now,
		}
		return []Event{ev.WithField("error", s.IngestError)}
	case recovered:
		return []Event{{
			Type: TypeIngestRecovered, Severity: SeverityInfo, Key: w.subject("ingest"),
			Title: w.titled("Ingest recovered"), Text: "The source is delivering again.", At: now,
		}}
	}
	return nil
}

func (w *Watcher) watchDestinations(s Snapshot, now time.Time) []Event {
	live := make(map[int64]bool, len(s.Destinations))
	var out []Event
	for _, d := range s.Destinations {
		live[d.ID] = true
		st := w.dest[d.ID]
		if st == nil {
			st = &downState{}
			w.dest[d.ID] = st
		}
		slow := w.slow[d.ID]
		if slow == nil {
			slow = &downState{}
			w.slow[d.ID] = slow
		}
		rate := w.rate[d.ID]
		if rate == nil {
			rate = &rateWindow{}
			w.rate[d.ID] = rate
		}
		if !d.Enabled {
			// A destination the operator turned off is not down. Clearing the
			// state means turning it back on starts the clock fresh instead of
			// firing on the time it spent disabled.
			*st = downState{}
			*slow = downState{}
			rate.reset()
			continue
		}
		fire, recovered := st.observe(!d.Running, now, w.cfg.DownFor)
		key := "destination:" + strconv.FormatInt(d.ID, 10)
		if recovered {
			// destination.recovered CLOSES A falling_behind THAT WAS OPEN ACROSS
			// THE OUTAGE. The incident the operator was told about -- this
			// destination is not delivering -- is over, and the recovery says
			// so; a caught_up a window later would close it a second time. The
			// new run starts with no latch, so if it is slow too, that is a
			// new falling_behind, not the old one held.
			*slow = downState{}
		}

		// The rate is only judged while the destination is up, the source is
		// arriving, and there is a measurement to judge.
		var slowFire, slowRecovered bool
		var speed float64
		switch {
		case !d.Running:
			// A destination that is not running has no speed, so the condition
			// is UNOBSERVABLE rather than recovered. Feeding "not slow" into
			// observe() here would announce that it caught up, at the exact
			// moment it actually gave up -- and it would do so ahead of
			// destination.down, which has its own longer dwell to serve.
			//
			// A latch that already FIRED is HELD, never dropped. Dropping it
			// was silent, and a stall usually ends exactly here: the sink
			// resets the connection, the child exits and is respawned inside
			// one sweep, far short of destination.down's dwell. With the latch
			// gone nothing was ever sent -- no caught_up, no recovered -- for a
			// falling_behind the operator had been paged with (exploratory row
			// 7). Held, it is closed by exactly one message: caught_up once
			// the new run is measured at realtime, or destination.recovered if
			// this turns into an outage long enough to be reported as one.
			if !slow.fired {
				*slow = downState{}
			}
			rate.reset()
		case s.IngestConfigured && !s.IngestLive:
			// NOTHING TO DELIVER IS NOT FALLING BEHIND. With the source gone
			// every destination's output time stops, and reading that as a
			// stall would page once per destination for what ingest.lost
			// already says once. The history is dropped so the gap is not
			// averaged into the first rate after the source returns; a latch
			// that already fired is held rather than cleared, because nothing
			// has been seen to recover.
			rate.reset()
			if !slow.fired {
				*slow = downState{}
			}
		default:
			var known bool
			speed, known = rate.observe(now, d.OutTimeMS, w.cfg.SpeedWindow)
			if known {
				slowFire, slowRecovered = slow.observe(speed < w.cfg.SpeedFloor, now, w.cfg.FallingBehindFor)
			} else if !slow.fired {
				// Not enough history yet: unknown, not slow, and not a
				// recovery either.
				*slow = downState{}
			}
		}
		switch {
		case slowFire:
			out = append(out, Event{
				Type: TypeDestinationFallingBehind, Severity: SeverityWarning,
				Key:   key + ":speed",
				Title: d.Name + " is falling behind",
				Text: d.Name + " has been delivering at " + speedText(speed) +
					" realtime for " + short(now.Sub(slow.since)) +
					". That usually means the platform or the network is not " +
					"taking data fast enough.",
				At: now,
			}.WithField("destination", d.Name).
				WithField("platform", d.Platform).
				WithField("speed", speedText(speed)).
				WithField("dropped frames", strconv.FormatInt(d.DropFrames, 10)).
				WithField("duplicated frames", strconv.FormatInt(d.DupFrames, 10)))
		case slowRecovered:
			out = append(out, Event{
				Type: TypeDestinationCaughtUp, Severity: SeverityInfo,
				Key:   key + ":speed",
				Title: d.Name + " is keeping up again",
				Text:  d.Name + " is back to realtime.", At: now,
			}.WithField("destination", d.Name))
		}
		switch {
		case fire:
			ev := Event{
				Type: TypeDestinationDown, Severity: SeverityCritical, Key: key,
				Title: "Destination down: " + d.Name,
				Text:  d.Name + " has not been delivering for " + short(now.Sub(st.since)) + ".",
				At:    now,
			}
			out = append(out, ev.WithField("destination", d.Name).
				WithField("platform", d.Platform).
				WithField("error", d.Error))
		case recovered:
			out = append(out, Event{
				Type: TypeDestinationRecovered, Severity: SeverityInfo, Key: key,
				Title: "Destination recovered: " + d.Name,
				Text:  d.Name + " is delivering again.", At: now,
			}.WithField("destination", d.Name))
		}
	}
	for id := range w.dest {
		if !live[id] {
			delete(w.dest, id)
			delete(w.slow, id)
			delete(w.rate, id)
		}
	}
	return out
}

// speedText renders FFmpeg's speed ratio the way its own output does, so an
// operator comparing the alert against a process log sees the same number in
// the same shape.
func speedText(speed float64) string {
	return strconv.FormatFloat(speed, 'f', 2, 64) + "x"
}

func (w *Watcher) watchFailover(s Snapshot, now time.Time) []Event {
	if s.Failover == nil {
		w.haveSwitches = false
		return nil
	}
	if !w.haveSwitches {
		// First sight of the tier. Adopt its counter rather than treating every
		// switch since boot as new, which is how a restarted server would greet
		// its operator with a history lesson.
		w.switches, w.haveSwitches = s.Failover.Switches, true
		return nil
	}
	if s.Failover.Switches <= w.switches {
		w.switches = s.Failover.Switches
		return nil
	}
	w.switches = s.Failover.Switches

	sev := SeverityWarning
	if s.Failover.Active == "primary" {
		sev = SeverityInfo
	}
	ev := Event{
		Type: TypeFailoverSwitched, Severity: sev, Key: w.subject("failover"),
		Title: w.titled("Source switched to " + s.Failover.Active),
		Text:  s.Failover.Reason, At: now,
	}
	return []Event{ev.WithField("source", s.Failover.Active).
		WithField("reason", s.Failover.Reason)}
}

func (w *Watcher) watchClipping(s Snapshot, now time.Time) []Event {
	seen := make(map[string]bool, len(s.Peaks))
	var out []Event
	// Sorted so a map iteration cannot reorder the events a snapshot produces.
	peaks := append([]PeakState(nil), s.Peaks...)
	sort.Slice(peaks, func(i, j int) bool {
		if peaks[i].Track != peaks[j].Track {
			return peaks[i].Track < peaks[j].Track
		}
		return peaks[i].Channel < peaks[j].Channel
	})
	for _, p := range peaks {
		id := fmt.Sprintf("t%dc%d", p.Track, p.Channel)
		seen[id] = true
		if p.PeakDB < w.cfg.ClipDBFS {
			delete(w.clipHits, id)
			continue
		}
		w.clipHits[id]++
		if w.clipHits[id] < w.cfg.ClipHits {
			continue
		}
		// Reset rather than latch: the next alert needs another full run of
		// consecutive hits, and the rule's debounce handles the rest.
		delete(w.clipHits, id)
		ev := Event{
			Type: TypeClipping, Severity: SeverityWarning,
			Key:   w.subject("clipping:track" + strconv.Itoa(p.Track)),
			Title: w.titled(fmt.Sprintf("Audio clipping on track %d", p.Track)),
			Text: fmt.Sprintf("Channel %d peaked at %.1f dBFS, at or above the %.1f dBFS ceiling.",
				p.Channel, p.PeakDB, w.cfg.ClipDBFS),
			At: now,
		}
		out = append(out, ev.
			WithField("track", strconv.Itoa(p.Track)).
			WithField("channel", strconv.Itoa(p.Channel)).
			WithField("peakDbfs", fmt.Sprintf("%.1f", p.PeakDB)))
	}
	for id := range w.clipHits {
		if !seen[id] {
			delete(w.clipHits, id)
		}
	}
	return out
}

func (w *Watcher) watchDisk(s Snapshot, now time.Time) []Event {
	d := s.Disk
	if d.TotalBytes == 0 && !d.Halted {
		// Nothing measured. Say nothing rather than alerting on a zero, which
		// is what an unreadable volume looks like.
		return nil
	}
	low := d.Halted || d.FreeBytes < w.cfg.DiskFloorBytes
	if !low && d.TotalBytes > 0 {
		low = float64(d.FreeBytes)/float64(d.TotalBytes)*100 < w.cfg.DiskFloorPercent
	}
	// No dwell time: disk space does not flap, and a recorder that has already
	// halted should not wait twenty seconds to say so.
	fire, recovered := w.disk.observe(low, now, 0)
	switch {
	case fire:
		sev := SeverityWarning
		text := fmt.Sprintf("%s free of %s on the recordings volume.",
			bytesHuman(d.FreeBytes), bytesHuman(d.TotalBytes))
		if d.Halted {
			sev = SeverityCritical
			text = "Recording has been stopped: " + d.Reason
		}
		ev := Event{
			Type: TypeDiskLow, Severity: sev, Key: "disk",
			Title: "Recording disk low", Text: text, At: now,
		}
		return []Event{ev.
			WithField("freeBytes", strconv.FormatUint(d.FreeBytes, 10)).
			WithField("totalBytes", strconv.FormatUint(d.TotalBytes, 10))}
	case recovered:
		return []Event{{
			Type: TypeDiskRecovered, Severity: SeverityInfo, Key: "disk",
			Title: "Recording disk recovered",
			Text: fmt.Sprintf("%s free on the recordings volume.",
				bytesHuman(d.FreeBytes)),
			At: now,
		}}
	}
	return nil
}

func (w *Watcher) watchLoudness(s Snapshot, now time.Time) []Event {
	live := make(map[int64]bool, len(s.Loudness))
	var out []Event
	for _, l := range s.Loudness {
		live[l.ID] = true
		st := w.loud[l.ID]
		if st == nil {
			st = &downState{}
			w.loud[l.ID] = st
		}
		fire, recovered := st.observe(l.Failed, now, w.cfg.LoudnessFor)
		key := "loudness:" + strconv.FormatInt(l.ID, 10)
		switch {
		case fire:
			out = append(out, Event{
				Type: TypeLoudnessOut, Severity: SeverityWarning, Key: key,
				Title: "Loudness out of compliance: " + l.Name,
				Text:  l.Reason, At: now,
			}.WithField("destination", l.Name).
				WithField("integratedLufs", fmt.Sprintf("%.1f", l.LUFS)).
				WithField("targetLufs", fmt.Sprintf("%.1f", l.Target)))
		case recovered:
			out = append(out, Event{
				Type: TypeLoudnessRecovered, Severity: SeverityInfo, Key: key,
				Title: "Loudness back in compliance: " + l.Name,
				Text:  fmt.Sprintf("%s is measuring %.1f LUFS against a %.1f LUFS target.", l.Name, l.LUFS, l.Target),
				At:    now,
			}.WithField("destination", l.Name))
		}
	}
	for id := range w.loud {
		if !live[id] {
			delete(w.loud, id)
		}
	}
	return out
}

// short renders a duration the way an operator reads it, not the way Go
// prints it: "1m20s", never "1m20.000481s".
func short(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return strconv.Itoa(int(d.Seconds())) + "s"
	}
	m := int(d.Minutes())
	sec := int(d.Seconds()) - m*60
	if sec == 0 {
		return strconv.Itoa(m) + "m"
	}
	return fmt.Sprintf("%dm%ds", m, sec)
}

func bytesHuman(b uint64) string {
	const unit = 1024
	if b < unit {
		return strconv.FormatUint(b, 10) + " B"
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTP"[exp])
}
