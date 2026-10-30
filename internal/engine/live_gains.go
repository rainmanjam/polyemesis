package engine

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sync"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
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

// structuralRestart is said of a level change that arrived with a change a
// command cannot carry: a track added or removed, a normaliser that flipped, a
// matrix cell, an encoder or an upstream move.
const structuralRestart = "the change alters more than a level, so the destination was restarted with it"

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

// A LIVE LEVEL DOES NOT SURVIVE A FILTER-GRAPH REINIT, so the argv must carry it.
//
// A volume command changes only the graph that is running. When the decoded
// input format changes -- a failover from a 48 kHz ingest to a 44.1 kHz slate
// or playlist, or the ingest's next session arriving at another rate -- FFmpeg
// throws that graph away and builds a new one from its ARGV, and the level the
// process was spawned with comes back. Measured on FFmpeg 7.1.5 and 9.0.2: a
// track muted live (-91 dB) is back at full level (-18 dB) the moment the
// 44.1 kHz frames reach the graph (TestALiveMuteSurvivesAFormatChange).
//
// Re-sending the level at the switch does not repair it. The command lands
// when the feed is swapped, and the reinit happens later, when the new feed's
// first frames have crossed the relay and the destination's decoder, so the
// reinit wipes the re-sent level as well. Nothing at the default -loglevel
// reports the reinit (FFmpeg logs "Reconfiguring filter graph" at verbose), so
// there is no later moment to re-send at either. And -reinit_filter 0 is no
// way out: a real rate change then fails with "Changing audio frame properties
// on the fly is not supported" and the output stops.
//
// So the repair is to make the argv true. At every event that can change the
// decoded format, a running process whose argv levels differ from the levels
// it would now be spawned with (its holder) is restarted, and the respawn reads
// the holder. That costs one reconnect per destination per live change, at most:
// once the argv matches, every later reinit rebuilds the right level. A
// destination nobody has changed live is never touched.

// argvGraph returns the -filter_complex an argv was spawned with, or "" when it
// has none (a copy destination, which has no volume stage to revert).
func argvGraph(argv []string) string {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "-filter_complex" {
			return argv[i+1]
		}
	}
	return ""
}

// spawnedArgs is what the stale check needs of a process: the argv its running
// child was actually spawned with. *supervisor.Process satisfies it.
type spawnedArgs interface {
	Args() []string
}

// levelsStale reports whether a running child's argv carries a level other than
// the one its holder would spawn it with, which is exactly the level a reinit
// would bring back.
func levelsStale(proc spawnedArgs, holder *graphHolder) bool {
	if proc == nil || holder == nil {
		return false
	}
	spawned := argvGraph(proc.Args())
	if spawned == "" {
		return false
	}
	return levelsDiffer(spawned, holder.get().FilterComplex)
}

// staleLevelProc is one process respawnStaleLevels will cycle.
type staleLevelProc struct {
	name string
	proc *supervisor.Process
}

// staleLevelProcs lists the running destination processes, primary and
// backup, whose argv levels differ from their holders'. The destinations are
// snapshotted under e.mu and judged outside it, since the judgement takes the
// process's and the holder's own locks.
func (e *Engine) staleLevelProcs() []staleLevelProc {
	type cand struct {
		name   string
		proc   *supervisor.Process
		holder *graphHolder
	}
	var cands []cand
	e.mu.RLock()
	for _, d := range e.dests {
		if d == nil || d.row == nil || copyPathOf(d.row) {
			continue
		}
		if d.proc != nil && d.graph != nil {
			cands = append(cands, cand{d.row.Name, d.proc, d.graph})
		}
		if d.backup != nil && d.backupGraph != nil {
			cands = append(cands, cand{d.row.Name + " (backup)", d.backup, d.backupGraph})
		}
	}
	e.mu.RUnlock()

	var out []staleLevelProc
	for _, c := range cands {
		if c.proc.Retired() || !levelsStale(c.proc, c.holder) {
			continue
		}
		out = append(out, staleLevelProc{name: c.name, proc: c.proc})
	}
	return out
}

// respawnStaleLevels restarts every destination process whose live levels a
// filter-graph reinit would revert (see above). why is the upstream event, for
// the log line. It never blocks its caller on a restart: the selector sweep and
// a process's state callback both call it, and a destination stop can take
// seconds. A process already being cycled for this reason is not cycled again.
func (e *Engine) respawnStaleLevels(why string) {
	for _, s := range e.staleLevelProcs() {
		e.mu.Lock()
		if e.levelRespawning[s.proc] {
			e.mu.Unlock()
			continue
		}
		if e.levelRespawning == nil {
			e.levelRespawning = map[*supervisor.Process]bool{}
		}
		e.levelRespawning[s.proc] = true
		e.mu.Unlock()

		e.log.Warn("restarting destination so a live level survives the upstream change",
			"dest", s.name, "event", why)
		go func(p *supervisor.Process) {
			defer func() {
				e.mu.Lock()
				delete(e.levelRespawning, p)
				e.mu.Unlock()
			}()
			ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
			defer cancel()
			p.Restart(ctx)
			e.publishStatus()
		}(s.proc)
	}
}

// onUpstreamState is the state callback of a process that publishes what
// destinations decode -- the primary and backup ingest listeners. Each spawn is
// a new streamer session, which may arrive at a new sample rate or channel
// layout without the selector's feed changing at all, so it is a format event.
func (e *Engine) onUpstreamState(s supervisor.Status) {
	if s.State == supervisor.StateRunning {
		e.respawnStaleLevels("the " + s.Name + " process started a new session")
	}
	e.onState(s)
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

// ForgetDestinationLive drops the recorded outcome for a destination, so the
// next reconcile's answer is the only one an API caller can see.
func (e *Engine) ForgetDestinationLive(id int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.liveNotes, id)
}

// levelChangeText is DestinationLive as the one line the status carries.
func (e *Engine) levelChangeText(id int64) string {
	applied, reason, ok := e.DestinationLive(id)
	switch {
	case !ok:
		return ""
	case applied == liveRestart:
		return "applied by restart: " + reason
	case reason != "":
		return "applied live: " + reason
	default:
		return "applied live"
	}
}

// DestinationRunning reports whether a destination currently has a process the
// live path could reach. The API uses it to tell "not running, stored for the
// next start" apart from a restart.
func (e *Engine) DestinationRunning(id int64) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	d := e.dests[id]
	return d != nil && d.proc != nil
}
