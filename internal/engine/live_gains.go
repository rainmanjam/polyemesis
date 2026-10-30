package engine

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sync"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// Live per-track gain and mute.
//
// A destination's graph carries one named volume stage per mixed track. A gain
// or mute edit changes only the levels of those stages, so it can be sent to
// the running FFmpeg on its command channel rather than restarting it. Three
// rules keep that safe:
//
//   - Persist first. The stored profile changes before anything is sent, so a
//     restart at any later point rebuilds the same levels.
//   - The graph a process respawns from is the one live application leaves
//     behind. A crash or reconnect after a live mute comes back muted.
//   - Only a change that is nothing but levels is live. Anything else (a track
//     added or removed, a normaliser flipped, a matrix cell moved) changes the
//     restart signature and restarts, as it always did.

// graphHolder is the compiled graph a destination process is spawned from.
// Respawns read it at spawn time, so the most recent live application is what
// every later spawn of that process uses.
type graphHolder struct {
	mu  sync.Mutex
	res routing.Result
}

func newGraphHolder(r routing.Result) *graphHolder {
	return &graphHolder{res: r}
}

func (g *graphHolder) get() routing.Result {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.res
}

func (g *graphHolder) set(r routing.Result) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.res = r
}

// liveSetter is the one capability live application needs of a process.
// *supervisor.Process satisfies it; tests supply a fake.
type liveSetter interface {
	SetVolume(instance string, level float64) error
}

// liveGainsSupported reports whether the live path may be attempted at all. On
// Windows it is unverified, so every gain change restarts instead.
func liveGainsSupported() bool {
	return runtime.GOOS != "windows"
}

// levelsDiffer reports whether two graphs differ in the level of any named
// volume stage. Callers have already established that the graphs are equal
// once levels are masked; this only answers "did a level move".
func levelsDiffer(running, want string) bool {
	a := ffmpeg.LiveVolumeLevels(running)
	b := ffmpeg.LiveVolumeLevels(want)
	if len(a) != len(b) {
		return true
	}
	for name, v := range b {
		av, ok := a[name]
		if !ok || av != v {
			return true
		}
	}
	return false
}

// applyLiveLevels sends every level that moved from before to after to the
// running process, in a stable order. It stops at the first refusal and returns
// it, and the caller then restarts the process.
func applyLiveLevels(proc liveSetter, before, after string) error {
	a := ffmpeg.LiveVolumeLevels(before)
	b := ffmpeg.LiveVolumeLevels(after)
	names := make([]string, 0, len(b))
	for name := range b {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if av, ok := a[name]; ok && av == b[name] {
			continue
		}
		if err := proc.SetVolume("volume@"+name, b[name]); err != nil {
			return fmt.Errorf("volume@%s: %w", name, err)
		}
	}
	return nil
}

// liveNote is what the last reconcile did about a destination's levels. It is
// what the API reports, so an operator is told whether a change was applied
// live or by a restart, and why a restart was needed.
type liveNote struct {
	applied string // "live" or "restart"
	reason  string // set when applied is "restart" by fallback
}

const (
	liveApplied = "live"
	liveRestart = "restart"
)

// copyNoEffect is said of a copy destination's level. Its audio passes through
// unchanged, so the level is stored and reaches nothing.
const copyNoEffect = "copy destinations pass audio through unchanged, so the level is stored and has no effect on this output"

// copyPathOf reports whether a destination's argv ignores its graph. Such an
// output has no volume stage to move.
func copyPathOf(row *db.Destination) bool {
	return row.Audio.Copy && ffmpeg.DestKind(row.Kind) != ffmpeg.DestAudio
}

// liveCandidate is a kept destination whose only change is levels.
type liveCandidate struct {
	id int64
	d  *destination
	p  destPlan
}

// liveApplyTo sends a level-only change to a running process. The holder is
// updated FIRST, so any respawn from this point on is built with the new levels;
// the commands then bring the running process to the same state.
func liveApplyTo(proc liveSetter, holder *graphHolder, want routing.Result) error {
	if !liveGainsSupported() {
		return errors.New("live gain is not verified on this platform")
	}
	if proc == nil || holder == nil {
		return errors.New("no running process to change")
	}
	before := holder.get()
	holder.set(want)
	return applyLiveLevels(proc, before.FilterComplex, want.FilterComplex)
}

// fallbackReason is the operator-facing reason a live change became a restart.
func fallbackReason(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// liveBackup applies a level-only change to a backup process. It reports true
// when nothing more needs doing; false means the caller must restart the backup.
func (e *Engine) liveBackup(d *destination, compiled routing.Result) bool {
	if d.backupGraph == nil || d.backup == nil || d.row == nil {
		return false
	}
	if !levelsDiffer(d.backupGraph.get().FilterComplex, compiled.FilterComplex) {
		return true
	}
	if copyPathOf(d.row) {
		e.setLiveNote(d.row.ID, liveNote{applied: liveApplied, reason: copyNoEffect})
		return true
	}
	if err := liveApplyTo(d.backup, d.backupGraph, compiled); err != nil {
		e.setLiveNote(d.row.ID, liveNote{applied: liveRestart, reason: fallbackReason(err)})
		return false
	}
	e.setLiveNote(d.row.ID, liveNote{applied: liveApplied})
	return true
}

// setLiveNote records a note. Takes e.mu; use setLiveNoteLocked under it.
func (e *Engine) setLiveNote(id int64, n liveNote) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.setLiveNoteLocked(id, n)
}

func (e *Engine) setLiveNoteLocked(id int64, n liveNote) {
	if e.liveNotes == nil {
		e.liveNotes = map[int64]liveNote{}
	}
	e.liveNotes[id] = n
}

// DestinationLive returns what the most recent level change did to a
// destination: "live", or "restart" with the reason. ok is false when no level
// change has been recorded for it, which means its last change restarted it
// for a structural reason.
func (e *Engine) DestinationLive(id int64) (applied, reason string, ok bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	n, ok := e.liveNotes[id]
	return n.applied, n.reason, ok
}
