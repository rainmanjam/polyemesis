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

// Watcher turns a stream of snapshots into events.
//
// It holds the "how long has this been true" state that a single snapshot
// cannot carry, and nothing else: given the same sequence of snapshots it emits
// the same sequence of events, which is what makes the flap and threshold
// behaviour a table test.
type Watcher struct {
	cfg  WatchConfig
	dest map[int64]*downState
	loud map[int64]*downState
	// clipHits counts consecutive observations on the ceiling, per channel.
	clipHits map[string]int
	ingest   downState
	disk     downState

	switches     int
	haveSwitches bool
}

// NewWatcher creates a watcher with cfg's thresholds.
func NewWatcher(cfg WatchConfig) *Watcher {
	return &Watcher{
		cfg:      cfg.normalized(),
		dest:     map[int64]*downState{},
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
			Type: TypeIngestLost, Severity: SeverityCritical, Key: "ingest",
			Title: "Ingest lost",
			Text:  "No data has arrived on the ingest for " + short(now.Sub(w.ingest.since)) + ".",
			At:    now,
		}
		return []Event{ev.WithField("error", s.IngestError)}
	case recovered:
		return []Event{{
			Type: TypeIngestRecovered, Severity: SeverityInfo, Key: "ingest",
			Title: "Ingest recovered", Text: "The source is delivering again.", At: now,
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
		if !d.Enabled {
			// A destination the operator turned off is not down. Clearing the
			// state means turning it back on starts the clock fresh instead of
			// firing on the time it spent disabled.
			*st = downState{}
			continue
		}
		fire, recovered := st.observe(!d.Running, now, w.cfg.DownFor)
		key := "destination:" + strconv.FormatInt(d.ID, 10)
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
		}
	}
	return out
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
		Type: TypeFailoverSwitched, Severity: sev, Key: "failover",
		Title: "Source switched to " + s.Failover.Active,
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
			Key:   "clipping:track" + strconv.Itoa(p.Track),
			Title: fmt.Sprintf("Audio clipping on track %d", p.Track),
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
