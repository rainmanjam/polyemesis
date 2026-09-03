package engine

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// A DESTINATION THAT PROBED BEFORE ITS AUDIO EXISTED CAN ONLY BE RESTARTED. #674
//
// FFmpeg characterises an input's streams ONCE, bounded by analyzeduration, and
// never re-probes. A destination started while the relay carries video and no
// audio yet resolves no audio stream -- permanently, for the life of that
// process, however much audio arrives afterwards. It then runs on reading zero
// audio packets, its filter graph never initialised, publishing nothing, and it
// DOES NOT EXIT. AutoRestart fires on exit, so nothing ever notices.
//
// Measured on the acceptance rig: step 4c creates the destination and starts
// the publisher about twenty seconds later; the relay then carries all three
// AAC tracks for the publisher's whole life (13,393,120 bytes, 2,318 packets
// per PID) and the destination reads 0 audio packets from first to last.
//
// WHY THE TRIGGER IS A LOG SIGNATURE AND NOT "PUBLISHED NOTHING".
//
// Two earlier attempts were reverted for being unable to tell this fault from
// healthy states:
//
//   - refusing to start while the hub had received no bytes broke FAILOVER. A
//     destination added during a failover has to start and carry the slate;
//     internal/engine's own test says so in as many words.
//   - restarting anything "receiving but publishing nothing" also restarts a
//     publisher that does not match the profile -- a legitimate steady state --
//     and a destination still inside its own startup probe. It broke
//     acceptance-failover, which pins zero restarts across a mismatched cut,
//     because restarting there splits the recording across files.
//
// These messages are neither of those. FFmpeg emits them only when the DEMUXER
// could not determine an audio stream's parameters, which is exactly the fault
// and is false for a mismatched publisher (whose streams are characterised
// fine) and for a slow starter (which has not finished probing and so has not
// concluded anything yet).
var reprobeSignatures = []string{
	// The demuxer gave up on an audio stream inside analyzeduration.
	"could not find codec parameters for stream",
	// The filter graph was handed an input whose layout was never resolved.
	"neither number of channels nor channel layout specified",
}

// maxReprobeRestarts bounds the self-healing. A destination that cannot
// characterise its audio after this many fresh probes is not suffering from
// start ordering, and restarting for ever would hide a permanent fault behind a
// process that always looks freshly started.
const maxReprobeRestarts = 3

// reprobeCooldown is how long one re-probe attempt is given before another may
// be counted. It must clear the restart plus the new child's own probe of the
// relay -- ffmpeg.RelayProbeWindow is 15s -- so a fresh process that fails the
// same way is a SECOND attempt rather than the tail of the first.
const reprobeCooldown = 2 * ffmpeg.RelayProbeWindow

// reprobeOnUncharacterisedAudio returns an OnLog handler that restarts this
// destination when FFmpeg says it could not characterise the input's audio.
//
// A restart is the ONLY repair available: the parameters are not late, they are
// absent for the life of the process, and a fresh child probes a relay that is
// by then carrying audio. Widening analyzeduration was tried and does not work
// -- the gap is ~20s of no audio at all, not sparsity -- and it regressed a
// passing acceptance step by tripling every destination's startup wait.
// procOf resolves the child LAZILY: this handler is built inside the Spec that
// constructs that very child, so the process does not exist yet at wiring time.
func (e *Engine) reprobeOnUncharacterisedAudio(
	name string, kind db.DestKind, procOf func() *supervisor.Process, next func(supervisor.LogLine),
) func(supervisor.LogLine) {
	var (
		mu       sync.Mutex
		restarts int
		lastFire time.Time
	)
	return func(l supervisor.LogLine) {
		if next != nil {
			next(l)
		}
		if !matchesReprobeSignature(l.Text) {
			return
		}
		// ONE RESTART PER PROBE, NOT ONE PER MESSAGE. FFmpeg emits this line
		// once for EVERY audio stream it could not characterise -- three at the
		// same millisecond on a three-track ingest. Counting messages spent the
		// whole budget on a single failed probe: measured on the acceptance rig
		// at 06:17:54.834, attempts 1, 2 and 3 all in the same instant, so the
		// destination never got a second real chance. The cooldown has to clear
		// a restart plus the fresh child's own analyzeduration, or the new
		// process's identical complaint burns the next attempt too.
		now := time.Now()
		mu.Lock()
		if !lastFire.IsZero() && now.Sub(lastFire) < reprobeCooldown {
			mu.Unlock()
			return
		}
		if restarts >= maxReprobeRestarts {
			mu.Unlock()
			return
		}
		restarts++
		lastFire = now
		n := restarts
		mu.Unlock()

		proc := procOf()
		if proc == nil || proc.Retired() {
			return
		}
		e.log.Warn("destination could not characterise its input audio; restarting to re-probe",
			"dest", name, "kind", kind, "attempt", n, "line", l.Text)
		// In a goroutine: this runs on the child's log pump, and Restart stops
		// that child. Restarting from inside its own log callback deadlocks.
		go proc.Restart(context.Background())
	}
}

// matchesReprobeSignature reports whether a log line is FFmpeg saying it could
// not work out an audio stream's parameters.
func matchesReprobeSignature(text string) bool {
	low := strings.ToLower(text)
	// Audio only. The same "could not find codec parameters" is emitted for
	// video on a late join, where the next keyframe resolves it without help
	// and a restart would be pure churn.
	if strings.Contains(low, "could not find codec parameters for stream") &&
		!strings.Contains(low, "audio") {
		return false
	}
	for _, sig := range reprobeSignatures {
		if strings.Contains(low, sig) {
			return true
		}
	}
	return false
}

// reprobeTarget is one destination the re-probe would restart.
type reprobeTarget struct {
	name string
	proc *supervisor.Process
	w    *destWatch
}

// destinationsNeedingReprobe selects the destinations that have never moved any
// media, extracted so the SELECTION can be tested without a live child.
//
// The discriminator is the whole design: a destination that has published is
// working and must be left alone -- acceptance-failover pins zero restarts
// across a switch, and a restart splits a recording across files. One that has
// never published since it started is the only kind that can be holding a
// characterisation taken before there was any audio to characterise.
//
// Caller holds e.mu.
func destinationsNeedingReprobe(dests map[int64]*destination) []reprobeTarget {
	var stuck []reprobeTarget
	for _, d := range dests {
		if d == nil || d.proc == nil || d.watch == nil || d.row == nil {
			continue
		}
		if d.watch.publishedSinceRearm() {
			continue // it is publishing; leave it alone
		}
		stuck = append(stuck, reprobeTarget{name: d.row.Name, proc: d.proc, w: d.watch})
	}
	return stuck
}

// reprobeDestinationsThatNeverPublished restarts every destination that has not
// yet moved any media, at the moment the ingest layout is first measured. #674
//
// WHY HERE, AND NOT ON THE CHILD'S OWN COMPLAINT.
//
// FFmpeg does say it could not characterise the audio -- see
// reprobeOnUncharacterisedAudio -- but it says so only once it GIVES UP, and
// measured on the acceptance rig that is 85 seconds after the destination
// started: 06:24:43 start, 06:26:08 complaint, against a publisher that lived
// 40 seconds. By then the stream it needed is gone, so the restart re-probes
// dead air and fails identically. A repair that arrives after the window has
// closed is not a repair.
//
// This fires instead on the engine's OWN signal, at the instant it learns the
// ingest carries audio, which is exactly when a fresh probe would succeed.
//
// THE DISCRIMINATOR IS "HAS IT EVER PUBLISHED".
//
// Restarting every destination whenever a layout is measured would break
// failover, which pins ZERO restarts across a switch -- acceptance-failover
// asserts "the destination rode both switches without restarting". A
// destination that has published is working and is riding a change; one that
// has never published since it started is the only kind that can be holding a
// characterisation taken before there was any audio to characterise. That is
// false for the mismatched publisher too: it publishes, it just publishes the
// wrong thing.
func (e *Engine) reprobeDestinationsThatNeverPublished(reason string) {
	e.mu.Lock()
	stuck := destinationsNeedingReprobe(e.dests)
	e.mu.Unlock()

	for _, t := range stuck {
		if t.proc.Retired() {
			continue
		}
		e.log.Warn("destination has published nothing and the ingest layout just changed; "+
			"restarting so it re-probes an input that now carries audio",
			"dest", t.name, "reason", reason)
		// Not under e.mu: Restart stops a child and waits for it.
		go t.proc.Restart(context.Background())
	}
}

// ingestProgressLogger reports how much the ingest has actually written, at a
// rate an operator can read. #674.
//
// The relay hub's own counter shows it receiving ~6 packets/second for 81
// seconds and then ~135, with subscribers attached the whole time -- so the hub
// is starved of input, and the ingest is the only thing that feeds it. Nothing
// recorded what the ingest was producing, so "it is running" and "it is
// producing" were indistinguishable for this entire investigation.
//
// Every 20th progress block: FFmpeg emits roughly two a second, so about one
// line every ten seconds. Silent when the ingest is not running at all.
func (e *Engine) ingestProgressLogger() func(ffmpeg.Progress) {
	var (
		mu   sync.Mutex
		n    int
		last int64
	)
	return func(pr ffmpeg.Progress) {
		mu.Lock()
		n++
		show := n%20 == 1
		delta := pr.TotalSize - last
		if show {
			last = pr.TotalSize
		}
		mu.Unlock()
		if show {
			e.log.Info("ingest output", "totalSize", pr.TotalSize,
				"bytesSinceLast", delta, "outTimeMs", pr.OutTimeMS, "speed", pr.Speed)
		}
	}
}
