// Destinations and their backups: planning what each enabled destination should
// look like, deriving the argv and the restart hash it is compared against, and
// starting, retuning and tearing the processes down. Everything below was moved
// out of engine.go verbatim.
//
// The blank line before the package clause is load-bearing: engine.go owns the
// package doc, and closing this gap would give the package a second one.

package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/hooks"
	"github.com/rainmanjam/polyemesis/internal/relay"
	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// destPlan is what one destination should look like after this reconcile.
type destPlan struct {
	row      *db.Destination
	compiled routing.Result
	spec     string
	// upstream is the signature of what this destination reads. Carried on the
	// plan because the BACKUP's hash needs it too, and recomputing it in a
	// second place is how two hashes drift apart.
	upstream string
	// err is a reason not to run at all — a routing graph that will not
	// compile, or an upstream rendition that is not there. Either way the
	// destination is shown as broken rather than started against nothing.
	err string
	// oneMix is this destination compiled WITHOUT its second audio mix, and it
	// is non-nil only for the destinations whose second track is conditional on
	// a Twitch negotiation -- see vodNeedsNegotiation.
	//
	// IT CANNOT BE DERIVED FROM compiled BY CLEARING SecondOutLabel. The label
	// names an output of the filter graph in compiled.FilterComplex, and FFmpeg
	// refuses a -filter_complex whose output nothing maps; dropping the map
	// alone would turn a fallback into a destination that will not start.
	//
	// Carried on the plan rather than recompiled in startDest so that both
	// graphs come from ONE read of the source layout. startDest runs after the
	// plan, and a source that was re-probed in between would give the fallback
	// a different track selection from the one the plan hashed.
	oneMix *routing.Result
	// vodDropped is the sentence for an operator whose second audio mix is not
	// being sent, decided at plan time. Empty for every destination that has no
	// second mix and for every one whose second mix is going out.
	vodDropped string
}

// planDestinations works out the desired state of every enabled destination,
// including which upstream it reads and whether it can run at all.
func (e *Engine) planDestinations(rows []*db.Destination, wantRends map[int64]string, src routing.Source, silenceSig string, provisional bool) map[int64]destPlan {
	plans := map[int64]destPlan{}
	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		p := destPlan{row: row}

		// The upstream's own signature rides in the destination's, so editing a
		// rendition restarts exactly the destinations downstream of it and
		// nothing else. A passthrough destination's upstream is the silence
		// tier when there is one, which is why silenceSig is the seed rather
		// than the empty string: switching synthesis on or off has to restart
		// every destination, not just the ones on a rendition.
		upstream := silenceSig
		if row.RenditionID != nil {
			sig, ok := wantRends[*row.RenditionID]
			if !ok {
				// The rendition was deleted between the two queries. Deleting
				// one drops its destinations back to passthrough, so the
				// reconcile that follows the delete sees a nil id and this
				// destination comes straight back.
				p.err = fmt.Sprintf("rendition %d is no longer available", *row.RenditionID)
			}
			upstream = sig
		}

		// provisional means the ingest could not be probed at all, so src is the
		// placeholder and its channel counts are a guess. CompileProvisional
		// replaces the guessed pan matrices with a runtime downmix, which is
		// what makes running on a guess defensible: a wrong layout then folds
		// audibly rather than discarding dialogue in silence.
		compile := routing.Compile
		if provisional {
			compile = routing.CompileProvisional
		}
		compiled, cerr := compile(row.Profile, src)
		// The second (VOD) audio mix, when this destination asked for one.
		//
		// NOT on the provisional path. A provisional compile is already running
		// on a guessed layout and saying so; adding a second guessed mix on top
		// doubles what is approximate while the operator is being told the first
		// one is unreliable. The VOD track comes back by itself on the next
		// reconcile after a probe succeeds, which is the same moment the live
		// mix stops being provisional.
		//
		// AND NOT ON A TWITCH DESTINATION THAT DID NOT OPT IN. The ordinary
		// Twitch RTMP ingest carries ONE audio track -- db.AudioEncoding's
		// copyProblems says so in as many words -- and Enhanced Broadcasting is
		// the only published path that takes a second. Before this gate the
		// engine compiled the pair on row.VODProfile != nil alone, so such a
		// destination pushed two tracks at a one-track ingest with nothing
		// anywhere saying so; db.Destination.VODProfile's own comment promised
		// "the engine reports it" and nothing did.
		//
		// The refusal is here, at plan time, only for the case a network call
		// could not change: the operator left the toggle off. Where they turned
		// it ON the answer depends on what Twitch says, so the pair is compiled
		// and startDest decides -- see oneMix below.
		switch {
		case row.VODProfile == nil || cerr != nil:
			// Nothing to pair, or nothing worth pairing.
		case provisional:
			// SAY SO. This arm used to share the one above and set no note, so a
			// destination on an unprobed ingest dropped its second mix on every
			// platform in silence -- the one drop with no explanation attached,
			// which is the case an operator is least able to work out for
			// themselves because nothing about it is their configuration.
			p.vodDropped = noteVODProvisional
		case twitchOneAudioTrack(row) && !wantsMultitrack(row):
			p.vodDropped = noteVODWithoutMultitrack
		default:
			paired, perr := routing.CompilePair(row.Profile, row.VODProfile, src)
			if perr != nil {
				// CompilePair fails only where Compile just failed, so reaching
				// here means the live mix is broken too and cerr would have been
				// set. Belt and braces: report it rather than silently publish
				// one track.
				cerr = perr
			} else {
				// The single-mix graph is kept beside the paired one for the
				// destinations whose second track is conditional on a
				// negotiation that has not happened yet. Compiled HERE, from
				// the same src, rather than recompiled inside startDest: the
				// two must be the same destination described two ways, and a
				// second compile against a source read a moment later is how
				// they stop being.
				if vodNeedsNegotiation(row) {
					one := compiled
					p.oneMix = &one
				}
				compiled = paired.Result
			}
		}
		if cerr != nil {
			p.err = cerr.Error()
		} else {
			p.compiled = compiled
			p.spec = destSpec(row, compiled, upstream)
			p.upstream = upstream
		}
		plans[row.ID] = p
	}
	return plans
}

// stopDestinations tears down every destination that is gone, newly disabled,
// newly broken, or running with arguments that no longer match. Everything else
// is left strictly alone — that is the guarantee that renaming a destination,
// or editing a different one, never interrupts a live output.
func (e *Engine) stopDestinations(plans map[int64]destPlan) {
	e.mu.Lock()
	var toStop []*destination
	for id, d := range e.dests {
		p, wanted := plans[id]
		keep := wanted && d.proc != nil && p.err == "" && d.spec == p.spec
		if !keep {
			toStop = append(toStop, d)
			delete(e.dests, id)
			// RECORDED BEFORE THE LOCK IS RELEASED, and that ordering is the
			// whole point. The entry leaves e.dests here, teardownDest runs
			// below without the lock, and the child is not asked to stop until
			// well inside it -- so between this unlock and that stop the process
			// is alive and belongs to nothing. Status() read that gap and
			// published a nil Process for a destination that was delivering
			// (#462). Handing it to retiring keeps it answerable throughout.
			if d.proc != nil {
				if e.retiring == nil {
					e.retiring = map[int64]*destination{}
				}
				e.retiring[id] = d
			}
		}
	}
	e.mu.Unlock()

	for _, d := range toStop {
		e.teardownDest(d)
		// AFTER teardown, not before: teardownDest returns once the child has
		// been asked to stop and its port and subscription released, which is
		// the first moment nothing is relying on it. A destination that Stop
		// gave up on -- SIGKILL issued, death unobserved -- is still cleared
		// here, because the engine has done everything it can and holding the
		// record forever would report a phantom.
		if d.row != nil {
			e.mu.Lock()
			delete(e.retiring, d.row.ID)
			e.mu.Unlock()
		}
	}
}

// startDestinations starts everything the plan wants that is not already
// running, once its rendition is up.
func (e *Engine) startDestinations(plans map[int64]destPlan) {
	ids := make([]int64, 0, len(plans))
	for id := range plans {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	// Spacing for destinations started in THIS sweep. Counted per actually-
	// started process, not per id, so a reconcile that leaves seven running and
	// starts one does not make that one wait seven slots for nothing.
	stagger := time.Duration(e.Settings().Destinations.StaggerMS) * time.Millisecond
	started := 0

	for _, id := range ids {
		p := plans[id]

		e.mu.Lock()
		if cur := e.dests[id]; cur != nil {
			// Survived the stop phase, so it is running with the right
			// arguments; refresh the row for cosmetic fields like the name.
			//
			// Replaced wholesale rather than mutated in place: Status hands out
			// these pointers and then reads their fields after dropping the
			// lock, which is only safe while a published destination never
			// changes again.
			next := *cur
			next.row = p.row
			if p.oneMix == nil {
				// The plan-time verdict on the second audio mix, refreshed
				// without a restart. Adding a VOD mix to a Twitch destination
				// that has not opted into Enhanced Broadcasting does not change
				// the command line -- that is the whole of the gate -- so
				// nothing else here would ever notice, and the operator would
				// be left with a setting that has silently done nothing.
				//
				// Only the plan-time verdict. Where oneMix is non-nil the
				// answer came from a negotiation that this pass has not run,
				// and the start that did run it is the authority.
				next.vodDropped = p.vodDropped
			}
			e.dests[id] = &next
			e.mu.Unlock()
			// AFTER the unlock. SetPolicy itself is a memory write, but the
			// revival path calls Restart, which blocks for up to stopTimeout.
			// Holding e.mu across that would stall every Status() the dashboard
			// asks for and every other tier's reconcile behind it.
			e.applyDestPolicy(&next, p.row)
			// The backup is reconciled here as well as on a fresh start,
			// because its toggle is absent from destSpec -- so a destination
			// that survived the stop phase is exactly the case nothing else
			// would notice the setting changed.
			e.reconcileBackup(id, &next, backupCompiled(p), p.upstream)
			continue
		}
		e.mu.Unlock()

		if p.err != "" {
			e.mu.Lock()
			e.dests[id] = &destination{row: p.row, compiled: p.compiled, err: p.err}
			e.mu.Unlock()
			e.log.Warn("destination cannot run", "dest", p.row.Name, "err", p.err)
			continue
		}

		hub, herr := e.upstreamHub(p.row)
		if herr != nil {
			e.mu.Lock()
			e.dests[id] = &destination{row: p.row, compiled: p.compiled, err: herr.Error()}
			e.mu.Unlock()
			e.log.Warn("destination has no upstream", "dest", p.row.Name, "err", herr)
			continue
		}

		if err := e.startDest(p, hub, stagger*time.Duration(started)); err != nil {
			e.log.Error("start destination", "dest", p.row.Name, "err", err)
			e.mu.Lock()
			e.dests[id] = &destination{row: p.row, compiled: p.compiled, err: err.Error()}
			e.mu.Unlock()
			continue
		}
		started++

		// The backup rides alongside, after the primary is up. Reconciled
		// through the same function the already-running branch uses, so there
		// is one place that decides whether a redundant feed should exist.
		e.mu.Lock()
		d := e.dests[id]
		e.mu.Unlock()
		e.reconcileBackup(id, d, backupCompiled(p), p.upstream)
	}
}

// upstreamHub is the relay a destination reads: the ingest's when it is on
// passthrough, its rendition's own otherwise.
func (e *Engine) upstreamHub(row *db.Destination) (*relay.Hub, error) {
	if row.RenditionID == nil {
		if h := e.selectorHub(); h != nil {
			// The silence tier is no longer this destination's problem: the
			// selector's feed is what reads it, and a silence tier that is
			// broken leaves the destination on a quiet hub rather than off the
			// air. Holding the platform connection while nothing is arriving is
			// the whole reason this tier exists.
			return h, nil
		}
		if err := e.selectorProblem(); err != nil {
			return nil, err
		}
		if err := e.silenceProblem(); err != nil {
			return nil, err
		}
		return e.sourceHub(), nil
	}
	e.mu.RLock()
	r := e.rends[*row.RenditionID]
	e.mu.RUnlock()
	if r == nil || r.hub == nil {
		// Starting it anyway would give the user a destination that looks
		// healthy and sends nothing.
		reason := "is not running"
		if r != nil && r.err != "" {
			reason = "failed to start: " + r.err
		}
		return nil, fmt.Errorf("rendition %d %s", *row.RenditionID, reason)
	}
	return r.hub, nil
}

// destSpec hashes everything about a destination that requires a restart.
//
// upstream is its rendition's signature, empty for passthrough. Folding both it
// and the rendition id in is what makes moving a destination between tiers, or
// editing the tier it sits on, restart that destination and no other.
func destSpec(row *db.Destination, compiled routing.Result, upstream string) string {
	source := "passthrough"
	if row.RenditionID != nil {
		source = "rendition:" + strconv.FormatInt(*row.RenditionID, 10)
	}
	return hashStrings([]string{
		// Everything that reaches the argv, taken from the argv builder's own
		// input rather than listed again by hand. See destArgvSig.
		//
		// Resilience is deliberately ABSENT, and stays absent for free: it is a
		// property of the supervisor, not of the command line, so it is not a
		// field of ffmpeg.DestSpec at all. supervisor.SetPolicy carries it into
		// a process that is already running -- see applyDestPolicy. The
		// reasoning that first put it in this hash was right about the danger (a
		// setting stored and never reaching the process it governs) and wrong
		// about the remedy: the remedy was to deliver it, not to drop the
		// operator's connection in order to deliver it.
		destArgvSig(destSpecFor(nil, row, compiled, "", row.Target())),
		// Not on the command line, and both required. Which tier a destination
		// reads is what makes moving it between tiers, or editing the tier it
		// sits on, restart that destination and no other.
		source, upstream,
	})
}

// destSpecFor is one output's ffmpeg.DestSpec: the single description of what
// goes on its command line.
//
// THE ONE PLACE. destArgs renders it, destSpec and backupSpecOf hash it. Before
// this, the two hashes were hand-written lists over the same inputs sitting 264
// lines apart, so adding a field to ffmpeg.DestSpec needed three edits and
// missing the third was silent and asymmetric -- the primary picked the setting
// up, the backup kept the old command line, and the two feeds the platform
// received differed with nothing reporting it. compiled.OutLabel had already
// gone missing from both.
//
// log may be nil, and is on the hashing paths: they build this purely to hash
// it, twice per destination per reconcile, and expertArgv's warning about
// unparseable text would otherwise be printed three times a pass about a value
// the editor already reports.
func destSpecFor(log *slog.Logger, row *db.Destination, compiled routing.Result, relayURL, target string) ffmpeg.DestSpec {
	return ffmpeg.DestSpec{
		Kind:          ffmpeg.DestKind(row.Kind),
		Target:        target,
		RelayURL:      relayURL,
		FilterComplex: compiled.FilterComplex,
		AudioOutLabel: compiled.OutLabel,
		// The second (VOD) audio track. Empty for every destination that has
		// not opted in, which is nearly all of them, and empty produces byte
		// for byte the command it produced before this existed. It reaches the
		// backup feed through the same struct, so a redundant feed carries the
		// same two tracks as the primary rather than silently dropping one --
		// which is the asymmetry this function's doc comment exists about.
		SecondAudioOutLabel: compiled.SecondOutLabel,
		AudioBitrate:        row.AudioBitrate,
		SampleRate:          row.Profile.SampleRate,
		CopyVideo:           true,
		// A negative routing delay pulls audio ahead of picture, which no
		// audio filter can do, so the compiler hands the amount over here
		// and the video is held back instead.
		VideoDelayMS: compiled.VideoDelayMS,
		// Expert mode. Spliced by DestinationArgs into the two positions
		// FFmpeg binds options from, which are the same two the operator
		// was shown in the confirm dialog.
		ExtraInputArgs:  expertArgv(log, row, row.ExtraInputArgs, "input"),
		ExtraOutputArgs: expertArgv(log, row, row.ExtraOutputArgs, "output"),
		// Output audio encoding. Zero value is AAC stereo.
		Audio: ffmpeg.AudioSpec{
			Codec: audioCodecOf(row.Audio.Codec),
			Mono:  row.Audio.Mono,
		},
		// Bit-exact audio. The compiled result is still the authority on WHICH
		// tracks go out -- routing.Compile runs for a copy destination exactly
		// as it does for a mixing one, so the profile's selection and the role
		// exclusions reach the -map list rather than being bypassed. The graph
		// it also produced is simply not used, and DestinationArgs drops it.
		CopyAudio:   row.Audio.Copy,
		AudioTracks: compiled.Tracks,
		// Muxer and socket tuning. Its zero value emits nothing, so a
		// destination that has not opted in produces exactly the command
		// it always did.
		Transport: ffmpeg.TransportSpec{
			NoDurationFilesize: row.Transport.NoDurationFilesize,
			MuxQueuePackets:    row.Transport.MuxQueuePackets,
			MuxQueueBytes:      row.Transport.MuxQueueBytes,
			RWTimeoutSeconds:   row.Transport.RWTimeoutSeconds,
		},
	}
}

// destArgvSig renders one output's command line into a single deterministic
// string, for the restart hashes.
//
// THE COMMAND LINE ITSELF, not a description of it. A restart is only ever
// justified by the argv changing, so the signature is taken from the argv the
// destination would be spawned with. There is no list to keep in step with
// anything: a field added to ffmpeg.DestSpec moves this hash if and only if
// DestinationArgs reads it.
//
// This replaces `%#v` over the spec, which was the previous step away from two
// hand-written field lists. It fixed the direction that silently diverges -- a
// field on the command line and in neither hash -- but it reintroduced B5 in the
// other direction, the one B5 was raised about. B5 was expert arguments whose
// whitespace moved the hash without moving the argv; the unified hash brought
// the same defect back through DestSpec fields the builder does not read:
//
//   - CopyVideo is never read by DestinationArgs at all. `-c:v copy` is
//     unconditional. It is documented as "always true in v1 and here to make the
//     guarantee explicit and testable", and both construction sites hardcode
//     true -- so the live cost was latent, but the class was not.
//   - Audio.Codec only reaches the argv where FFmpeg can honour it. Opus is
//     refused on RTMP, which cannot carry it, so choosing Opus on an RTMP
//     destination changed the hash, dropped a live connection to the platform,
//     and spawned the identical command line.
//
// Both fall out for free now, and so does the next one, because "does this
// change the command" is answered by building the command.
//
// RelayURL is cleared first. It is the allocated port, which is new on every
// start, so hashing it would make every destination's signature differ from
// itself and nothing would ever be left running.
func destArgvSig(s ffmpeg.DestSpec) string {
	s.RelayURL = ""
	return hashStrings(ffmpeg.DestinationArgs(s))
}

// audioCodecOf maps the stored codec name onto the FFmpeg encoder name. An
// unrecognised value falls back to AAC rather than reaching the command line:
// a destination row written by a newer build must still stream, and AAC is the
// one codec every platform takes.
func audioCodecOf(stored string) string {
	if stored == db.DestAudioOpus {
		return ffmpeg.AudioCodecOpus
	}
	return ffmpeg.AudioCodecAAC
}

// secondsOr converts a settings value in seconds to a Duration, returning the
// fallback when the operator has not set one. Zero means "the supervisor's
// default", never "no delay at all" -- a zero backoff would be a spin loop
// against a platform that is refusing us.
func secondsOr(v int, fallback time.Duration) time.Duration {
	if v <= 0 {
		return fallback
	}
	return time.Duration(v) * time.Second
}

// destPolicy is the reconnect policy for one destination row.
//
// The zero value must map to the zero Policy, not to an explicit 1s/30s: that
// is what leaves supervisor.New's own defaults in place, which is what every
// destination ran on before the policy was configurable.
func destPolicy(row *db.Destination) supervisor.Policy {
	return supervisor.Policy{
		MinBackoff:  secondsOr(row.Resilience.MinBackoffSeconds, 0),
		MaxBackoff:  secondsOr(row.Resilience.MaxBackoffSeconds, 0),
		MaxRestarts: row.Resilience.GiveUpAfter,
	}
}

// applyDestPolicy carries a changed reconnect policy into a destination that is
// already running, and revives one that had given up under a stricter rule.
//
// BOTH outputs, not just the primary. The redundant feed is a separate
// supervisor.Process built from the same row, and Resilience is deliberately
// absent from backupSpecOf -- so reconcileBackup short-circuits on an unchanged
// spec and nothing else in the file would ever deliver the new policy to it. An
// operator raising giveUpAfter was told by noteReload that it applied; the
// primary was retuned and revived, and the backup kept the MaxRestarts baked in
// at supervisor.New time and, if it was already in StateFailed, stayed failed
// permanently.
//
// The fix is NOT to add Resilience to backupSpecOf. That would restart a live
// redundant feed to deliver a value that can be set on a running process, which
// is the mistake destSpec's own comment records having already made once.
func (e *Engine) applyDestPolicy(d *destination, row *db.Destination) {
	if d == nil {
		return
	}
	want := destPolicy(row)
	e.retunePolicy(d.proc, row, want, "")
	e.retunePolicy(d.backup, row, want, destRoleBackup)
}

// retunePolicy pushes a changed reconnect policy into one running process, and
// revives it if it had given up under a stricter rule.
//
// The revival is the one place this work chooses a restart over a live apply,
// and it is chosen deliberately. Raising GiveUpAfter on an output that has
// already exhausted the old limit and would otherwise sit in StateFailed for
// ever is exactly the "stored, reported as applied, and does nothing" failure
// this file is littered with warnings about. Lowering it is NOT retroactive: an
// output is not executed for exits it made under the old rules.
//
// Start() cannot do the revival -- supervise returns down the give-up path
// without clearing p.running, so Start takes its idempotence early return.
// Restart() is the only door, and its Stop returns immediately because the
// supervise goroutine has already closed done.
//
// role is empty for the primary, so its log lines and reload notes are
// byte-identical to what they were before the backup shared this code.
func (e *Engine) retunePolicy(p *supervisor.Process, row *db.Destination, want supervisor.Policy, role string) {
	if p == nil {
		return
	}
	// Normalised on BOTH sides, or the comparison is between two spellings of
	// the same policy. destPolicy reads the database row, where "unconfigured"
	// is 0; supervisor.New filled those zeroes in with its defaults before the
	// process ever ran. Compared raw, an untouched destination never matched --
	// so every reconcile logged it as retuned and added a reload note, and a
	// destination with a backup did it twice.
	before := p.Policy().Normalised()
	want = want.Normalised()
	if before == want {
		return
	}
	name := row.Name
	if role != "" {
		name = row.Name + " (" + role + ")"
	}
	p.SetPolicy(want)
	e.log.Info("destination reconnect policy retuned without a restart",
		"dest", name, "minBackoff", want.MinBackoff, "maxBackoff", want.MaxBackoff,
		"giveUpAfter", want.MaxRestarts)
	e.noteReload("destination", name, reloadLive,
		fmt.Sprintf("reconnect policy retuned to %s..%s, giving up after %d",
			want.MinBackoff, want.MaxBackoff, want.MaxRestarts))

	if p.Status().State != supervisor.StateFailed || !moreForgiving(before, want) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	p.Restart(ctx)
	e.log.Info("destination revived: it had given up under the previous limit",
		"dest", name, "giveUpAfter", want.MaxRestarts)
	e.noteReload("destination", name, reloadRestart,
		"it had given up under the previous limit and the new one is more forgiving")
}

// moreForgiving reports whether want allows more attempts than before.
//
// 0 means unlimited, so it is the MOST forgiving value rather than the least.
// Compared as a plain number it sorts exactly the wrong way round, which would
// revive a destination the operator had just told to give up sooner.
func moreForgiving(before, want supervisor.Policy) bool {
	if want.MaxRestarts == 0 {
		return before.MaxRestarts != 0
	}
	if before.MaxRestarts == 0 {
		return false
	}
	return want.MaxRestarts > before.MaxRestarts
}

// expertArgv parses a destination's hand-written arguments into an argv.
//
// A parse failure yields nothing rather than an error, and that is deliberate:
// the API validates on the way in, so anything unparseable here got in before
// the rules were what they are now. Dropping it starts the destination on its
// generated command — the stream keeps running and the editor still shows the
// operator the stored text with the reason it will not apply. Refusing to start
// the destination would be the restrictive-direction failure this repo has
// already paid for three times.
func expertArgv(log *slog.Logger, row *db.Destination, raw, field string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	argv, err := ffmpeg.SplitArgs(raw)
	if err != nil {
		// A nil log is the restart-hash path -- see destSpecFor. It builds the
		// same spec purely to hash it, twice per destination per reconcile, and
		// this line would be printed three times a pass about a value the
		// editor already shows the operator with the reason it will not apply.
		if log != nil {
			log.Warn("ignoring unparseable expert arguments",
				"dest", row.Name, "field", field, "err", err)
		}
		return nil
	}
	return argv
}

// destWritesAFile reports whether a destination's target is a path on this
// machine rather than a network endpoint, and therefore has to be confined to
// the recordings directory before FFmpeg is handed it.
//
// An audio-only destination is either an Icecast mount or a bare filename; the
// scheme is the only thing that tells them apart. Without this an audio file
// target would be written relative to the process working directory, outside
// the confinement every other file destination has.
func destWritesAFile(row *db.Destination) bool {
	switch row.Kind {
	case db.DestFile:
		return true
	case db.DestAudio:
		return !strings.Contains(row.URL, "://")
	default:
		return false
	}
}

// destSubName is the relay subscription for one of a destination's outputs.
//
// THE ROLE IS PART OF THE NAME, ALWAYS, and that is not tidiness.
// Hub.Subscribe is a map assignment keyed by this string:
//
//	h.subs[name] = &subscriber{...}
//
// so registering two outputs under one name REPLACES the first. The replaced
// process keeps running, keeps a correct command line, and keeps a healthy
// card -- and receives no packets at all. Nothing about the process, its
// target URL, or the destination's status reveals it.
//
// The primary's name is unchanged (`dest:<id>`), so no existing subscription
// moves; only new roles get a suffix.
func destSubName(id int64, role string) string {
	if role == "" {
		return fmt.Sprintf("dest:%d", id)
	}
	return fmt.Sprintf("dest:%d:%s", id, role)
}

// destArgs is one output's FFmpeg command line.
//
// Shared by the primary and the backup so the two cannot drift: a redundant
// feed encoded differently from the one it backs up is not redundancy, and the
// difference would be invisible until somebody compared two argv strings on the
// monitoring page.
//
// Only the relay it reads and the target it writes differ between them.
func (e *Engine) destArgs(row *db.Destination, compiled routing.Result, relayURL, target string) []string {
	return ffmpeg.DestinationArgs(destSpecFor(e.log, row, compiled, relayURL, target))
}

// destRoleBackup names the redundant output's subscription.
const destRoleBackup = "backup"

// wantsBackup reports whether this destination should be publishing a
// redundant feed right now.
//
// Both halves are required. The intent alone is intent; without an endpoint
// there is nowhere to publish, which is the normal state between enabling the
// setting and the next broadcast being created.
//
// NOTHING HERE NAMES A PLATFORM, and that is the point. This read used to be
// row.Facebook.BackupIngest, which meant the engine's gate on two
// platform-neutral columns went through a platform-named struct: a Twitch row
// with an endpoint and the intent set could never start a redundant feed,
// because the field it was gated on belonged to a different platform's block.
// The endpoint fields already carried the argument in their own comment -- the
// engine should not have to know which platform a destination is -- and the
// intent now sits beside them.
func wantsBackup(row *db.Destination) bool {
	return row.BackupIngestWanted && row.BackupURL != "" && row.Kind == db.DestRTMP
}

// backupCompiled is the graph the REDUNDANT feed runs, which is not always the
// primary's.
//
// The one place the two legitimately differ, and it is Twitch-specific. A
// negotiated Enhanced Broadcasting configuration names ONE ingest endpoint; the
// backup publishes to the operator's own BackupURL, which is an ordinary
// one-track RTMP ingest whatever the negotiation said. So a destination whose
// primary won a second audio track must still send ONE track on its backup, or
// the redundant feed is the silent two-track push all over again -- on exactly
// the feed that is supposed to take over when the primary drops.
//
// oneMix is non-nil for precisely the destinations this applies to: it is set
// only where the second mix was compiled AND is conditional on a Twitch
// negotiation (see vodNeedsNegotiation), so for every other destination this
// returns the primary's graph unchanged and the generic two-mix egress to a
// non-Twitch target is untouched.
func backupCompiled(p destPlan) routing.Result {
	if p.oneMix != nil {
		return *p.oneMix
	}
	return p.compiled
}

// backupTarget is the redundant output's URL, assembled the way Target() does.
func backupTarget(row *db.Destination) string {
	if row.BackupStreamKey == "" {
		return row.BackupURL
	}
	return strings.TrimRight(row.BackupURL, "/") + "/" + row.BackupStreamKey
}

// backupSpecOf hashes everything on the BACKUP's command line.
//
// SAME INPUTS, SEPARATE VERDICT, and the distinction is the whole design. The
// two share one description of the command line -- destSpecFor, with the
// backup's target substituted -- because a redundant feed encoded differently
// from the one it backs up is not redundancy. They must never share a hash:
// enabling backup already costs one reconnect for an unavoidable reason (a new
// broadcast means a new primary key), and it must not cost a second one for an
// avoidable one.
//
// So the toggle is here and absent from destSpec, and nothing about the backup
// can move the primary's number.
func backupSpecOf(row *db.Destination, compiled routing.Result, upstream string) string {
	return hashStrings([]string{
		strconv.FormatBool(wantsBackup(row)),
		destArgvSig(destSpecFor(nil, row, compiled, "", backupTarget(row))),
		upstream,
	})
}

// multitrackDeadline is the backstop on the go-live negotiation.
//
// It is NOT the timeout that normally fires: multitrack.Client carries its own
// (10s), and obs-studio allows five seconds for the same call. This is the
// guard for a transport that has none -- a Client with an *http.Client whose
// Timeout is zero would otherwise be free to hold a broadcast open for as long
// as the far end keeps the socket alive. Above the client's own, so that the
// client's timeout produces its own error message where it can.
//
// A var so a test can prove the broadcast starts anyway without waiting for it.
var multitrackDeadline = 15 * time.Second

// startDest brings one destination up.
//
// ENHANCED BROADCASTING IS NEGOTIATED HERE AND NOWHERE ELSE, and "here" is the
// reason "it says so once" is true. This function runs once per START: a
// destination that survives a reconcile is left strictly alone, and a
// reconnect reuses the argv the supervisor already holds rather than coming
// back through this path. So the note is produced once because the negotiation
// happens once, with no bookkeeping to get wrong.
func (e *Engine) startDest(p destPlan, hub *relay.Hub, startDelay time.Duration) error {
	row, spec := p.row, p.spec
	compiled := p.compiled

	// BEFORE the port and the subscription. A negotiation that takes the whole
	// of its timeout would otherwise hold a relay port and a hub entry open
	// for the duration, and this call is on the path between the operator
	// pressing the button and anything reaching a viewer.
	ctx, cancel := context.WithTimeout(context.Background(), multitrackDeadline)
	mt := e.negotiateFor(ctx, row)
	cancel()

	vodDropped := p.vodDropped
	if p.oneMix != nil && !mt.Use {
		// The negotiation this destination's second audio mix was conditional
		// on did not succeed, so the pair must not go on the wire. Twitch's
		// ordinary RTMP ingest takes one audio track and would either drop or
		// refuse the second; publishing it anyway is the silent two-track push
		// this gate exists to stop.
		compiled = *p.oneMix
		vodDropped = noteVODNotNegotiated
	}

	port, err := e.alloc.Allocate()
	if err != nil {
		return err
	}
	subName := destSubName(row.ID, "")
	url := hub.Subscribe(subName, port)

	target := row.Target()
	if mt.Use {
		// THE NEGOTIATED TARGET REPLACES THE STORED ONE WHOLE, server and key
		// together. The multitrack ingest is a different host from the one the
		// row names, and the key is Twitch's minted 312-character credential
		// with the agreed ladder signed inside it -- publishing to the
		// negotiated host with the operator's own key would connect and send a
		// ladder the ingest never agreed to. See multitrack.Outcome.Use.
		target = mt.Target
	}
	writesAFile := destWritesAFile(row)
	if writesAFile {
		// File destinations are confined to the recordings directory; the
		// path never comes straight from user input.
		resolved, err := e.recman.ResolveForWrite(row.URL)
		if err != nil {
			// The hub this subscribed to, which is not always the ingest's:
			// upstreamHub returns e.hub only for a passthrough destination on a
			// bare source, and a rendition or a running selector returns its
			// own. Unsubscribing from e.hub left the subscriber in the OTHER hub
			// for ever while the port went back to the allocator -- so the port
			// is reissued and the stale entry blasts transport-stream datagrams
			// into whatever now owns that socket.
			hub.Unsubscribe(subName)
			e.alloc.Release(port)
			return err
		}
		target = resolved
	}

	buildArgs := func(out string) []string {
		return e.destArgs(row, compiled, url, out)
	}

	// Only a file destination needs a fresh argv per spawn, and only because
	// its output path cannot be reused. An RTMP or SRT target is reconnected
	// to, not recreated, so rebuilding its command line every respawn would be
	// churn with no benefit — and it would make the argv shown on the
	// monitoring page differ from the one that has been running all along.
	var nextArgs func() []string
	if writesAFile {
		nextArgs = func() []string {
			out, err := e.recman.ResolveForWrite(row.URL)
			if err != nil {
				// Keep the last known-good path rather than refusing to start:
				// the resolver only fails when the directory itself is
				// unusable, and that is already reported by the recorder's own
				// storage guard.
				e.log.Error("destination: cannot pick an output filename",
					"dest", row.Name, "err", err)
				return buildArgs(target)
			}
			e.noteRollover(row, out)
			return buildArgs(out)
		}
	}

	onProgress, watch := e.firstMediaLoggerWatched(row.Name, row.Kind)

	// Declared before the Spec because OnLog's #674 re-probe handler closes
	// over it: that handler is built inside the Spec that constructs this
	// very child, so it can only resolve the process lazily.
	var proc *supervisor.Process
	proc = supervisor.New(e.log, supervisor.Spec{
		Name:     subName,
		Kind:     "destination",
		Bin:      e.tools.FFmpeg,
		Args:     buildArgs(target),
		NextArgs: nextArgs,
		// The minted key is passed as an EXTRA literal rather than left to
		// inherit the row's registration, because it cannot: SecretSet.Scrub is
		// a substring replace and the minted key ENDS WITH the operator's
		// original, so registering only the original masks the last segment and
		// leaves the signature and the hex manifest standing in the log. Empty
		// when nothing was minted, which destSecrets and alerts.NewSecretSet
		// both drop. See TestTheMintedKeyIsMaskedWholeAndNotJustItsTail.
		Secrets: destSecrets(row, mt.MintedKey),
		// The only signal that says this destination is PUBLISHING rather than
		// merely spawned. See firstMediaLogger and #675.
		OnProgress:  onProgress,
		AutoRestart: true,
		// Per-destination reconnect policy. Zero values leave the supervisor's
		// own defaults in place, which is what every destination ran on before
		// this was configurable. The same three values are re-applied without a
		// restart by applyDestPolicy when they change.
		MinBackoff:  destPolicy(row).MinBackoff,
		MaxBackoff:  destPolicy(row).MaxBackoff,
		MaxRestarts: destPolicy(row).MaxRestarts,
		// Spaced out so going live does not spawn every destination in the
		// same tick. First spawn only -- a reconnect is never delayed.
		StartDelay: startDelay,
		// #674: a destination that probed before its audio existed can only be
		// restarted. See reprobeOnUncharacterisedAudio -- the trigger is the
		// demuxer's own "could not find codec parameters" rather than an
		// absence of output, because absence of output is also what a
		// mismatched publisher and a still-probing child look like.
		OnLog: e.reprobeOnUncharacterisedAudio(row.Name, row.Kind,
			func() *supervisor.Process { return proc }, e.onLog),
		OnState: e.onState,
		LogSink: logSink{e},
	})

	e.mu.Lock()
	// Shutdown may have run since this reconcile started; publishing under the
	// same lock Stop collects destinations with is what keeps a late start from
	// becoming an FFmpeg still publishing to a platform, holding a relay port,
	// that nothing can find or stop. The identical guard startRendition carries,
	// for the identical reason -- this was the one start path in the file
	// without it.
	//
	// nil rather than an error: an error makes startDestinations record a broken
	// destination in the map this has just declined to write to.
	//
	// The guard alone is NOT the whole fix, and reading it as though it were is
	// how the remaining window survived a review. It closes the case where Stop
	// has already run. The case where Stop runs AFTER this publication and
	// BEFORE the Start below is closed by supervisor.Process retiring on Stop:
	// the teardown Stop performs on this very entry latches the process, and the
	// Start below becomes a no-op. Without that latch, Stop on a process that
	// has not started yet is a no-op, so the shutdown would release this port
	// and this subscription and the Start would then bring a child up on both.
	if e.stopped {
		e.mu.Unlock()
		hub.Unsubscribe(subName)
		e.alloc.Release(port)
		return nil
	}
	e.dests[row.ID] = &destination{
		row: row, proc: proc, port: port, subName: subName, watch: watch,
		compiled: compiled, hub: hub, spec: spec,
		multitrack: mt, vodDropped: vodDropped,
	}
	e.mu.Unlock()

	if e.afterPublish != nil {
		e.afterPublish()
	}
	// A new child supersedes whatever the previous one's stop failed to observe:
	// the warning is about THIS destination's current situation, not a log.
	e.noteStopOutcome(row.ID, nil)
	proc.Start()
	// SPAWNED, NOT PUBLISHING, and the wording now says so.
	//
	// This line read "destination started" and was written on the instruction
	// after proc.Start(), which reports only that a child process exists.
	// FFmpeg can and does fail after that -- "Error initializing filters!",
	// "Could not open encoder before EOF", "Nothing was written into output
	// file" -- and every one of those leaves this line standing as the last
	// word on the destination's health.
	//
	// It cost a whole investigation: an operator, and five passes of debugging,
	// saw a healthy console and a started destination while nothing at all
	// reached the platform. The failure was visible only from the receiving end
	// as an absence, which is the least informative place to observe it. #675.
	e.log.Info("destination starting", "dest", row.Name, "kind", row.Kind,
		"tracks", compiled.Summary, "rendition", renditionLabel(row))
	e.logMultitrack(row, mt, vodDropped)
	e.noteReload("destination", row.Name, reloadRestart, "started")
	return nil
}

// logMultitrack says what the negotiation decided, once per start.
//
// AT INFO, NEVER AT WARN OR ERROR, whatever the verdict. Twitch refuses any
// client without a supported GPU and polyemesis is installed on the operator's
// own server, so on most installs the fallback is the path every time for ever
// -- logging it as a fault would make the ordinary case look broken, would be
// counted as an incident by the alert rules, and would send somebody hunting
// for a problem that is not there. multitrack.Outcome's own doc comment refuses
// an error return for exactly this reason.
//
// The note is Twitch's sentence and ours, already scrubbed of the stream key by
// multitrack.Negotiate. Neither the minted key nor the negotiated target is
// logged: mt.Target carries the credential as its last path segment, and the
// server half is already in the argv the monitoring page renders.
func (e *Engine) logMultitrack(row *db.Destination, mt mtDecision, vodDropped string) {
	if mt.Asked {
		e.log.Info("enhanced broadcasting", "dest", row.Name,
			"verdict", string(mt.Verdict), "note", mt.Note)
		for _, d := range mt.Divergences {
			// Advisory, and logged as such. They annotate a destination that IS
			// publishing; a divergence must never read like a reason it is not.
			e.log.Info("enhanced broadcasting: Twitch's configuration differs from this destination's",
				"dest", row.Name, "field", d.Field, "detail", d.Detail)
		}
	}
	if vodDropped != "" {
		e.log.Info("second audio track not sent", "dest", row.Name, "reason", vodDropped)
	}
}

func (e *Engine) teardownDest(d *destination) {
	if d == nil {
		return
	}
	e.noteReload("destination", d.row.Name, reloadRestart,
		"its command line changed, or it was disabled or removed")
	if d.proc != nil {
		ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		// NOT DISCARDED. This is the one Stop in the tree whose error a caller is
		// waiting on: POST /destinations/{id}/stop reads the process state back
		// so that "a success response is evidence that something happened", and
		// the state is StateStopped on both of stop()'s arms. The error is the
		// only thing that distinguishes "the child was reaped" from "SIGKILL was
		// issued and nobody waited", and those have different consequences --
		// the port and the hub subscription released below are about to be handed
		// to somebody else (#209).
		e.noteStopOutcome(d.row.ID, d.proc.Stop(ctx))
		cancel()
	}
	if d.subName != "" {
		// Its own hub, which is not always the ingest's.
		hub := d.hub
		if hub == nil {
			hub = e.hub
		}
		hub.Unsubscribe(d.subName)
	}
	if d.port != 0 {
		e.alloc.Release(d.port)
	}
	e.stopBackup(d)
}

// stopUnreapedWarning is what a caller is told when a stop ended on the deadline
// arm. It says what is uncertain and what that costs, because "stopped" already
// said the thing that is certain.
const stopUnreapedWarning = "the child was sent SIGKILL and was not waited for; " +
	"it may still be running and still publishing"

// noteStopOutcome records what a destination's stop actually achieved.
//
// err is the value (*supervisor.Process).Stop returned. Only ErrStopDeadline is
// recorded: every other error means the stop did not happen for a reason the
// caller can already see, while ErrStopDeadline means it happened and the result
// was not observed, which is the distinction nothing downstream could make.
//
// A nil err clears the record, so this is also how a fresh start retracts an old
// warning.
func (e *Engine) noteStopOutcome(id int64, err error) {
	e.stopMu.Lock()
	defer e.stopMu.Unlock()
	if !errors.Is(err, supervisor.ErrStopDeadline) {
		delete(e.unreaped, id)
		return
	}
	if e.unreaped == nil {
		e.unreaped = make(map[int64]string, 1)
	}
	e.unreaped[id] = stopUnreapedWarning
}

// StopUnreaped reports whether the last stop of this destination left a child
// that was killed but never observed dead, and the warning that says so.
// noteRollover reports that a file destination's respawn is writing somewhere
// other than the name the operator configured.
//
// THE ROLLOVER ITSELF IS CORRECT AND IS NOT WHAT THIS IS ABOUT. ResolveForWrite
// refuses to destroy footage, so a respawn whose configured name already holds
// bytes is given a timestamped sibling instead. That is the right call and it is
// load-bearing: the alternative, -y, would truncate an earlier take every time a
// destination flapped.
//
// What was wrong is that nobody was told. The child that stopped can exit
// CLEANLY -- in FFmpeg 8.1 a demuxer-side failure exits 0 -- and a clean exit is
// logged at Info with no entry in the process log ring, while LastError stays
// empty because -loglevel warning suppresses the lines classify would have kept.
// So the operator's configured filename held a Matroska header and nothing else,
// the footage was in a file nobody had mentioned, and the only trace anywhere was
// a restart counter going from 0 to 1. It reads exactly like an empty recording.
//
// Warn rather than Info: this is the one moment at which the name an operator
// asked for stops being the name their footage is in.
func (e *Engine) noteRollover(row *db.Destination, actual string) {
	want, err := e.recman.Resolve(row.URL)
	if err != nil || actual == "" || actual == want {
		return
	}
	e.stopMu.Lock()
	if e.rolledOver == nil {
		e.rolledOver = map[int64]string{}
	}
	e.rolledOver[row.ID] = actual
	e.stopMu.Unlock()

	e.log.Warn("destination: recording continued in a different file",
		"dest", row.Name, "configured", want, "writing", actual,
		"why", "the configured file already holds footage and is never overwritten")

	// Nil-safe by the same discipline as e.hooks elsewhere: an install with no
	// hooks configured still gets the log line and the status field.
	if e.hooks != nil {
		e.hooks.Publish(hooks.Event{
			Trigger: hooks.TriggerDestinationRolledOver,
			At:      time.Now(),
			Source:  hooks.SourceRef{ID: e.sourceID, Name: e.SourceName()},
			Destination: &hooks.DestinationRef{
				ID: row.ID, Name: row.Name, Platform: string(row.Platform),
			},
			Reason: actual,
		})
	}
}

// RolledOver is the path a file destination is actually writing to, when that
// differs from the configured one, and whether it differs at all.
func (e *Engine) RolledOver(id int64) (path string, rolled bool) {
	e.stopMu.Lock()
	defer e.stopMu.Unlock()
	p, ok := e.rolledOver[id]
	return p, ok
}

func (e *Engine) StopUnreaped(id int64) (warning string, unreaped bool) {
	e.stopMu.Lock()
	defer e.stopMu.Unlock()
	w, ok := e.unreaped[id]
	return w, ok
}

// backupPending is what an operator is told between switching redundancy on and
// the platform having somewhere to send it.
const backupPending = "Facebook has not offered a backup ingest endpoint yet; " +
	"it is provisioned when the broadcast is created"

// reconcileBackup brings the redundant output into line with the row and
// publishes the result, without ever consulting or touching the primary.
//
// Called for a destination that is already running as well as one just
// started, because the toggle is deliberately ABSENT from destSpec: nothing
// else would ever notice it changed. Absence from that hash stops the primary
// cycling; this is what makes the setting take effect.
//
// prev is the destination currently at e.dests[id], and NOTHING HERE WRITES
// THROUGH IT. The backup fields used to be set in place immediately after
// startDestinations published the pointer, which is precisely what the
// copy-on-publish comment above that publication says must never happen:
// "Status hands out these pointers and then reads their fields after dropping
// the lock, which is only safe while a published destination never changes
// again." A replacement is built, filled in while nothing else can see it, and
// swapped in by publishDest.
func (e *Engine) reconcileBackup(id int64, prev *destination, compiled routing.Result, upstream string) {
	if prev == nil || prev.row == nil {
		return
	}
	// The toggle-on-but-no-endpoint case is included in "not wanted": it is a
	// real state between enabling the setting and the next broadcast being
	// created, and it is reported rather than left blank.
	reason := ""
	if !wantsBackup(prev.row) && prev.row.BackupIngestWanted && prev.row.BackupURL == "" {
		reason = backupPending
	}
	// want is computed ONLY on the branch that reads it. backupSpecOf renders a
	// whole destination argv and takes a SHA-256 of it, and it used to run above
	// this switch -- so every destination without redundancy, on every reconcile,
	// paid for a hash of a command line that was then discarded by the early
	// return below. That is the common case, not an edge one.
	var want string
	if wantsBackup(prev.row) {
		want = backupSpecOf(prev.row, compiled, upstream)
		if prev.backup != nil && prev.backupSpec == want {
			return
		}
	} else if prev.backup == nil && prev.backupSub == "" && prev.backupPort == 0 &&
		prev.backupSpec == "" && prev.backupErr == reason {
		// Already exactly right, which is the common case on every reconcile of
		// every destination without redundancy. Returning here is what keeps
		// this from republishing an identical destination each pass.
		return
	}

	e.stopBackup(prev)
	next := *prev
	next.backup, next.backupPort, next.backupSub = nil, 0, ""
	next.backupSpec, next.backupErr = "", reason
	if wantsBackup(prev.row) {
		e.buildBackup(&next, compiled, want)
	}
	e.publishDest(id, prev, &next)
}

// publishDest swaps a replacement destination into e.dests and starts whatever
// the replacement brought with it.
//
// The check and the swap are one critical section, which is what startRendition
// and startDest do and for the same reason: shutdown may have run since this
// reconcile started, and a process started after Stop has copied e.dests is an
// orphan nothing can ever reach. The identity check on prev is the other half
// -- a replacement built from an entry that is no longer the one in the map
// describes a destination that has already been torn down.
//
// And, as in startDest, the guard is only half of it: a Stop that lands between
// the swap and the Start below passes the guard, finds the replacement in the
// map, and stops a backup that has not started. supervisor.Process retires on
// Stop, so that teardown latches the process and the Start below does nothing.
// This is the worse of the two paths to leave open -- the backup runs with
// AutoRestart, so the orphan reconnects to the platform for ever.
func (e *Engine) publishDest(id int64, prev, next *destination) {
	e.mu.Lock()
	if e.stopped || e.dests[id] != prev {
		e.mu.Unlock()
		// Everything the replacement was given has to go back, or it is a
		// subscription and a relay port held by a struct nothing references.
		e.stopBackup(next)
		return
	}
	e.dests[id] = next
	e.mu.Unlock()

	if e.afterPublish != nil {
		e.afterPublish()
	}
	if next.backup != nil {
		next.backup.Start()
		e.log.Info("backup ingest started", "dest", next.row.Name)
	}
}

// buildBackup prepares the redundant output on a destination that is NOT
// published yet. It does not start the process; publishDest does, once the
// replacement is the one in the map.
//
// The port is asked for LAST and its refusal costs only the backup. There are
// 500 relay ports shared across every source engine, so exhaustion is a real
// state -- and it must cost the redundancy rather than the broadcast, which is
// why this returns quietly with a reason instead of failing the destination.
func (e *Engine) buildBackup(d *destination, compiled routing.Result, spec string) {
	hub := d.hub
	if hub == nil {
		hub = e.hub
	}
	port, err := e.alloc.Allocate()
	if err != nil {
		d.backupErr = "no relay port is free for the backup feed"
		e.log.Warn("backup ingest has no relay port; the primary is unaffected",
			"dest", d.row.Name, "err", err)
		return
	}
	sub := destSubName(d.row.ID, destRoleBackup)
	url := hub.Subscribe(sub, port)

	proc := supervisor.New(e.log, supervisor.Spec{
		Name:        sub,
		Kind:        "destination",
		Bin:         e.tools.FFmpeg,
		Args:        e.destArgs(d.row, compiled, url, backupTarget(d.row)),
		Secrets:     destSecrets(d.row),
		AutoRestart: true,
		MinBackoff:  destPolicy(d.row).MinBackoff,
		MaxBackoff:  destPolicy(d.row).MaxBackoff,
		MaxRestarts: destPolicy(d.row).MaxRestarts,
		OnLog:       e.onLog,
		OnState:     e.onState,
		LogSink:     logSink{e},
	})

	d.backup = proc
	d.backupPort = port
	d.backupSub = sub
	d.backupSpec = spec
	d.backupErr = ""
}

// stopBackup tears the redundant output down and releases everything it held.
//
// It READS d and does not write to it, which is what lets it be called on a
// destination the dashboard is already holding a pointer to -- from
// teardownDest, where the entry has been removed from the map but not from
// whatever Status copied a moment earlier. The caller publishes a replacement
// with the fields cleared instead.
//
// Unsubscribing and releasing the port matter as much as stopping the process:
// a stale subscriber keeps the hub writing datagrams into a socket nobody
// reads, and a leaked port is one fewer of the 500 shared across every source
// engine. Neither would be noticed until the pool ran out.
func (e *Engine) stopBackup(d *destination) {
	if d == nil {
		return
	}
	if d.backup != nil {
		ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		_ = d.backup.Stop(ctx)
		cancel()
	}
	if d.backupSub != "" {
		hub := d.hub
		if hub == nil {
			hub = e.hub
		}
		hub.Unsubscribe(d.backupSub)
	}
	if d.backupPort != 0 {
		e.alloc.Release(d.backupPort)
	}
}

// firstMediaLogger returns an OnProgress handler that says once, per run, that
// a destination has actually published something.
//
// OutTimeMS is the field that means "media has moved": it advances for
// audio-only destinations where Frame never leaves zero, and it stays at zero
// while FFmpeg is bound and waiting rather than writing. supervisor.noteProgress
// uses the same field to start the uptime clock, so this is the same definition
// of "up", said out loud in the log rather than only in a status struct.
//
// ONCE PER RUN, NOT ONCE PER PROCESS. The handler outlives a restart -- the
// supervisor keeps its Spec across them -- so a plain sync.Once would announce
// the first publish of the first run and stay silent through every reconnect
// after it, which is exactly when an operator most needs to know the stream
// came back. A run is detected by OutTimeMS going BACKWARDS: a fresh FFmpeg
// starts its output clock near zero, so a value below the last one can only
// mean a new child.
func (e *Engine) firstMediaLogger(name string, kind db.DestKind) func(ffmpeg.Progress) {
	fn, _ := e.firstMediaLoggerWatched(name, kind)
	return fn
}

// firstMediaLoggerWatched is firstMediaLogger plus a latch saying whether media
// has moved.
//
// THE WATCHDOG THAT CONSUMED THIS LATCH WAS REMOVED. guardSilentPublish
// restarted a destination that was "receiving but publishing nothing" -- which
// is ALSO the correct steady state of a publisher that does not match the
// profile, and of a destination still inside its own startup probe. It broke
// acceptance-failover, which pins 0 restarts across a mismatched cut, and then
// masked the #674 ingest fix in acceptance 4c by restarting the destination
// every 30s so it never settled. A guard whose trigger cannot tell the fault
// from two healthy states is not a guard. The latch is kept because it is the
// honest definition of "publishing"; nothing acts on it automatically. Split rather than folded in because the plain form is what the rest of
// the engine and its tests want, and a handler that also returned state would
// make every caller carry something only one of them uses.
func (e *Engine) firstMediaLoggerWatched(name string, kind db.DestKind) (func(ffmpeg.Progress), *destWatch) {
	w := &destWatch{}
	return func(pr ffmpeg.Progress) {
		if pr.OutTimeMS <= 0 {
			return
		}
		w.mu.Lock()
		newRun := w.last == 0 || pr.OutTimeMS < w.last
		w.last = pr.OutTimeMS
		w.published = true
		w.mu.Unlock()
		if newRun {
			e.log.Info("destination publishing", "dest", name, "kind", kind)
		}
	}, w
}

// destWatch is what a destination's progress stream has proved so far.
type destWatch struct {
	mu        sync.Mutex
	last      int64
	published bool
}

func (w *destWatch) publishedSinceRearm() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.published
}

func (w *destWatch) rearm() {
	w.mu.Lock()
	w.published = false
	w.mu.Unlock()
}
