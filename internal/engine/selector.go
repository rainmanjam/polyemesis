// The selector: which source is on air, and what happens when it stops being.
//
// One tier owning four kinds of input -- the live primary, a standby encoder, a
// playlist file and the slate -- plus the feed process that publishes whichever
// is chosen into the tier's own hub, and the operator-facing controls over all
// of it.
//
// Split out of engine.go because the boundary is load-bearing rather than
// cosmetic. Everything here is serialised by selMu, and everything in engine.go
// that touches this tier does so either while holding it or in a documented
// window where it does not -- which is exactly where the one bug in this
// subsystem lived: reconcileOutputs drops selMu between detachFeedForSilence
// and reconcileSilence, and a sweep landing in that gap started a feed on a hub
// about to close. With the two halves in one file that was invisible; with a
// file boundary, "who holds selMu across the silence swap" is a question the
// call sites have to answer out loud.
package engine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/playlistmedia"
	"github.com/rainmanjam/polyemesis/internal/relay"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// ----------------------------------------------------------------- selector
//
// The source-selector tier is a permanent relay between the ingest and
// everything downstream. Destinations, renditions, playout and the meters
// subscribe to it for their whole life; a separate FEED process decides what
// flows into it — the primary ingest, the backup ingest, or a synthesised
// slate — and a switch replaces only that feed.
//
//	ingest  -> [silence] -\
//	backup ingest --------+-> [selector] -> [rendition] -> destination
//	slate ----------------/
//
// That indirection is the whole feature. Switching a destination's own
// subscription would restart its process and drop the platform connection,
// which is the exact failure both failover and the slate exist to prevent. So
// the hub a destination reads never changes; only the bytes arriving on it do.
//
// It is OFF by default and costs nothing when off: sourceHub() answers exactly
// as it did before, and no feed process runs. Turning it on adds one `-c copy`
// remux hop, which is a few percent of a core and a few milliseconds — real,
// but not something an upgrade should spend on your behalf.
//
// TWO THINGS THAT DECIDE WHETHER THIS WORKS AT ALL:
//
// PTS CONTINUITY. Every feed normalises its own input to a timeline starting at
// zero, so without help each switch would hand the destinations a timestamp
// that jumps BACKWARDS by however long the previous feed had been running —
// and a platform answers a backwards jump by dropping the connection. Every
// feed is therefore started with -output_ts_offset set to the tier's own
// elapsed wall-clock time (SlateSpec.TimestampOffsetSeconds is the slate's
// spelling of the same flag). Because each feed publishes in real time, the
// published timeline stays within a switch's dead time of wall clock, forwards
// and monotonic across any number of switches. This is the first thing to look
// at if destinations stall on a switch rather than riding it.
//
// It is also why a feed is NOT AutoRestart: the supervisor would respawn it
// with the offset it was born with, and the second life of a feed that had been
// running an hour would publish an hour in the past. Respawn is owned by the
// sweep below, which computes a fresh offset every time.
//
// CODEC AND LAYOUT MATCH. A destination copies video (`-c:v copy`), so the
// slate has to look enough like the departed ingest that the platform's decoder
// re-initialises instead of giving up. The slate is therefore built at the
// PROBED width, height and frame rate, and the mpegts muxer repeats SPS/PPS
// in-band at every keyframe so a decoder has what it needs to re-init. What it
// cannot match is the encoder itself: the ingest's stream came from OBS, the
// slate's comes from libx264, and a platform that refuses that change will show
// a glitch or a reconnect at the switch. When the ingest was never probed there
// is no geometry to copy and the slate falls back to 1280x720 at 30, which a
// copying destination will pass through as a visible resolution change. Both of
// those are the deliberate choice: degrade visibly rather than corrupt quietly.
//
// The slate publishes ONE stereo track. A destination whose routing profile
// selects track 1 or above finds nothing on those inputs while the slate is up
// and stops producing output — no worse than the departed ingest, which
// delivered nothing on every track, but no better either. Closing that gap
// needs a track count on ffmpeg.SlateSpec, which is a change to a file this
// work does not own.

const (
	// selectorSweep is how often liveness is re-evaluated. Well under any
	// sensible grace period, so a switch lands near its deadline rather than up
	// to a whole period late.
	selectorSweep = 500 * time.Millisecond
	// feedRespawn bounds how fast a feed that will not start is retried, so a
	// slate with a broken encoder logs once a couple of seconds instead of
	// spawning in a tight loop.
	feedRespawn = 2 * time.Second
	// selectorSubName is the selector's subscription on whichever hub is
	// feeding it. Fixed: there is at most one feed.
	selectorSubName = "selector"
	// eventFailover announces a source switch. Declared here rather than in
	// internal/events because the constant is only meaningful to a system that
	// has a selector tier; the broker takes any type.
	eventFailover events.Type = "failover"
)

// sourceKind names what is feeding the selector.
type sourceKind string

const (
	sourceNone    sourceKind = ""
	sourcePrimary sourceKind = "primary"
	sourceBackup  sourceKind = "backup"
	// sourcePlaylist is a scheduled playlist feed: a real programme, but not a
	// live one. It sits between the ingests and the slate -- see candidatesFor
	// for why that is the ordering and not the other one.
	sourcePlaylist sourceKind = "playlist"
	sourceSlate    sourceKind = "slate"
)

// selector is the running tier.
type selector struct {
	// hub is the relay every downstream consumer reads, for its whole life.
	hub *relay.Hub
	// spec is deliberately constant while the tier is enabled. Anything that
	// changed it would close this hub and restart every destination on it,
	// which is precisely what the tier exists to avoid.
	spec string
	// startedAt is the tier's own clock and the origin of every feed's
	// timestamp offset.
	startedAt time.Time

	feed   *sourceFeed
	active sourceKind
	// feedAt is the last START ATTEMPT, recorded whether or not it worked, so a
	// feed that cannot start backs off instead of being retried every sweep.
	feedAt time.Time
	reason string
	// pinned is an operator's manual choice. It is honoured only while that
	// source is delivering: a pin that outlived its source would strand the
	// broadcast on a dead input, which is the opposite of what somebody
	// reaching for a manual override wants.
	pinned     sourceKind
	switchedAt time.Time
	switches   int
	err        string

	// live is per-source liveness, sampled from each hub's byte counter.
	live map[sourceKind]*liveness
}

// sourceFeed is the one process publishing into the selector's hub.
type sourceFeed struct {
	kind sourceKind
	// gen is this feed's engine-wide serial number, from Engine.feedGen. It is
	// carried only so the seam ledger can say WHICH feed handed over to which:
	// a run performs several switches between the same two kinds, and "primary
	// -> slate" on its own does not identify one of them. Nothing decides
	// anything on it.
	gen  uint64
	proc *supervisor.Process
	// in is the hub this feed READS, nil for the slate, which reads nothing.
	in      *relay.Hub
	port    int
	subName string
	// upstream hashes what the feed's command line depends on, so a settings
	// change respawns the feed without disturbing anything downstream of it.
	upstream  string
	offset    float64
	startedAt time.Time
}

// backupIngest is the second listener, with a hub of its own so it can be
// receiving from its encoder long before anybody asks it to go on air.
type backupIngest struct {
	proc *supervisor.Process
	hub  *relay.Hub
	sig  string
}

// playlistTier is the file on loop, and it is shaped exactly like backupIngest
// because it answers the same question the backup does: what else could be on
// air, and is it ready before anybody asks for it.
//
// The hub of its own is the entire point of the tier. A file played into the
// PRIMARY's hub carries bytes into it, so the primary reads live, and a live
// primary is the one thing failover never switches away from -- putting a file
// on air would have silently disabled the whole feature. With its own relay the
// primary goes quiet when the encoder goes quiet, which is the truth the
// selector needs.
type playlistTier struct {
	proc *supervisor.Process
	hub  *relay.Hub
	sig  string
	// listPath is the concat list this tier's FFmpeg is reading, and the tier
	// owns exactly that file: it wrote it before spawning and removes that same
	// path, and no other, once its process has stopped.
	//
	// Held as a field rather than recomputed at teardown because a recomputed
	// name is only as good as the inputs still being what they were. The
	// signature moves whenever the list does, so a teardown that rebuilt the
	// name from the CURRENT settings would sweep the wrong file, or none, at
	// precisely the moment an operator edits a playlist that is on air.
	listPath string
}

// liveness is one candidate source's delivery record, derived from bytes on its
// hub rather than from its process state. An SRT listener sits in "running"
// for as long as it waits for a publisher, so process state answers a different
// question than the one failover has to ask.
type liveness struct {
	rx uint64
	// at is when rx last increased, zero for a source that has never delivered.
	at time.Time
	// since is when the current unbroken run of delivery began, which is what an
	// automatic return measures its stability window against.
	since time.Time
}

func (l liveness) alive(now time.Time, grace time.Duration) bool {
	return !l.at.IsZero() && now.Sub(l.at) < grace
}

func (l liveness) stableFor(now time.Time) time.Duration {
	if l.since.IsZero() {
		return 0
	}
	return now.Sub(l.since)
}

func (l *liveness) sample(rx uint64, now time.Time, grace time.Duration) {
	if rx <= l.rx {
		return
	}
	if !l.alive(now, grace) {
		l.since = now
	}
	l.rx = rx
	l.at = now
}

// wantSelector reports the tier's signature, empty when it must not run.
//
// The signature is a constant rather than a hash of the settings, and that is
// load-bearing: every consumer folds it into its own restart hash, so a
// signature that moved with the backup's port or the slate's colour would
// restart every destination on an edit that changes neither.
func wantSelector(s db.Settings) string {
	if !s.Failover.Enabled {
		return ""
	}
	return "on"
}

// upstreamSig is what a consumer of "the source" folds into its restart hash:
// the selector tier's signature when it is running, the silence tier's
// otherwise.
//
// With the selector running the silence signature drops out entirely, because a
// destination no longer reads the silence hub — the feed does. That is what
// makes an ingest gaining or losing audio a restart of one remux process rather
// than of every destination.
func upstreamSig(selSig, silenceSig string) string {
	if selSig != "" {
		return "selector:" + selSig
	}
	return silenceSig
}

// selectorHub is the tier's relay, or nil when the tier is not running.
func (e *Engine) selectorHub() *relay.Hub {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.sel == nil {
		return nil
	}
	return e.sel.hub
}

// downstreamHub is what sourceHub() would answer if it could see the selector.
//
// Every consumer inside this file names this instead of sourceHub(), so there
// is still exactly ONE decision about where "the source" is — it is spelled
// across two files only because the silence tier and the selector tier are
// stacked, and sourceHub() remains the answer for everything below the selector
// as well as for the whole pipeline when the tier is off.
func (e *Engine) downstreamHub() *relay.Hub {
	if h := e.selectorHub(); h != nil {
		return h
	}
	return e.sourceHub()
}

// sourceLabel distinguishes the hubs a consumer might be reading, for the
// restart hashes that have to notice a consumer moving between them.
func (e *Engine) sourceLabel() string {
	e.mu.RLock()
	sel := e.sel
	e.mu.RUnlock()
	if sel != nil && sel.hub != nil {
		return "selector:" + sel.spec
	}
	return e.silenceLabel()
}

// backupHub is the second listener's relay, or nil when there is none.
func (e *Engine) backupHub() *relay.Hub {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.backup == nil {
		return nil
	}
	return e.backup.hub
}

// playlistHub is the playlist tier's relay, or nil when there is none.
//
// Nil rather than a panic for the same reason backupHub answers nil: every
// caller asks BEFORE knowing whether the tier is running, and "no hub" is a
// normal answer for a feature that is off by default.
func (e *Engine) playlistHub() *relay.Hub {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.playlist == nil {
		return nil
	}
	return e.playlist.hub
}

// holdSilence freezes the silence tier's signature while the selector is
// standing in for a departed primary.
//
// wantSilence answers from the PROBE, and the probe goes blank a few seconds
// after the primary stops delivering. Read literally that would tear the
// silence tier down in the middle of a failover, change the signature every
// consumer was started with, and restart every destination — the exact failure
// the tier exists to prevent. So the last answer taken while the primary was
// the live source is held until it is the live source again.
func (e *Engine) holdSilence(fresh string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sel != nil && e.sel.hub != nil && e.sel.active != sourcePrimary {
		return e.heldSilenceSig
	}
	e.heldSilenceSig = fresh
	return fresh
}

// heldSilence is the signature the running primary feed was built against.
func (e *Engine) heldSilence() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.heldSilenceSig
}

// sourceChoice is everything the decision needs, gathered under the lock so the
// decision itself stays a pure function of a snapshot.
type sourceChoice struct {
	now     time.Time
	cur     sourceKind
	pinned  sourceKind
	primary liveness
	backup  liveness

	backupEnabled bool
	slateEnabled  bool
	// playlistRunning is whether the playlist is DELIVERING, which is the whole
	// of "would the playlist carry bytes if we switched to it".
	//
	// It is not "the tier is up". PlaylistFileProblem checks path confinement,
	// not existence, so a path safely inside the data directory that names no
	// file starts a supervised process which never opens an input -- running,
	// and permanently empty. Offering that as a candidate would rank a source
	// that cannot deliver above the slate, and the slate is the thing that
	// exists so an operator never sees nothing. So it is collapsed from the
	// playlist hub's own liveness, sampled from that hub's byte counter beside
	// the two ingests' -- see sampleSources, and the comment on liveness for why
	// process state answers a different question than failover has to ask.
	//
	// One bit, not a liveness: the decision asks nothing else of a playlist.
	// There is no automatic return TO a file and no stability window to measure,
	// so alive/not is everything, and it is collapsed by the caller under the
	// lock rather than looked up from inside the decision -- chooseSource has to
	// stay pure and cheap, because the golden table's only claim to being
	// exhaustive is that every input it branches on can be enumerated.
	playlistRunning bool
	grace           time.Duration
	autoReturn      bool
	returnStable    time.Duration
}

// candidate is one source the selector may choose, and whether it can be
// chosen right now.
//
// available is not "is the process running" — it is "would this deliver bytes
// if we switched to it". A slate is always available when enabled because it
// synthesises its own picture; an ingest is available only when it is actually
// delivering, which is the distinction the liveness type already draws and the
// reason the selector switches on delivery rather than on process state.
type candidate struct {
	kind      sourceKind
	available bool
	// rank orders the list. Lower is preferred. It is a field rather than the
	// slice position so a future caller can build the list in any order and
	// still get a stable decision.
	rank int
}

// candidatesFor turns a snapshot into the ordered list the ladder implies:
// primary, then backup, then the playlist, then slate.
//
// rank is assigned from position rather than written out, so this literal is
// the ONE place the preference order lives. Two spellings of the same ordering
// is how a reordering ends up half-applied — the list reads one way, the
// decision goes the other, and nothing fails until an outage.
func candidatesFor(c sourceChoice) []candidate {
	out := []candidate{
		{kind: sourcePrimary, available: c.primary.alive(c.now, c.grace)},
		// A backup that is delivering into a hub nobody enabled is still not a
		// place to send viewers, so the setting gates availability rather than
		// membership: the candidate stays in the list and stays unavailable.
		{kind: sourceBackup, available: c.backupEnabled && c.backup.alive(c.now, c.grace)},
		// THE PLAYLIST RANKS BELOW BOTH INGESTS, AND THAT IS A DECISION.
		//
		// A scheduled broadcast is a fallback for "nobody is streaming", not a
		// pre-emption of somebody who is. Put it above the primary and the
		// failure it buys is the one nobody forgives: a presenter live on air
		// is cut off mid-sentence because a playlist entry came due. An
		// operator who genuinely wants the playlist to win says so by pinning
		// it, and the pin path outranks this whole ladder already — so the
		// wanted behaviour costs nothing and the unwanted one cannot happen by
		// accident.
		//
		// It ranks ABOVE the slate for the mirror-image reason: both are
		// holding patterns, but one of them is programming somebody chose and
		// the other is a card saying the picture is missing.
		//
		// There is deliberately no `&& c.playlistEnabled` here to mirror the
		// backup's line above, and the asymmetry is the design rather than an
		// omission. The backup's hub is the listener's and outlives every
		// publisher, so its counter cannot tell enabled from disabled; the
		// playlist's hub belongs to the tier and dies with it, so
		// reconcilePlaylist zeroes this liveness as it tears the tier down --
		// see the comment there. That covers the untick a setting gate would
		// cover AND the three it would not: failover switched off entirely, a
		// file path that fails confinement, and a path edit that swaps one hub
		// for another. The invariant the two ends maintain between them: no
		// hub, no liveness, no candidate.
		{kind: sourcePlaylist, available: c.playlistRunning},
		// The slate has no liveness to check. It synthesises its own picture, so
		// "enabled" is the whole of "would this deliver bytes".
		{kind: sourceSlate, available: c.slateEnabled},
	}
	for i := range out {
		out[i].rank = i
	}
	return out
}

// chooseFrom is the ladder, expressed over a candidate list.
//
// Every branch below picks the best AVAILABLE candidate and then names why,
// which is the shape the ladder always had; the reasons are keyed by the kind
// that won because they are a contract. They reach an operator through
// Failover.Reason, so rewording one is a user-visible change.
//
// PRECONDITION: cands and c must describe the SAME INSTANT. This function has
// two sources of truth and reads both. Preference order and "can this deliver
// bytes right now" come from cands; everything else -- c.cur, c.pinned,
// c.autoReturn, c.returnStable, and the liveness histories behind
// c.primary/c.backup -- comes from c. Two rules read one of each in the same
// breath: the pin below asks available(c.pinned), and the flapping guard asks
// available(sourcePrimary) alongside c.primary.stableFor(c.now). Build cands
// from a snapshot fresher (or staler) than c and those rules straddle two
// different moments: the `>= returnStable` boundary would be evaluated against
// a primary the list never judged, so the selector could return to a primary
// the list calls dead, or refuse to return to one it calls alive. chooseSource
// keeps the two consistent by construction -- it derives cands from the same c
// it passes -- and that is exactly why the golden table cannot see a violation
// here. A future caller that assembles candidates itself owns this precondition.
//
// It also assumes the list is well formed: one candidate per kind, and no
// available sourceNone. best() panics on the ways that matter rather than
// deciding from a list that cannot mean what it says.
func chooseFrom(cands []candidate, c sourceChoice) (sourceKind, string) {
	ranked := slices.Clone(cands)
	slices.SortStableFunc(ranked, func(a, b candidate) int { return cmp.Compare(a.rank, b.rank) })

	// available answers about one named source, for the rules that are about a
	// particular source rather than about preference order.
	available := func(k sourceKind) bool {
		for _, cand := range ranked {
			if cand.kind == k {
				return cand.available
			}
		}
		return false
	}

	// best is the ladder itself: the first available candidate wins, and says
	// the sentence that goes with having won. fallbackKind is where the
	// selector parks when nothing at all is available — never sourceNone,
	// because handing the pipeline nothing is worse than every alternative.
	//
	// reasons is looked up with comma-ok rather than a plain index, and a miss
	// panics instead of returning "". A plain index is what this looked like
	// until a review of the candidate-list cutover: available(k) matches the
	// FIRST candidate of a kind in rank order, while best here matches the
	// FIRST AVAILABLE one, and those two agree only when the list has one
	// candidate per kind. candidatesFor always builds it that way today, but
	// nothing in the type checks that a future caller -- or a map literal
	// missing the entry for a kind Task 4 adds -- keeps it true. Get it wrong
	// and a plain index hands the operator a blank Failover.Reason on a real
	// switch, which reads as "nothing happened" when something did. A panic
	// naming the kind is a test failure at the point the list is built wrong,
	// which is the whole point of a candidate winning a reason nobody wrote.
	//
	// An available sourceNone is rejected the same way, and BEFORE the reason
	// lookup, because it is malformed for a different reason: sourceNone is the
	// absence of a source, not a source that can be on air, so a list offering
	// it as available says something that cannot be true. Returning it would be
	// worse than the blank reason above -- applySourceChoice's want ==
	// sourceNone guard would drop the decision, and a dropped decision is a
	// failover that silently did not happen. Today a "" kind happens to trip
	// the missing-reason panic below, because no branch registers a reason
	// under the empty key -- but only by accident, and a map literal that ever
	// gained a sourceNone entry would turn that accident into a silent
	// discard. This check does not read the reasons map, so no map can undo it.
	best := func(reasons map[sourceKind]string, fallbackKind sourceKind, fallbackReason string) (sourceKind, string) {
		for _, cand := range ranked {
			if cand.available {
				if cand.kind == sourceNone {
					panic("chooseFrom: the candidate list offers sourceNone as available, but sourceNone is the absence of a source and can never be put on air -- fix the list that was built")
				}
				reason, ok := reasons[cand.kind]
				if !ok {
					panic(fmt.Sprintf("chooseFrom: candidate %q won but this branch has no reason registered for it -- add one to the map", cand.kind))
				}
				return cand.kind, reason
			}
		}
		return fallbackKind, fallbackReason
	}

	// An operator's pin outranks the ladder, but only while the pinned source
	// is available: a pin that outlived its source would strand the broadcast
	// on a dead input.
	if reason, ok := pinReason(c.pinned); ok && available(c.pinned) {
		return c.pinned, reason
	}

	switch c.cur {
	case sourceBackup:
		if available(sourceBackup) {
			// The flapping guard. Manual is the default because an encoder that
			// dropped once usually drops again, and each automatic return is a
			// visible cut for every viewer.
			if available(sourcePrimary) && c.autoReturn && c.primary.stableFor(c.now) >= c.returnStable {
				return sourcePrimary, "the primary ingest has been delivering steadily again"
			}
			return sourceBackup, ""
		}
		// Manual return means "do not flap", not "never recover": with the
		// backup gone there is nothing to flap between. The backup is known
		// unavailable here, so it cannot win its own branch.
		return best(map[sourceKind]string{
			sourcePrimary:  "the backup ingest stopped delivering and the primary is back",
			sourcePlaylist: "neither ingest is delivering, so the playlist is on air",
			sourceSlate:    "neither ingest is delivering",
		}, sourcePrimary, "the backup ingest stopped delivering")

	case sourceSlate:
		// A slate is a holding pattern, never a destination. The return to a
		// real source is immediate and is NOT subject to the return mode: the
		// flap risk is already bounded by the grace period on the way out, and
		// sitting on a standby card while the show is back on air is the worse
		// failure by a wide margin. Staying put is silent; a slate that has been
		// switched off underneath us falls through to the primary.
		return best(map[sourceKind]string{
			sourcePrimary:  "the primary ingest is delivering again",
			sourceBackup:   "the backup ingest is delivering",
			sourcePlaylist: "the playlist is running",
			sourceSlate:    "",
		}, sourcePrimary, "the slate was switched off")

	case sourcePlaylist:
		// The playlist is a holding pattern too, so it leaves the same way the
		// slate does: the moment a real ingest is back, and without consulting
		// the return mode. The flap risk the return mode exists to bound is a
		// risk between two LIVE encoders; there is none here, because the
		// playlist never stops delivering and so can never hand the primary a
		// window to flap in.
		//
		// Staying put is silent, exactly as the slate branch is. Without that
		// empty string a selector already on the playlist would republish the
		// same reason on every 500ms sweep for the whole scheduled programme,
		// and an operator reading the log would find a failover storm where
		// nothing at all had moved. That is the trap a fourth kind walks into
		// by being added only to the maps of branches it can arrive in, and
		// never to a branch of its own.
		return best(map[sourceKind]string{
			sourcePrimary:  "the primary ingest is delivering again",
			sourceBackup:   "the backup ingest is delivering",
			sourcePlaylist: "",
			sourceSlate:    "the playlist stopped running",
		}, sourcePrimary, "the playlist stopped running")

	default:
		// Already on the primary means the primary winning is not news, so the
		// reason stays empty and nothing is logged or published.
		onPrimary := ""
		if c.cur != sourcePrimary {
			onPrimary = "the primary ingest is delivering"
		}
		// Nothing better exists, so stay parked on the primary rather than
		// switching to nothing: a feed that is merely waiting still holds its
		// place, and it starts carrying the stream the moment an encoder
		// arrives.
		noOther := ""
		if c.cur != sourcePrimary {
			noOther = "there is no other source to run"
		}
		return best(map[sourceKind]string{
			sourcePrimary:  onPrimary,
			sourceBackup:   "the primary ingest stopped delivering",
			sourcePlaylist: "the primary ingest stopped delivering and the playlist is running",
			sourceSlate:    "the primary ingest stopped delivering and no backup is on air",
		}, sourcePrimary, noOther)
	}
}

// pinReason is the sentence for an honoured manual override, and whether the
// kind is one an operator can pin at all. sourceNone is not: it is the absence
// of a pin, not a pin on nothing.
func pinReason(k sourceKind) (string, bool) {
	switch k {
	case sourcePrimary:
		return "an operator selected the primary ingest", true
	case sourceBackup:
		return "an operator selected the backup ingest", true
	// The pin is how an operator overrides the ranking decision candidatesFor
	// explains: the playlist loses to a live encoder on the ladder, and this is
	// the sentence that says somebody wanted it to win anyway. Still honoured
	// only while the playlist is actually running, like every other pin.
	case sourcePlaylist:
		return "an operator selected the playlist", true
	case sourceSlate:
		return "an operator selected the slate", true
	}
	return "", false
}

// chooseSource decides what should be feeding the selector, and says why.
//
// An empty reason means "no change"; a non-empty one is written to the log and
// published, because a failover nobody notices is how an operator discovers at
// the end of a broadcast that they streamed the backup all night.
func chooseSource(c sourceChoice) (sourceKind, string) {
	return chooseFrom(candidatesFor(c), c)
}

// reconcileSelector brings the tier, the backup listener and the feed into line
// with settings.
//
// Called from reconcileOutputs in the window where nothing downstream is
// reading anything: the hub it may create is the one every consumer below will
// subscribe to, and the hub it may close must not close under one.
func (e *Engine) reconcileSelector(s db.Settings, want, silenceSig string) {
	e.selMu.Lock()
	defer e.selMu.Unlock()

	e.mu.Lock()
	cur := e.sel
	e.mu.Unlock()

	// A tier whose hub failed to bind carries an empty signature, so it never
	// matches "on" and is retried on the next reconcile — and it is cleared
	// unconditionally when the feature is switched off, because a leftover
	// broken tier would go on refusing destinations that no longer need it.
	if cur != nil && cur.spec == want && want != "" {
		e.reconcileBackupIngest(s)
		e.reconcilePlaylist(s)
		// Ignored on purpose: a reconcile has no caller to fail to, and a
		// decision that could not be made has already logged itself. Holding
		// the current source is what this path wants anyway.
		_ = e.applySourceChoice(s, silenceSig, time.Now())
		return
	}

	if cur != nil {
		e.mu.Lock()
		e.sel = nil
		e.mu.Unlock()
		e.teardownFeed(cur.feed)
		if cur.hub != nil {
			_ = cur.hub.Close()
			e.log.Info("source selector stopped; destinations read the ingest directly again")
		}
	}
	if want == "" {
		e.reconcileBackupIngest(s)
		// Reached with the whole feature switched off, which is when the tier
		// most needs stopping: playlistSig is empty without Failover.Enabled, so
		// this is the call that takes the file off air with the selector.
		e.reconcilePlaylist(s)
		return
	}

	hub, err := relay.New(e.log, 0)
	if err != nil {
		// Recorded rather than returned, exactly as a rendition does: the
		// destinations downstream have to be told why they are not starting.
		e.mu.Lock()
		e.sel = &selector{spec: "", err: err.Error()}
		e.mu.Unlock()
		e.log.Error("start source selector", "err", err)
		return
	}

	now := time.Now()
	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		_ = hub.Close()
		return
	}
	e.sel = &selector{
		hub: hub, spec: want, startedAt: now, switchedAt: now,
		live: map[sourceKind]*liveness{
			sourcePrimary: {}, sourceBackup: {}, sourcePlaylist: {},
		},
	}
	e.mu.Unlock()

	e.log.Info("source selector started",
		"reason", "failover is enabled, so destinations subscribe to one stable relay for their whole life",
		"relayPort", hub.Port(),
		// THE TIER EPOCH, and it is here so two records can share an axis. Every
		// feed offset in the seam ledger is seconds since this instant, while the
		// recorded output's decode timestamps are seconds on the published
		// timeline and a log line's own timestamp is wall clock. Without the
		// origin written down once, a backward step in the mkv cannot be attributed
		// to a particular seam -- which is the gap that has made every occurrence
		// of #126 so far unusable as evidence.
		"tierEpoch", now.Format(time.RFC3339Nano),
		"tierEpochUnixMs", now.UnixMilli())

	e.reconcileBackupIngest(s)
	e.reconcilePlaylist(s)
	// Ignored for the same reason as the "already running" window above.
	_ = e.applySourceChoice(s, silenceSig, now)
}

// selectorProblem is the reason a destination cannot run that the selector is
// responsible for, or nil when it is not in the way.
//
// A feed that is down is deliberately NOT a reason: a destination started
// against a silent hub holds its platform connection and starts sending the
// moment the feed comes back, whereas one that refused to start has already
// lost the broadcast.
func (e *Engine) selectorProblem() error {
	e.mu.RLock()
	sel := e.sel
	e.mu.RUnlock()
	if sel == nil || sel.hub != nil {
		return nil
	}
	if sel.err != "" {
		return fmt.Errorf("the source selector failed to start: %s", sel.err)
	}
	return fmt.Errorf("the source selector is not running")
}

// selectorLoop is the failover detector: it samples each source's byte counter
// and switches the feed when the answer changes.
func (e *Engine) selectorLoop(ctx context.Context) {
	tick := time.NewTicker(selectorSweep)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			e.sweepSelector(time.Now())
		}
	}
}

func (e *Engine) sweepSelector(now time.Time) {
	s := e.Settings()

	e.selMu.Lock()
	defer e.selMu.Unlock()
	if e.selectorHub() == nil {
		return
	}
	e.sampleSources(s, now)
	// Ignored on purpose: the ticker has no caller to fail to. A decision that
	// could not be made holds the current source and logs, and the next tick
	// tries again 500ms later.
	_ = e.applySourceChoice(s, e.heldSilence(), now)
}

// sampleSources folds each candidate hub's byte counter into its liveness.
func (e *Engine) sampleSources(s db.Settings, now time.Time) {
	// The PRIMARY's own hub, never the selector's or the silence tier's: the
	// question is whether the operator's encoder is delivering, and the selector
	// hub carries bytes whichever source is on air.
	primaryRx := e.hub.RxBytes()
	var backupRx uint64
	if h := e.backupHub(); h != nil {
		backupRx = h.RxBytes()
	}
	// The playlist is sampled from bytes for the same reason the two ingests
	// are, and the failure it guards against is specific: PlaylistFileProblem
	// checks that the operator's path is CONFINED to the data directory, not
	// that it names a file. A path that is safely inside it and names nothing
	// passes validation, playlistSig is non-empty, the tier starts, and FFmpeg
	// then fails to open the input and backs off toward five seconds. "The tier
	// is running" is true throughout, and it is the wrong question -- exactly as
	// it is for an SRT listener that sits in "running" while it waits for a
	// publisher. Ranking is only worth anything if a candidate offered is a
	// candidate that would deliver, so what is sampled is delivery.
	var playlistRx uint64
	if h := e.playlistHub(); h != nil {
		playlistRx = h.RxBytes()
	}
	grace := failoverGrace(s)

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sel == nil {
		return
	}
	e.sel.live[sourcePrimary].sample(primaryRx, now, grace)
	e.sel.live[sourceBackup].sample(backupRx, now, grace)

	// The playlist's counter is the one that goes BACKWARDS, and it has to be
	// handled rather than fed to sample(), which takes a counter that only ever
	// rises and ignores anything at or below what it has seen. Two ordinary
	// things reset it: an operator editing the file path, which makes
	// reconcilePlaylist close this hub and build a fresh one counting from zero,
	// and disabling the tier, which leaves no hub to read at all. Fed in as-is,
	// the first would leave the liveness pinned at the OLD hub's total -- a
	// playlist that has been re-pointed at a working file reported dead until it
	// out-counts a file it no longer plays -- and the second would keep offering
	// a tier that has been torn down for a whole grace window, which startFeed
	// can only answer with "the playlist source has no relay to read". Zeroing
	// first makes both read as what they are: this is a different playlist, and
	// it has delivered nothing yet.
	//
	// reconcilePlaylist now zeroes the same liveness as it tears a tier down,
	// which is what makes the correction immediate rather than one sweep late,
	// so in practice this branch finds pl.rx already zero. It stays as the
	// second line of defence for the invariant, not as its enforcement: this is
	// the only place that reads the counter, and a counter read without a
	// rollback guard is how the first version of this went wrong.
	pl := e.sel.live[sourcePlaylist]
	if playlistRx < pl.rx {
		*pl = liveness{}
	}
	pl.sample(playlistRx, now, grace)
}

func failoverGrace(s db.Settings) time.Duration {
	if s.Failover.GraceSeconds <= 0 {
		return 5 * time.Second
	}
	return time.Duration(s.Failover.GraceSeconds) * time.Second
}

// errSelectorUndecided says the selector produced no source to put on air, so
// nothing was switched. It is what a recovered decision panic looks like from
// the outside: the three background callers ignore it and hold what they have,
// and SwitchSource turns it into a failure for the operator rather than
// reporting a switch that did not happen.
var errSelectorUndecided = errors.New("the source selector could not decide which source to put on air")

// applySourceChoice decides which source should be on air and makes it so. The
// caller must hold selMu.
//
// It returns errSelectorUndecided when the decision failed and the current
// source was held instead. Nothing was switched in that case, so a caller that
// asked for a specific source must not report success. The background callers
// -- the sweep ticker and both reconcile windows -- have nobody to report to
// and deliberately ignore it: holding is already the behaviour they want, and
// making them start failing would turn a decision bug into a reconcile outage.
func (e *Engine) applySourceChoice(s db.Settings, silenceSig string, now time.Time) error {
	e.mu.Lock()
	sel := e.sel
	if sel == nil || sel.hub == nil {
		e.mu.Unlock()
		return nil
	}
	c := sourceChoice{
		now:           now,
		cur:           sel.active,
		pinned:        sel.pinned,
		primary:       *sel.live[sourcePrimary],
		backup:        *sel.live[sourceBackup],
		backupEnabled: s.Failover.Backup.Enabled,
		slateEnabled:  s.Failover.Slate.Enabled,
		// Collapsed from the playlist hub's liveness, NOT from "e.playlist !=
		// nil". A tier can be up and its hub empty forever -- see the field's
		// comment on sourceChoice -- and a candidate offered on process state
		// would be a candidate that cannot deliver. Read here under the same
		// lock as the primary's, from the sample sweepSelector took a moment
		// ago, so the decision still sees one consistent snapshot.
		playlistRunning: sel.live[sourcePlaylist].alive(now, failoverGrace(s)),
		grace:           failoverGrace(s),
		autoReturn:      s.Failover.Return == db.FailoverReturnAuto,
		returnStable:    time.Duration(s.Failover.ReturnStableSeconds) * time.Second,
	}
	e.mu.Unlock()

	want, reason, decided := e.decideSource(c)
	// want == sourceNone can ONLY happen here on a recovered panic with
	// nothing to hold: chooseFrom's own best() never returns sourceNone on a
	// real decision (its fallback is always sourcePrimary -- see the comment
	// on candidatesFor), so decideSource's recover is the only path that can
	// hand this back, and only when c.cur was itself sourceNone -- the
	// selector's first decision, before any feed has ever run, or the first
	// one after the tier restarts. There is no current source to hold, so
	// there is nothing to do, and this returns rather than calling
	// ensureFeed(sourceNone).
	//
	// ensureFeed would now refuse that kind itself -- errNoFeedShape lists the
	// kinds positively and sourceNone is not among them -- but this guard is
	// still the one that belongs here, and it is not redundant: refusing inside
	// ensureFeed would record an error on the tier describing a missing feed
	// shape, when the true story is that the decision itself failed and there
	// was nothing to hold. The two are logged apart on purpose below. Before
	// either guard existed this started a PRIMARY-shaped feed while
	// e.sel.active stayed recorded as none, because every function that builds
	// a feed treated an unrecognised kind as the primary.
	//
	// It used to return here in silence, which was the one thing worse than the
	// blank reason this whole file exists to prevent: a decision that reached
	// no feed and left no trace. The two ways of getting here are logged apart
	// on purpose, and neither repeats the panic itself -- decideSource has
	// already named the cause, and once a minute its stack -- so this line adds
	// only what that one cannot know, which is that nothing is on air.
	if want == sourceNone {
		if decided {
			// Unreachable today and a bug if it ever happens: a real decision
			// never yields sourceNone. best() panics on an available one and
			// every fallback is sourcePrimary, which is why all 3200 rows of
			// the frozen table decide primary, backup, playlist or slate and
			// none ever decides none.
			e.log.Error("selector decided on no source at all; no feed started",
				"source", e.sourceID, "cause", "the candidate list produced sourceNone from a decision that did not panic")
		} else {
			e.log.Error("selector has no current source to hold; no feed started",
				"source", e.sourceID, "cause", "the decision panicked before any feed had ever run, so there was nothing to hold")
		}
		return errSelectorUndecided
	}
	e.ensureFeed(s, silenceSig, want, reason, now)
	if !decided {
		return errSelectorUndecided
	}
	return nil
}

// selPanicRelogDefault bounds how often a PERSISTENT decision panic re-logs
// its full stack, for every Engine that leaves its own selPanicRelog field at
// the zero value -- which is every production Engine, since only a test ever
// sets that field (see its comment on the Engine struct for why the window
// lives there, per-instance, rather than in a package-level var). The
// underlying cause (a map entry missing a reason) does not change between
// sweeps, so the tenth identical stack trace in five seconds tells an
// operator nothing the first one didn't -- it only spends CPU walking the
// stack and log volume repeating it, on the same 500ms ticker that is also
// the thing hammering the panic.
//
// It is a window since the last STACK WAS LOGGED, not since the last panic --
// see decideSource. Measured the other way it would never elapse, and "bounds
// how often" would quietly mean "logs it once".
const selPanicRelogDefault = time.Minute

// decideSource is chooseSource with a recover around it, and it is the ONE
// place that recover needs to live: chooseSource has exactly one caller
// (here), and this is in turn called from every production path that can
// reach the selector's decision -- both windows of reconcileSelector, the
// operator's SwitchSource, and the sweep's ticker. Recovering here, rather
// than separately in each of those four, is what keeps a fix from covering
// three of them and leaving the fourth fatal.
//
// It is placed at the DECISION, not around applySourceChoice's ensureFeed
// call that follows it. applySourceChoice is the function that performs a
// switch -- teardownFeed, then startFeed, then only after both succeed does
// it update e.sel.feed/active -- so recovering across that whole sequence
// could catch a panic mid-switch and leave e.sel's bookkeeping pointing at a
// feed that teardownFeed already stopped: a half-switched state that is worse
// than the panic it was hiding. chooseSource runs entirely before any of that
// teardown or start begins, so a panic here can only ever mean "the decision
// itself is broken", never "the switch got halfway done". Recovering at
// exactly this boundary makes surviving it safe BY CONSTRUCTION rather than
// by care taken in ensureFeed.
//
// On a recovered panic this holds the current source with no reason AND
// reports decided=false, which is the least action available: not "switch to
// whatever best() almost
// picked" (that candidate had no explanation, and this file exists precisely
// so an unexplained switch is never shown to an operator as one), and not
// "crash" either. The invariant chooseFrom's best() leans on -- one candidate
// per kind -- is enforced by candidatesFor's convention, not by the compiler,
// so a change that breaks it (Task 4 adds a fourth candidate kind) reaches
// this on every reconcile a settings change triggers, not only on a tick.
//
// "Hold the current source" only means something when there is one. On the
// selector's first decision -- c.cur is sourceNone, no feed has ever run --
// there is nothing to hold, and the caller must not treat sourceNone as a
// destination: this was considered, not missed, and applySourceChoice is
// where it is handled, by skipping ensureFeed entirely rather than starting
// a feed the bookkeeping cannot describe. See the comment there.
//
// decided is how the recovery leaves the building. Holding the current source
// is the right BEHAVIOUR for the three callers that have nobody to report to
// -- the ticker and both reconcile windows just carry on -- but it is a lie to
// the one that does: SwitchSource would otherwise return nil to an operator
// whose source was never put on air, with the log saying it was and the pin
// LATCHED, so every later sweep re-panics on the same input forever. A
// substituted value cannot be told apart from a real one; a separate flag can.
//
// Deliberately NOT inside chooseFrom, chooseSource or best: those are called
// directly by selector_candidates_test.go and selector_golden_test.go, and a
// recover placed there would swallow the panic those tests exist to observe.
func (e *Engine) decideSource(c sourceChoice) (kind sourceKind, reason string, decided bool) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		msg := fmt.Sprint(r)
		window := e.selPanicRelog
		if window == 0 {
			window = selPanicRelogDefault
		}
		// The window is measured from the last STACK, not from the last panic.
		// Refreshing selPanicAt on every panic would restart the clock twice a
		// second, time.Since would never reach the window, and the full
		// stack would be logged once EVER rather than once a minute -- so an
		// operator opening the logs an hour into an incident would find only
		// stackless lines and a first stack that had long since rotated out.
		fresh := msg != e.selPanicMsg || time.Since(e.selPanicAt) > window
		if fresh {
			e.selPanicMsg, e.selPanicAt = msg, time.Now()
			e.log.Error("selector decision panicked; holding the current source",
				"source", e.sourceID, "panic", msg, "stack", string(debug.Stack()))
		} else {
			e.log.Error("selector decision panicked again; holding the current source",
				"source", e.sourceID, "panic", msg)
		}
		kind, reason, decided = c.cur, "", false
	}()
	// choose is chooseSource unless a test has substituted decideFn -- see
	// its comment on the Engine struct. Indirecting through a local rather
	// than branching inline keeps this identical to calling chooseSource
	// directly when decideFn is nil, which is every production call.
	choose := chooseSource
	if e.decideFn != nil {
		choose = e.decideFn
	}
	kind, reason = choose(c)
	return kind, reason, true
}

// holdForUnfeedableKind records a decision the feed layer cannot carry out and
// leaves everything else exactly as it was.
//
// It logs once per distinct cause rather than on every 500ms sweep. The cause
// cannot change between sweeps -- it is a missing case in the source, not a
// condition that clears -- so the hundredth identical line tells an operator
// nothing the first did, while the sweep producing it is the same one already
// hammering the fault. sel.err is the dedupe key AND the operator-visible
// record, which is what keeps the quiet sweeps from being silent ones: the
// tier goes on reporting the fault through the API for as long as it lasts.
func (e *Engine) holdForUnfeedableKind(want sourceKind, err error) {
	e.mu.Lock()
	repeat := e.sel != nil && e.sel.err == err.Error()
	if e.sel != nil {
		e.sel.err = err.Error()
	}
	e.mu.Unlock()
	if repeat {
		return
	}
	e.log.Error("the selector chose a source no feed can run; holding the current feed",
		"source", e.sourceID, "want", string(want), "err", err)
}

// ensureFeed starts, replaces or leaves the feed alone. The caller must hold
// selMu.
func (e *Engine) ensureFeed(s db.Settings, silenceSig string, want sourceKind, reason string, now time.Time) {
	// The boundary for the three functions below, and the reason none of their
	// loud failures reaches a production crash: a kind the feed layer cannot
	// build is refused HERE, before anything is torn down. The running feed
	// keeps running, active and reason are left alone, and the tier records why
	// -- which is the same "hold what you have and say so" the recovered
	// decision panic settles on, for the same reason. Starting nothing is
	// better than starting the primary under another name.
	if err := errNoFeedShape(want); err != nil {
		e.holdForUnfeedableKind(want, err)
		return
	}
	upstream := e.feedUpstreamSig(s, want, silenceSig)

	e.mu.Lock()
	sel := e.sel
	if sel == nil || sel.hub == nil {
		e.mu.Unlock()
		return
	}
	cur, active, lastAt := sel.feed, sel.active, sel.feedAt
	e.mu.Unlock()

	switch {
	case cur != nil && cur.kind == want && cur.upstream == upstream:
		if feedRunning(cur) {
			return
		}
		if now.Sub(lastAt) < feedRespawn {
			return
		}
	case cur == nil && !lastAt.IsZero() && now.Sub(lastAt) < feedRespawn:
		// A start that failed backs off rather than being retried on every
		// sweep, which is what keeps a slate with an unopenable encoder from
		// spawning twice a second forever.
		return
	}
	respawn := active == want

	teardownFrom := time.Now()
	stopErr := e.teardownFeed(cur)
	teardownMs := float64(time.Since(teardownFrom).Microseconds()) / 1000

	// THE OFFSET TAKES THE DECISION TIME, AND A MEASUREMENT SAYS SO.
	//
	// This block used to take time.Now() here, after the teardown, on the
	// argument that teardownFeed blocks for as long as the outgoing process
	// takes to exit, so a time captured before it would start the incoming feed
	// behind where the outgoing one's timestamps had reached. That argument reads
	// well and is wrong: measured over twelve runs of the failover suite per
	// configuration, the post-teardown time produced a backwards DTS at the seam
	// in 3 of 12, and the decision time in 0 of 12. The same 0 of 12 holds on the
	// commit before the change was made.
	//
	// The mechanism is not yet established -- see issue #126. What IS established
	// is the direction, and it is the opposite of the reasoning that was here.
	// The bug the old comment described was never observed; it was derived from
	// reading, and fixing it caused the failure it claimed to prevent.
	//
	// So: do not "fix" this again from first principles. Change it only with a
	// twelve-run failover measurement on both sides of the change.
	//
	// startedAt is still read, because feedAt genuinely does want the later time:
	// it is what feedRespawn measures against, and a pre-teardown value means the
	// backoff has already expired when the feed starts, so a feed that fails to
	// start respawns on every 500ms sweep. That half was measured innocent --
	// 0 of 12 with it kept and the offset reverted.
	startedAt := time.Now()
	feed := e.startFeed(s, want, upstream, silenceSig, now)
	e.logSeam(cur, feed, reason, teardownMs, stopErr)

	e.mu.Lock()
	if e.sel != nil {
		e.sel.feed = feed
		e.sel.active = want
		e.sel.feedAt = startedAt
		if feed != nil {
			e.sel.err = ""
		}
		if !respawn {
			e.sel.reason = reason
			e.sel.switchedAt = now
			e.sel.switches++
		}
	}
	e.mu.Unlock()

	if respawn {
		// Not a switch, but never silent either: a feed that keeps dying is the
		// difference between a broadcast that is on air and one that only looks
		// like it.
		e.log.Warn("source feed restarted", "source", string(want))
		e.publishStatus()
		return
	}
	if reason == "" {
		return
	}
	// Three ways to notice, because a silent failover is the failure this
	// feature is judged on: a log line, the status snapshot, and an event.
	if want == sourcePrimary {
		e.log.Info("source switched", "to", string(want), "reason", reason)
	} else {
		e.log.Warn("source switched", "to", string(want), "reason", reason)
	}
	e.bus.Publish(eventFailover, e.Failover())
	e.publishStatus()
}

// logSeam writes the one line per handover that issue #126 is missing.
//
// THE SEAM IS WHERE THE TIMELINE IS JOINED AND NOTHING RECORDED THE JOIN. The
// bug is a backwards decode timestamp at a switch, seen in 3 runs of 12 under
// one timing change and 0 of 12 under another, and every occurrence so far has
// been a single number in an acceptance script's output -- which cannot say
// WHICH switch produced it, how far the outgoing feed's timeline had actually
// reached, or how long the teardown took. Those three facts are what separate
// the surviving explanations, so they are written down at the moment they are
// known and nowhere else can know them.
//
// predictedStepMs is the falsifiable part, and it is a PREDICTION rather than a
// measurement: it is where the outgoing feed's last published timestamp is
// believed to be (its offset plus how far FFmpeg said it had got) minus where
// the incoming feed's timeline starts. The leading hypothesis for #126 is that
// the copy hop, which has no -re where the slate has one, publishes as fast as
// bytes arrive and so runs AHEAD of the tier clock its offset was computed from.
// Measured offline at about 30x realtime given a burst; where a burst would come
// from in production is NOT established, because relay.Hub is a pure fanout and
// queues nothing, so it would have to be a socket buffer or FFmpeg's own fifo.
// If the hypothesis is right this number is positive at a seam that produces a
// backwards step, and a failing run with predictedStepMs near zero kills it
// outright.
//
// It rests on one assumption -- that FFmpeg's out_time does not already include
// -output_ts_offset -- which is exactly what relayfeed_offset_integration_test.go
// measures offline rather than argues about. The three inputs are logged
// separately as well as combined, so a reader who finds the assumption wrong can
// recompute the prediction from the same line without rerunning anything.
//
// WHAT predictedStepMs WAS MEASURED TO BE, AND IT IS NOT A STEP. Read the
// hypothesis above as history. out_time counts the media a feed has produced
// since its own first output -- measured in
// TestProgressOutTimeCountsMediaProducedNotPositionOnThePublishedTimeline -- and
// both offsets are tier-clock stamps taken at a switch DECISION. So
// `(out.offset + out_time) - in.offset` is, to within two correction terms,
// MINUS THE OUTGOING FEED'S START LAG: the wall time between its offset being
// stamped and its first output, which for a feed that follows a teardown is the
// teardown. That is why 91% of 113 recorded seams carry a negative prediction
// while 112 of them produced no step at all, and why the seam whose predecessor
// logged an 8002ms teardown carries predictedStepMs = -8586ms against a
// delivered step of -30ms.
//
// The step a destination actually sees is
//
//	(out.offset + C_out + out_time) - (in.offset + C_in)
//
// where C_k is a feed's own start constant, the first value on its input
// timeline, which -output_ts_offset ADDS to. The ledger drops both C terms. They
// cancel only when the two feeds read the same stream from the same point, which
// at a real seam they never do; measured spread between feeds is about 1.15s
// against a 1ms detector threshold. Nothing FFmpeg reports through -progress
// carries C, so this line cannot be repaired into a step predictor by arithmetic
// alone. Do not try; two fixes derived from reading rather than measuring have
// already been refuted here. See #126.
//
// Info rather than Debug on purpose: the acceptance suite runs the server at
// -log info, and turning it up to debug to see this would also turn on the
// per-packet churn logging, which changes the timing of the very thing being
// measured. One line per switch is roughly five lines per run.
//
// Called ONLY from ensureFeed, immediately after the replacement is started.
// Anywhere earlier and the incoming feed has no offset yet; anywhere later and
// the outgoing process has been collected.
func (e *Engine) logSeam(out, in *sourceFeed, reason string, teardownMs float64, stopErr error) {
	// No outgoing feed is not a seam -- there is no timeline to join to -- and a
	// line for it would put rows in the ledger the script would have to filter
	// back out.
	if out == nil {
		return
	}

	var outTimeMs int64
	var progressDone bool
	if out.proc != nil {
		// The final -progress block, still there because runOnce clears progress
		// when it STARTS a child and never on the way out, and Stop waits for the
		// stdout parser to drain before it returns. A child killed on the deadline
		// may not have emitted one; progressDone says which case this is rather
		// than leaving a zero to be misread as a feed that published nothing.
		st := out.proc.Status()
		outTimeMs, progressDone = st.Progress.OutTimeMS, st.Progress.Done
	}

	// A SWITCH WITH NO INCOMING FEED IS A DIFFERENT RECORD, and it gets a
	// different message rather than sentinel values in the same one. The script
	// buckets backward steps by each seam's inOffset, so a row carrying a
	// stand-in for "there is no incoming timeline" would sort before every real
	// seam and silently collect steps belonging to all of them. Warn, because a
	// teardown that was not followed by a start is a tier with nothing on air.
	if in == nil {
		e.log.Warn("feed seam incomplete: the outgoing feed was stopped and the replacement did not start",
			"outGen", out.gen, "outKind", string(out.kind), "outOffset", out.offset,
			"outTimeMs", outTimeMs, "outProgressDone", progressDone,
			"teardownMs", teardownMs, "stopDeadline", errors.Is(stopErr, supervisor.ErrStopDeadline),
			"reason", reason)
		return
	}

	predicted := (out.offset + float64(outTimeMs)/1000 - in.offset) * 1000

	e.log.Info("feed seam",
		"outGen", out.gen, "outKind", string(out.kind), "outOffset", out.offset,
		"outTimeMs", outTimeMs, "outProgressDone", progressDone,
		"teardownMs", teardownMs, "stopDeadline", errors.Is(stopErr, supervisor.ErrStopDeadline),
		"inGen", in.gen, "inKind", string(in.kind), "inOffset", in.offset,
		"predictedStepMs", predicted, "reason", reason)
}

// feedRunning reports whether the feed's process is still up. A feed is not
// AutoRestart, so a process that has exited stays exited until the sweep
// rebuilds it with a current timestamp offset.
func feedRunning(f *sourceFeed) bool {
	if f == nil || f.proc == nil {
		return false
	}
	switch f.proc.Status().State {
	case supervisor.StateStopped, supervisor.StateFailed:
		return false
	}
	return true
}

// errNoFeedShape names a source kind that the feed layer does not know how to
// run, and it exists because "does not know how to run it" used to be spelled
// as silence.
//
// THREE functions decide what a feed actually is -- feedUpstreamSig hashes what
// its command line depends on, startFeed builds that command line, and
// downstreamFeedInput picks the hub it reads -- and until this commit ALL THREE
// treated an unrecognised kind as the primary. A kind added to the ladder and
// missed in any one of them therefore produced a running process reading the
// PRIMARY's hub while sel.active recorded the new kind and Failover.Reason told
// the operator that new source was on air. Bookkeeping and process disagreeing,
// with no error, no panic and no test failure -- and the selector's panic
// recovery never fires, because nothing panicked.
//
// This is the same class of defect best() already refuses in chooseFrom (a
// winning candidate with no reason registered), caught the same way and for the
// same reason: a kind that reaches a place nobody taught about it must fail
// where it is noticed, not produce a plausible-looking wrong answer.
//
// The kinds are listed positively. A future kind is then a case that is
// VISIBLY absent here rather than one silently absorbed by a default.
func errNoFeedShape(kind sourceKind) error {
	switch kind {
	case sourcePrimary, sourceBackup, sourceSlate, sourcePlaylist:
		return nil
	}
	// The three sites are named in the message rather than in a comment, because
	// the reader of this line is somebody who has just added a FIFTH kind and is
	// looking at a failure they did not expect. Teaching two of the three and
	// missing the last is the mistake this whole guard exists for, and it is the
	// one a message that merely said "unknown source" would let them repeat.
	return fmt.Errorf("no feed knows how to run source %q: feedUpstreamSig, startFeed and "+
		"downstreamFeedInput each need a case for it before the selector is ever allowed "+
		"to offer it as a candidate", kind)
}

// feedUpstreamSig hashes what one feed's command line depends on, so a settings
// change respawns the feed and disturbs nothing downstream of it.
//
// It panics on a kind with no feed shape rather than hashing one. It cannot
// return an error -- its result is a hash folded into a respawn decision, and
// there is no value it could return that means "refuse" -- so the loud failure
// is the only honest one available here. ensureFeed rejects such a kind before
// this is ever reached, which is what keeps the panic off every production
// path; see the guard there.
func (e *Engine) feedUpstreamSig(s db.Settings, kind sourceKind, silenceSig string) string {
	if err := errNoFeedShape(kind); err != nil {
		panic("feedUpstreamSig: " + err.Error())
	}
	switch kind {
	case sourceBackup:
		return hashStrings([]string{"backup", backupIngestSig(s, e.ingestToken())})
	case sourcePlaylist:
		// playlistSig, exactly as the backup folds in backupIngestSig: the feed
		// here is only a copy hop out of the playlist's hub, but that hub is
		// CLOSED and rebuilt whenever reconcilePlaylist sees the signature move
		// -- an operator editing the file path is the ordinary case -- and a
		// copy hop left pointing at the old relay would spin on a port nothing
		// publishes to. Folding the same signature in is what makes the feed
		// respawn onto the new hub in the same sweep.
		return hashStrings([]string{"playlist", playlistSig(s)})
	case sourceSlate:
		e.mu.RLock()
		v := e.videoInfo
		e.mu.RUnlock()
		sl := s.Failover.Slate
		parts := []string{
			"slate", sl.ImagePath, sl.Color, strconv.Itoa(sl.VideoKbps),
			string(sl.Encoder), sl.Preset,
		}
		if v != nil {
			parts = append(parts, strconv.Itoa(v.Width), strconv.Itoa(v.Height),
				strconv.FormatFloat(v.FrameRate, 'g', -1, 64))
		}
		return hashStrings(parts)
	default:
		// sourcePrimary, and nothing else: errNoFeedShape above has already
		// turned every other kind away. Left as a default rather than written
		// as `case sourcePrimary` so the compiler still sees a total function,
		// but the set it stands for is now one kind wide instead of open.
		return primaryFeedSig(silenceSig, e.sourceHubPort())
	}
}

// primaryFeedSig is the primary feed's upstream signature. The silence tier is
// between the ingest and this feed, so the feed has to be rebuilt onto the
// tier's hub when one appears and back off it when it goes.
//
// Two ingredients, and the second is the fix for a feed that ran forever
// carrying nothing.
//
// silenceSig is what the tier is SUPPOSED to be, derived from settings. port is
// what the feed is actually reading. In the steady state they agree and the
// port contributes nothing. They come apart in one window: reconcileOutputs
// calls detachFeedForSilence, which drops selMu, and only then reconcileSilence
// swaps the hub while holding e.mu alone. A 500ms selector sweep landing in
// that gap computed the NEW silenceSig -- it comes from settings, which have
// already changed -- while downstreamFeedInput handed it the OLD hub, and
// detachFeedForSilence had just zeroed feedAt so the respawn backoff did not
// stop it. The feed started, tagged with a signature that matched what
// reconcileSelector was about to ask for.
//
// So reconcileSelector then found cur.upstream == want and a running process,
// and left it alone. Permanently: the selector's hub carried zero bytes, every
// destination reported running, nothing published, and no error was raised
// anywhere, because from each layer's own point of view nothing had failed.
//
// Folding the port in makes the signature describe the hub the feed IS reading
// rather than the one it was meant to. A feed left on a closed tier can no
// longer match, so the next sweep rebuilds it. This is the same reasoning the
// playlist case already applies to playlistSig, one level more literal.
func primaryFeedSig(silenceSig string, port int) string {
	return hashStrings([]string{"primary", silenceSig, strconv.Itoa(port)})
}

// sourceHubPort is the port of the relay the primary feed reads: the silence
// tier's when one is up, the ingest's otherwise. Zero when neither exists.
func (e *Engine) sourceHubPort() int {
	if h := e.sourceHub(); h != nil {
		return h.Port()
	}
	return 0
}

// detachFeedForSilence stops the primary feed when the silence tier under it is
// about to be replaced.
//
// reconcileSilence closes that tier's hub, and the feed is the only thing still
// subscribed to it. Stopped here, it is rebuilt onto the new hub by
// reconcileSelector a few lines later, and the destinations ride out the same
// pause in datagrams they already survive on every rendition restart.
func (e *Engine) detachFeedForSilence(silenceSig string) {
	e.selMu.Lock()
	defer e.selMu.Unlock()

	// The port is the CURRENT one, which is the tier about to be closed. That
	// is deliberate: this compares the feed against what it would be if only
	// the settings had moved, so a feed already on the right tier is left
	// alone and one facing a tier that is being replaced is torn down.
	want := primaryFeedSig(silenceSig, e.sourceHubPort())
	e.mu.Lock()
	var feed *sourceFeed
	if e.sel != nil && e.sel.feed != nil &&
		e.sel.feed.kind == sourcePrimary && e.sel.feed.upstream != want {
		feed, e.sel.feed = e.sel.feed, nil
		// Cleared, or the failed-start backoff would read this deliberate
		// teardown as a feed that cannot start and leave the tier unfed for a
		// couple of seconds.
		e.sel.feedAt = time.Time{}
	}
	e.mu.Unlock()
	e.teardownFeed(feed)
}

// startFeed spawns the process that publishes one source into the selector's
// hub. The caller must hold selMu.
func (e *Engine) startFeed(s db.Settings, kind sourceKind, upstream, silenceSig string, now time.Time) *sourceFeed {
	fail := func(err error) *sourceFeed {
		e.mu.Lock()
		if e.sel != nil {
			e.sel.err = err.Error()
		}
		e.mu.Unlock()
		e.log.Error("start source feed", "source", string(kind), "err", err)
		return nil
	}

	e.mu.RLock()
	sel := e.sel
	e.mu.RUnlock()
	if sel == nil || sel.hub == nil {
		return nil
	}
	out := sel.hub.InputURL()
	// The tier's own elapsed time. See the PTS note at the top of this section:
	// this single number is what keeps the published timeline monotonic across
	// every switch and every respawn.
	offset := now.Sub(sel.startedAt).Seconds()
	if offset < 0 {
		offset = 0
	}

	feed := &sourceFeed{kind: kind, gen: e.feedGen.Add(1), upstream: upstream, offset: offset, startedAt: now}
	var args []string

	// Switched on the kind exhaustively rather than "slate, or else the primary
	// shape". The else was the mechanism: an unrecognised kind fell into the
	// relay branch, downstreamFeedInput handed it the primary's hub, and a
	// process started that read the primary while calling itself something
	// else. This function can report a failure -- fail() records it on the tier
	// and logs it -- so an unfeedable kind is returned as an error rather than
	// as a panic.
	switch kind {
	case sourceSlate:
		spec, encFallback := e.slateSpec(s, out, offset)
		if encFallback != "" {
			e.log.Warn("slate encoder unusable; falling back to software",
				"encoder", string(s.Failover.Slate.Encoder), "reason", encFallback)
		}
		args = ffmpeg.SlateArgs(spec)
	case sourcePrimary, sourceBackup, sourcePlaylist:
		// The playlist joins the two ingests here rather than getting a branch
		// of its own, and that is the whole shape of the tier: it already runs
		// its own FFmpeg against the file and publishes into its own hub, so
		// what the selector needs is the identical copy hop the backup gets --
		// subscribe to a relay, republish into the selector's hub with the
		// tier's timestamp offset. Re-reading the file here instead would put a
		// second decoder on the same input and reset its timeline on every
		// switch, which is the thing the offset exists to prevent.
		in := e.downstreamFeedInput(kind)
		if in == nil {
			return fail(fmt.Errorf("the %s source has no relay to read", kind))
		}
		port, err := e.alloc.Allocate()
		if err != nil {
			return fail(err)
		}
		feed.in, feed.port, feed.subName = in, port, selectorSubName
		args = relayFeedArgs(in.Subscribe(selectorSubName, port), out, offset)
	default:
		// A fifth kind lands here, and lands here VISIBLY: nothing is started
		// and nothing is recorded as active, because a feed that cannot be built
		// must not leave a process behind that pretends it was.
		return fail(errNoFeedShape(kind))
	}

	feed.proc = supervisor.New(e.log, supervisor.Spec{
		Name: "source:" + string(kind), Kind: "source", Bin: e.tools.FFmpeg, Args: args,
		// Deliberately not AutoRestart: a respawn has to be rebuilt with a
		// current timestamp offset, so the sweep owns it.
		AutoRestart: false, OnLog: e.onLog, OnState: e.onState, LogSink: logSink{e},
	})

	e.mu.Lock()
	// Shutdown may have run since this started; publishing under the same lock
	// Stop collects processes with is what keeps a late start from becoming an
	// orphan holding a UDP socket.
	if e.stopped {
		e.mu.Unlock()
		e.teardownFeed(feed)
		return nil
	}
	e.mu.Unlock()

	feed.proc.Start()
	return feed
}

// downstreamFeedInput is the hub one copy-hop feed reads.
//
// This is the function that actually made a mislabelled feed primary-shaped: it
// was `if kind == sourceBackup` and everything else fell through to
// sourceHub(). A kind nobody taught it about got the primary's bytes and no
// complaint. It is now a closed switch, and startFeed calls it only for the two
// kinds that HAVE a hub, so the panic below is a "this cannot happen" marker
// rather than a path -- the same division of labour as chooseFrom's best(),
// which panics while decideSource holds the recovery.
func (e *Engine) downstreamFeedInput(kind sourceKind) *relay.Hub {
	switch kind {
	case sourceBackup:
		return e.backupHub()
	case sourcePlaylist:
		// The playlist's OWN hub, and naming it here is the point of the tier
		// having one. Handed the primary's hub instead -- which is what the old
		// fall-through did -- a file on air would have carried bytes onto the
		// primary's relay, the primary would have read live, and failover never
		// switches away from a live primary. The feature would have disabled
		// itself the first time it was used, silently.
		return e.playlistHub()
	case sourcePrimary:
		// sourceHub(), not e.hub: with a video-only primary the silence tier is
		// between the two, and feeding the selector from the raw ingest would
		// publish a stream with no audio track at all.
		return e.sourceHub()
	}
	panic(fmt.Sprintf("downstreamFeedInput: source %q has no hub to read -- only the primary, "+
		"the backup and the playlist do, and startFeed must not ask about any other kind", kind))
}

// teardownFeed stops one feed and gives back everything it held.
//
// RETURNS THE STOP ERROR as well as logging it, because ensureFeed needs the
// same fact in a second place: the seam ledger it writes for #126 has to record
// whether the outgoing feed exited or was killed on the deadline, and a switch
// where the old process may still have been writing is not comparable with one
// where it definitely was not. Every other caller ignores it -- a shutdown path
// has nothing better to do with it -- which is why this reports rather than
// refusing to proceed.
func (e *Engine) teardownFeed(f *sourceFeed) error {
	if f == nil {
		return nil
	}
	var stopErr error
	if f.proc != nil {
		ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		err := f.proc.Stop(ctx)
		cancel()
		stopErr = err
		// A KILLED CHILD IS NOT A STOPPED ONE, and this is the caller that has
		// to care. ensureFeed starts the replacement into the same hub the
		// moment this returns, so a feed that ignored SIGTERM for twelve
		// seconds and was killed may still be writing when the new one begins:
		// two publishers on one input, which is a corrupted timeline rather
		// than a missing one.
		//
		// Reported rather than obeyed. Refusing to start the replacement would
		// leave the tier with no feed at all, which is worse than a seam, and
		// waiting longer would hold the switch open past the deadline that was
		// already chosen. So the switch proceeds and the operator is told --
		// this is the "degrade visibly rather than corrupt quietly" rule the
		// slate fallback follows, applied to the case where the corruption is
		// possible rather than certain.
		if err != nil {
			e.log.Error("outgoing feed did not exit before the deadline; the "+
				"replacement starts while it may still be writing",
				"source", string(f.kind), "err", err)
			e.mu.Lock()
			if e.sel != nil {
				e.sel.err = err.Error()
			}
			e.mu.Unlock()
		}
	}
	if f.subName != "" && f.in != nil {
		f.in.Unsubscribe(f.subName)
	}
	if f.port != 0 {
		e.alloc.Release(f.port)
	}
	return stopErr
}

// relayFeedArgs builds the copy hop that carries one ingest into the selector.
//
// `-map 0 -c copy`, exactly like the ingest itself: the selector must never
// become a second place video is degraded or a track is quietly dropped. The
// one thing it adds is -output_ts_offset, which is what makes a switch a
// forward step on a shared timeline instead of a jump into the past.
//
// This is the only FFmpeg command line built outside internal/ffmpeg. It
// belongs there beside IngestArgs, where it would be table-tested with the
// rest; it is written out here because nothing in that package builds a
// relay-to-relay copy yet.
func relayFeedArgs(inURL, outURL string, offsetSeconds float64) []string {
	return []string{
		"-hide_banner", "-nostdin", "-loglevel", "warning",
		"-nostats", "-progress", "pipe:1",
		"-fflags", "+genpts",
		"-thread_queue_size", "1024",
		"-i", ffmpeg.RelayInputURL(inURL),
		"-map", "0",
		"-c", "copy",
		"-output_ts_offset", strconv.FormatFloat(offsetSeconds, 'f', 3, 64),
		"-f", "mpegts",
		"-flush_packets", "1",
		ffmpeg.RelayOutputURL(outURL),
	}
}

// slateSpec builds the standby source, and reports why a configured encoder was
// not used when it was not.
func (e *Engine) slateSpec(s db.Settings, out string, offset float64) (ffmpeg.SlateSpec, string) {
	sl := s.Failover.Slate

	e.mu.RLock()
	v := e.videoInfo
	e.mu.RUnlock()

	spec := ffmpeg.SlateSpec{
		OutRelayURL:            out,
		Color:                  sl.Color,
		VideoKbps:              sl.VideoKbps,
		Preset:                 sl.Preset,
		TimestampOffsetSeconds: offset,
	}
	// Geometry from the probe, never from a form: matching the departed ingest
	// is what gives a `-c:v copy` destination a chance of riding the change, and
	// a hand-typed 1080p over a 720p camera would be exactly the silent
	// corruption this must not cause.
	if v != nil {
		spec.Width, spec.Height, spec.FPS = v.Width, v.Height, v.FrameRate
	}

	var fallback string
	if sl.Encoder != "" {
		if err := renditionEncoderProblem(e.tools, sl.Encoder); err != nil {
			// Fails OPEN, and this is the one place in the pipeline where that
			// matters most: a standby source exists to start when everything
			// else has already failed, so an encoder we cannot vouch for costs
			// a fallback to software, never a refusal to build a command.
			fallback = err.Error()
		} else {
			// The device is left to SlateArgs' own default, exactly as a
			// rendition leaves it: one place decides which render node VAAPI
			// opens, and it is not this one.
			spec.Encoder = string(sl.Encoder)
		}
	}

	if p := strings.TrimSpace(sl.ImagePath); p != "" {
		if err := sl.SlateImageProblem(); err != nil {
			e.log.Warn("ignoring slate image; painting a flat colour instead",
				"path", p, "err", err)
		} else {
			// Confined to the data directory, resolved here rather than stored
			// absolute, exactly as a file:// pull source is.
			spec.ImagePath = filepath.Join(e.cfg.DataDir, filepath.FromSlash(p))
		}
	}
	return spec, fallback
}

// ----------------------------------------------------------- backup listener

// backupIngestSig hashes everything the second listener's command depends on.
func backupIngestSig(s db.Settings, token string) string {
	b := s.Failover.Backup
	if !s.Failover.Enabled || !b.Enabled {
		return ""
	}
	return hashStrings([]string{
		string(b.Mode),
		b.SRT.Passphrase, strconv.Itoa(b.SRT.LatencyMS),
		// The standby's RTMP address, not b.RTMP.StreamKey, which addresses
		// nothing now. Rotating the source's token moves where the standby
		// subscribes, so it has to restart -- and this hash is the only thing
		// that makes it.
		b.RTMP.App, token + backupTokenSuffix,
		// The listener ports are install-wide now, but they still belong in
		// this hash: changing one changes the command the backup runs.
		strconv.Itoa(s.Listeners.SRTPort), strconv.Itoa(s.Listeners.RTMPPort),
		b.Pull.URL, strconv.Itoa(b.Pull.ReconnectDelayMaxSeconds), b.Pull.RTSPTransport,
	})
}

// reconcileBackupIngest starts, stops or restarts the second listener. The
// caller must hold selMu.
func (e *Engine) reconcileBackupIngest(s db.Settings) {
	token := e.ingestToken()
	want := backupIngestSig(s, token)

	e.mu.Lock()
	cur := e.backup
	e.mu.Unlock()
	if cur != nil && cur.sig == want {
		return
	}

	if cur != nil {
		// The feed reads this hub, so it goes first — a feed left running
		// across the teardown would spin on a relay that has gone away.
		e.mu.Lock()
		var feed *sourceFeed
		if e.sel != nil && e.sel.feed != nil && e.sel.feed.kind == sourceBackup {
			feed, e.sel.feed = e.sel.feed, nil
			// See detachFeedForSilence: a deliberate teardown must not be
			// mistaken for a start that failed.
			e.sel.feedAt = time.Time{}
		}
		e.backup = nil
		e.mu.Unlock()
		e.teardownFeed(feed)
		e.teardownBackup(cur)
	}
	if want == "" {
		return
	}

	hub, err := relay.New(e.log, 0)
	if err != nil {
		e.log.Error("backup ingest: no relay", "err", err)
		return
	}
	b := s.Failover.Backup
	if b.Mode == db.IngestSRT {
		// Same reasoning as the primary: the Go listener already holds the
		// socket, and the backup is reached on it by publishing to
		// `<token>.backup`. All this tier needs is the hub to receive into,
		// which was created above.
		//
		// RTMP is addressed the same way and does NOT come through here, for the
		// reason reconcileIngest gives: srtserver delivers into a hub, so there
		// is nothing to spawn, while rtmpserver re-publishes to a subscriber, so
		// the subscriber still has to be a child. Same address, different
		// plumbing.
		e.mu.Lock()
		e.backup = &backupIngest{hub: hub, sig: want}
		e.mu.Unlock()
		e.log.Info("backup ingest ready", "addressedBy", "<token>.backup",
			"relayPort", hub.Port())
		return
	}
	spec := ffmpeg.IngestSpec{
		Kind:                  ffmpeg.IngestKind(b.Mode),
		SRTPort:               s.Listeners.SRTPort,
		SRTPassphrase:         b.SRT.Passphrase,
		SRTLatencyMS:          b.SRT.LatencyMS,
		RTMPPort:              s.Listeners.RTMPPort,
		RTMPApp:               b.RTMP.App,
		RTMPAddress:           token + backupTokenSuffix,
		PullURL:               b.Pull.URL,
		PullDataDir:           e.cfg.DataDir,
		PullReconnectDelayMax: b.Pull.ReconnectDelayMaxSeconds,
		PullRTSPTransport:     b.Pull.RTSPTransport,
		RelayURL:              hub.InputURL(),
	}

	proc := supervisor.New(e.log, supervisor.Spec{
		Name: "backup-ingest", Kind: "ingest", Bin: e.tools.FFmpeg,
		Args:        ffmpeg.IngestArgs(spec),
		Secrets:     ingestSecrets(b.SRT, b.RTMP, b.Pull, token+backupTokenSuffix),
		AutoRestart: true,
		// Same reasoning as the primary listener: it exits whenever its
		// streamer stops, and the backup of all things must be waiting again
		// immediately rather than backing off toward half a minute.
		MinBackoff: 500 * time.Millisecond,
		MaxBackoff: 5 * time.Second,
		OnLog:      e.onLog, OnState: e.onState, LogSink: logSink{e},
	})

	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		_ = hub.Close()
		return
	}
	e.backup = &backupIngest{proc: proc, hub: hub, sig: want}
	e.mu.Unlock()

	proc.Start()
	e.log.Info("backup ingest started", "mode", b.Mode, "url", spec.PublicIngestURL("<server>"))
}

func (e *Engine) teardownBackup(b *backupIngest) {
	if b == nil {
		return
	}
	if b.proc != nil {
		ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		_ = b.proc.Stop(ctx)
		cancel()
	}
	if b.hub != nil {
		_ = b.hub.Close()
	}
}

// ------------------------------------------------------------ playlist tier

// playlistItemUpload is item i's Upload, trimmed -- the ONE place engine.go
// reads it, so playlistSig's hash and reconcilePlaylist's resolution cannot
// disagree about what an item names.
//
// THE TRIM ITSELF IS db.PlaylistUploadName'S, not a copy of it. This function
// exists for the bounds check and for being the single engine-side accessor;
// the whitespace rule lives with the type, where internal/api can reach it too.
// It used to carry its own strings.TrimSpace, and a second one appeared in
// internal/api the moment the settings handler started reading items -- which
// is the same three-way drift, starting again, that this helper was created to
// end. See db.PlaylistUploadName for the failure.
func playlistItemUpload(items []db.PlaylistItem, i int) string {
	if i < 0 || i >= len(items) {
		return ""
	}
	return db.PlaylistUploadName(items[i].Upload)
}

// playlistItemsReady reports whether every item could actually go on air:
// every one has a derivative this profile produced.
//
// NORMALISATION IS ASYNCHRONOUS, so "an item names this upload" and "this item
// can be played" are different states, and there is nothing else on disk that
// tells them apart. An operator adds an item the moment their file finishes
// uploading; the transcode that makes it playable is a queued job that runs
// when the governor lets it, which on a machine that is busy carrying a live
// stream is not immediately.
//
// THE FAILURE THIS PREVENTS: the playlist ranks ABOVE the slate. Start the tier
// for a list whose first item is still transcoding and the selector is offered
// a candidate that cannot play, in preference to the slate -- and the slate is
// the one thing that exists so that an operator never sees nothing. Holding the
// slate for another few seconds is the cheap outcome; handing the broadcast to
// a file that is not there yet is not.
//
// IT ASKS ONE QUESTION: is there a non-empty derivative this profile produced?
// It no longer asks whether the upload survives.
//
// B1 required the upload too, because the argv NAMED it: a deleted upload was a
// tier respawn-looping on a missing file with the process reported healthy the
// whole time. B2's argv names the concat list, every entry of which is a
// derivative, so the original is never opened and that reason expired with it.
// Keeping the stat would take a PLAYING programme off air because a source file
// was tidied away -- punishing the operator for something the broadcast does not
// depend on. A missing upload is a configuration problem the readiness endpoint
// reports; it is not a reason to go to black. Validation governs what may be
// SAVED, readiness governs what may go to AIR, and this is where they stop
// swapping jobs.
//
// Confinement is not lost with the upload stat, it moves to where the path is
// actually built: playlistmedia.DerivativePath reduces the name to its base
// before joining, so no item name can name a file outside the derivative
// directory -- which is the property -c copy's `-safe 0` list depends on, and
// stronger than the uploads-directory check it replaces. An item that escapes
// is refused earlier anyway: playlistSig hashes EMPTY when PlaylistFileProblem
// fails, so an unusable list never reaches this function at all.
//
// This is NOT a second notion of availability. Sub-project A settled that a
// candidate is available when its hub is delivering bytes, and chooseSource
// sees one plain boolean for that; a parallel "ready" input would give the
// selector two ways to be unavailable and make the golden table's claim to
// exhaustiveness false. This decides whether the hub gets FED at all: an
// unready playlist starts no tier, so playlistRunning stays false through the
// existing byte-counter path and the slate wins by the ranking already there.
//
// An empty list is vacuously ready and never reaches here anyway: playlistSig
// is empty for an enabled playlist with no items, so reconcilePlaylist has
// already returned.
//
// It takes the items rather than reading e.settings because reconcilePlaylist
// must gate the playlist it is ABOUT TO START -- the settings it was handed --
// and not whatever the engine last stored. In production those are the same
// value; Reconcile assigns e.settings before reconcileOutputs runs. Anywhere
// else they are not, and a gate that consulted the wrong one would refuse or
// admit a playlist other than the one being started.
func (e *Engine) playlistItemsReady(items []db.PlaylistItem) bool {
	for i := range items {
		// playlistItemUpload is the ONE trim point shared with playlistSig, so
		// readiness cannot disagree with the hash about what an item names.
		upload := playlistItemUpload(items, i)
		// The normalised copy, which is what says the transcode has finished AND
		// is the exact file reconcilePlaylist puts in the concat list below.
		// os.Stat, never a Resolve: Resolve is a shape check that never touches
		// the disk, and the question here is existence.
		//
		// NON-EMPTY, not merely present. A zero-length file under the finished
		// name is a transcode that died between create and first write, and
		// nothing on disk distinguishes it from a finished one except its size.
		// Admit it and the concat list names an empty file, FFmpeg exits at once
		// and the tier respawn-loops while the process reports healthy -- the
		// same shape as B1's deleted-upload loop, and it would do it in
		// preference to the slate.
		//
		// The other three readers of this exact path already treat zero-length
		// as absent: api.playlistItemStatus, api.enqueuePlaylistNormalisation
		// and RunNormalise's already-normalised skip. This one decides what goes
		// to AIR, so a disagreement here is the one that costs a broadcast --
		// and it would show as an amber UI over a black output, with nothing
		// reconciling the two.
		fi, err := os.Stat(playlistmedia.DerivativePath(e.cfg.DataDir, upload))
		if err != nil || fi.Size() == 0 {
			return false
		}
	}
	return true
}

// playlistSig hashes everything the playlist's command depends on, and is empty
// when the tier must not run.
//
// An unusable list hashes empty rather than being started and left to fail:
// PlaylistFileProblem is the same confinement a file:// pull source and the
// slate's still are held to, and an item that fails it is operator input
// trying to name something other than a stored upload, not a file to hand
// FFmpeg anyway.
func playlistSig(s db.Settings) string {
	p := s.Failover.Playlist
	if !s.Failover.Enabled || !p.Enabled {
		return ""
	}
	if p.PlaylistFileProblem() != nil {
		return ""
	}
	// Every item's name is part of the hash, and in order, so re-sequencing
	// the list (once sequencing exists) respawns exactly as editing one entry
	// does now.
	parts := make([]string, 0, len(p.Items)+1)
	parts = append(parts, "playlist")
	for i := range p.Items {
		parts = append(parts, playlistItemUpload(p.Items, i))
	}
	return hashStrings(parts)
}

// playlistFeedArgs builds the loop that publishes the WHOLE list into the
// playlist's own hub. It is the backup's command with a concat list where the
// socket was: `-map 0 -c copy`, so a programme that was encoded once is not
// re-encoded here.
//
// -f concat over every item's derivative, looped as a whole rather than per
// item, so the list plays in order and then starts again from the top.
//
// THE LIST NAMES DERIVATIVES, which is the point of B1: every entry shares
// codec, timebase, geometry and channel layout by construction, so `-c copy`
// across a seam is a copy and not a codec change.
//
// -safe 0 because the list holds absolute paths. They are paths this process
// built through playlistmedia.DerivativePath, from a name uploads.SafeName had
// already sanitised and DerivativePath then reduces to its base -- never
// operator text reaching FFmpeg as written, which is the whole reason items
// reference uploads rather than paths.
//
// ALWAYS CONCAT, EVEN FOR ONE ITEM. A single-file special case would mean two
// argv shapes, two sets of seam behaviour, and a branch that is wrong in a way
// nobody notices until the one-item playlist is the one on air.
//
// NO `duration` DIRECTIVES ARE EMITTED, and that is a measured result rather
// than a preference: three real derivatives were concatenated, looped past two
// full wraps and probed over 1068 packets with and without the per-entry
// directives. See internal/playlistmedia/concat_behaviour_test.go.
//
// The packet streams are identical ONLY WHEN THE DIRECTIVE IS EXACT. An earlier
// version of this comment said "byte-identical either way" without that clause,
// and the measurement does not support it: at three decimals the directive is
// 333 microseconds short of the file and every packet after the first item
// shifts. ffmpeg.ConcatList renders `%.3f` because ConcatEntry.DurationMS is
// MILLISECONDS, so the only directives this product could emit are the
// inaccurate kind -- which is an argument for leaving the field zero, not
// against it. The field survives for a profile that drifts far enough to need
// it, and whoever turns it on needs sub-millisecond precision first.
//
// The two input flags are the whole difference from every other feed, and
// neither is optional. -stream_loop -1 is what makes a file that ends look like
// a source that does not, so the tier is still delivering an hour later instead
// of exiting once and leaving the selector with a candidate that vanished.
// Without -re FFmpeg reads a file as fast as the disk allows: an hour of
// programme arrives at the relay in seconds, the hub's consumers are handed a
// timeline racing away from wall clock, and what an operator sees is a playlist
// that "played" and disappeared. The same pair, for the same reason, is what
// pullFile emits for a file:// ingest.
//
// The "file:" prefix pins the LIST to the file protocol, as B1 pinned the
// upload path and as pullFile and pullSource pin theirs.
//
// WHAT IT ACTUALLY PROTECTS IS NARROWER THAN IT LOOKS, and the boundary was
// measured against FFmpeg 8.1.2 rather than reasoned about. FFmpeg infers a
// protocol from the characters before the first ":" ONLY while no "/" has
// appeared, so:
//
//   - "2026:01/data/list.txt" -- a RELATIVE data directory whose first segment
//     carries a colon -- fails with "Protocol not found" unprefixed, and opens
//     with the prefix. This is the case the prefix buys.
//   - "/mnt/2026:01/data/list.txt" is fine either way. An ABSOLUTE path can
//     never be misread, because the leading "/" ends protocol detection before
//     the colon is reached.
//   - "data/a:b/list.txt" is fine either way, for the same reason.
//
// So the widely-repeated worry -- an operator's data directory containing a
// colon -- is NOT a failure for any absolute DataDir, which is the ordinary
// case. The prefix is kept because it makes the guarantee unconditional instead
// of resting on that argument, it costs one string concatenation, and it keeps
// every file input in this package spelled the same way. It is not load bearing
// for a deployment that configures an absolute data directory.
//
// THE FILE'S CODEC PARAMETERS MUST MATCH THE INGEST'S, AND NOTHING CHECKS.
// `-c copy` here and a copy hop in startFeed mean the file's codec, resolution,
// frame rate and pixel format reach every destination exactly as they were
// encoded. A destination that is also copying passes them straight to the
// platform, so switching to a file that differs is a mid-stream codec change --
// and platforms answer that by dropping the connection, which is the one thing
// this whole tier exists to prevent. The slate re-encodes to the ingest's
// PROBED geometry for precisely this reason; the playlist cannot, because
// re-encoding a programme that was already encoded once is a cost an operator
// did not ask for. scripts/acceptance-failover.sh builds its filler clip to
// match the publisher deliberately, and says so.
//
// It is not validated here because validation would mean probing the operator's
// file at settings-save time and comparing it against an ingest that may not be
// connected yet -- a feature with its own failure modes (an unprobeable file, a
// geometry that changes when the encoder reconnects), not a check this function
// can make from an argv. Until that exists the constraint is documented where
// an operator meets it, in docs/SCHEDULED-BROADCAST.md.
func playlistFeedArgs(listPath, outURL string) []string {
	return []string{
		"-hide_banner", "-nostdin", "-loglevel", "warning",
		"-nostats", "-progress", "pipe:1",
		"-stream_loop", "-1",
		"-re",
		// Each entry's own timestamps restart at every seam and at every loop
		// boundary; genpts gives the relay a monotonic base without touching the
		// payload.
		"-fflags", "+genpts",
		"-f", "concat", "-safe", "0", "-i", "file:" + listPath,
		"-map", "0",
		"-c", "copy",
		"-f", "mpegts",
		"-flush_packets", "1",
		ffmpeg.RelayOutputURL(outURL),
	}
}

// reconcilePlaylist starts, stops or restarts the playlist tier. The caller
// must hold selMu.
//
// Signature-compared rather than restarted unconditionally, exactly as the
// backup listener is: a respawn is visible downstream -- the hub goes quiet
// while FFmpeg reopens the file and the loop restarts from the top -- so a
// settings save that touches the recorder or a destination must not cost the
// playlist a gap. The no-op is the common case, not the optimisation.
func (e *Engine) reconcilePlaylist(s db.Settings) {
	want := playlistSig(s)

	e.mu.Lock()
	cur := e.playlist
	e.mu.Unlock()
	if cur != nil && cur.sig == want {
		return
	}

	// READINESS IS EVALUATED BEFORE ANYTHING IS TORN DOWN, and the order is the
	// whole of this check. It used to sit after the teardown block below, so an
	// operator who appended one item to a playlist that was ON AIR moved the
	// signature, lost the running tier to the teardown, and then had the gate
	// refuse to bring it back because the new item had not been normalised yet.
	// That is dead air, bought for an item B1 would not have played anyway --
	// it still plays item 0 only -- and it lasted until some unrelated event
	// happened to reconcile again. Refusing HERE leaves the running tier
	// exactly as it was, still recorded under the OLD signature, so the next
	// reconcile after the transcode lands still sees a mismatch and respawns
	// onto the new list. Nothing is latched.
	//
	// Only when want is non-empty. An empty signature means the playlist must
	// STOP -- the operator disabled it, or the items no longer pass
	// PlaylistFileProblem -- and a stop must never be held up by a readiness
	// question about a list that is not going on air.
	if want != "" && !e.playlistItemsReady(s.Failover.Playlist.Items) {
		e.log.Info("playlist not started; not every item has been normalised yet",
			"items", s.Failover.Playlist.Items,
			"alreadyRunning", cur != nil,
			"reason", "the playlist ranks above the slate, so a tier started for an item "+
				"that cannot play would displace the source that exists so an operator "+
				"never sees nothing; a finished normalisation job reconciles again")
		return
	}

	if cur != nil {
		// The feed reads this hub, so it goes first -- a feed left running
		// across the teardown would spin on a relay that has gone away. This
		// is now a live hazard rather than a precaution: startFeed builds a
		// playlist copy hop out of exactly the hub being closed two lines below.
		e.mu.Lock()
		var feed *sourceFeed
		if e.sel != nil && e.sel.feed != nil && e.sel.feed.kind == sourcePlaylist {
			feed, e.sel.feed = e.sel.feed, nil
			// See detachFeedForSilence: a deliberate teardown must not be
			// mistaken for a start that failed.
			e.sel.feedAt = time.Time{}
		}
		// A TIER THAT HAS BEEN TORN DOWN MUST NOT READ AS DELIVERING, and this
		// is the line that makes that true at the instant it stops being a
		// place to send viewers rather than up to a sweep later.
		//
		// The liveness this zeroes is the ONLY thing candidatesFor consults
		// about the playlist. Leave it standing and the very next decision --
		// applySourceChoice, which reconcileSelector calls immediately after
		// this function with no sample in between -- reads a counter describing
		// a hub that has just been closed, holds the playlist, and asks
		// startFeed for a relay that no longer exists. What an operator does to
		// reach that is not exotic: they untick the playlist while it is on
		// air. The result was roughly two seconds of dead air on every
		// destination, because the sweep that would have corrected it arrives
		// after ensureFeed's failed-start backoff has already begun.
		//
		// Zeroed HERE rather than gated in candidatesFor on
		// Failover.Playlist.Enabled, which was the other candidate fix and is
		// the shape the backup's own line has. That gate would answer the
		// untick and nothing else: playlistSig is also empty when the whole
		// failover feature goes off and when the file path fails confinement,
		// and it changes for a path EDIT, which closes this hub and builds a
		// fresh one counting from zero. In every one of those the setting still
		// reads enabled while the hub is gone or new, so a gate on the setting
		// would leave the same stale-liveness hole open under a different
		// cause. One assignment at the single place the tier can go away covers
		// all four, and it keeps chooseSource's inputs describing DELIVERY
		// rather than mixing in a process fact -- which is what the comment on
		// sourceChoice.playlistRunning promises a reader.
		if e.sel != nil {
			if pl := e.sel.live[sourcePlaylist]; pl != nil {
				*pl = liveness{}
			}
		}
		e.playlist = nil
		e.mu.Unlock()
		e.teardownFeed(feed)
		e.teardownPlaylist(cur)
	}
	if want == "" {
		if s.Failover.Enabled && s.Failover.Playlist.Enabled {
			// Only reachable through settings that never passed validation --
			// db.Settings.Validate rejects an item that fails PlaylistFileProblem
			// -- so it is said out loud rather than left as a tier that quietly
			// never starts.
			e.log.Warn("playlist not started; its items are unusable",
				"items", s.Failover.Playlist.Items,
				"err", s.Failover.Playlist.PlaylistFileProblem())
		}
		return
	}

	// READINESS GATED THE START ABOVE, before the teardown, and that is the
	// whole of it -- there is no second availability input anywhere below. A
	// playlist that is not ready starts no tier, so it has no hub, so
	// playlistRunning stays false through the byte counter sampleSources
	// already reads, and the slate wins on the ranking that is already there.
	//
	// Re-read on every reconcile rather than watched: an unready reconcile
	// records no tier, so the next one re-evaluates for free. What FIRES that
	// next reconcile when the last normalisation job finishes is
	// cmd/polyemesis/postprod.go's queue change hook, which calls
	// Manager.Reconcile for a completed playlistmedia.KindNormalise. Without
	// it nothing would: reconcilePlaylist is reached only from Reconcile, and
	// Reconcile is only called by settings saves, the API, the manager and the
	// scheduler -- none of which a finished job is.
	hub, err := relay.New(e.log, 0)
	if err != nil {
		e.log.Error("playlist: no relay", "err", err)
		return
	}

	// EVERY ITEM, IN ORDER, AND IT PLAYS THE DERIVATIVES. The gate above has
	// already required a derivative for every item; these are the very files it
	// stat'd, and the operator's originals are not opened at all. That is what
	// makes the fixed normalised profile load bearing rather than bookkeeping:
	// the concat demuxer requires every file in the list to share codecs,
	// timebase, resolution and channel layout, and `-c copy` across a seam is
	// only a copy because B1 guaranteed it.
	//
	// Built through playlistmedia.DerivativePath, never a join of our own, so
	// the profile version in the filename cannot drift from the one the
	// normaliser writes -- and so the name is reduced to its base on the way,
	// which is the confinement standing behind `-safe 0`.
	//
	// No per-entry `duration` directives: DurationMS is left zero deliberately.
	// Three real derivatives, looped past two full wraps, 1068 packets probed
	// with and without them -- identical ONLY when the directive is exact, and
	// ConcatList renders milliseconds. See the fuller note above playlistFeedArgs
	// and internal/playlistmedia/concat_behaviour_test.go.
	// ABSOLUTE, and that is a bug fix rather than tidiness.
	//
	// The concat demuxer resolves a relative entry against THE LIST FILE'S OWN
	// DIRECTORY -- and the list lives in <dataDir>/playlist-media, which is
	// exactly the prefix a relative DerivativePath already carries. So
	// `--data data` produced entries like `data/playlist-media/x.ts.v2.ts` in a
	// list at `data/playlist-media/`, the demuxer looked for
	// `data/playlist-media/data/playlist-media/x.ts.v2.ts`, and the tier
	// respawn-looped on a file that was sitting right there. Readiness had
	// already stat'd it and passed, because readiness resolves paths from the
	// process's working directory the way every other caller does.
	//
	// Nothing shipped hits it -- deployments pass an absolute --data and the
	// engine's own tests build one with t.TempDir() -- which is precisely why it
	// survived: every existing caller happened to be absolute, so the one that
	// is not had nothing watching it. Found by the acceptance suite.
	items := s.Failover.Playlist.Items
	entries := make([]ffmpeg.ConcatEntry, 0, len(items))
	for i := range items {
		p := playlistmedia.DerivativePath(e.cfg.DataDir, playlistItemUpload(items, i))
		abs, err := filepath.Abs(p)
		if err != nil {
			// Only when the working directory cannot be read, which is a great
			// deal more wrong than one playlist. Said out loud rather than
			// silently feeding the demuxer a path it will double.
			e.log.Error("playlist: cannot make a derivative path absolute",
				"path", p, "err", err)
			return
		}
		entries = append(entries, ffmpeg.ConcatEntry{Path: abs})
	}

	// THE FILENAME CARRIES THE SIGNATURE AND THIS ENGINE'S SOURCE ID, and it
	// needs both.
	//
	// The signature alone fixes the different-list overwrite but not the
	// identical-list case. internal/engine/manager.go runs ONE ENGINE PER
	// SOURCE over a shared data directory, so two sources configured with the
	// same playlist hash the same: one tier stopping would delete a file the
	// other's FFmpeg is still re-reading at its next wrap, and the second source
	// would drop off air for a reason nothing in its own configuration changed.
	// A tier deletes only the path it holds, and only once its process is gone.
	listPath := filepath.Join(playlistmedia.DerivativeDir(e.cfg.DataDir),
		fmt.Sprintf("playlist-%d-%s.txt", e.sourceID, want))
	if err := os.MkdirAll(filepath.Dir(listPath), 0o755); err != nil {
		e.log.Error("playlist: no derivative directory for the list", "err", err)
		_ = hub.Close()
		return
	}
	if err := os.WriteFile(listPath, []byte(ffmpeg.ConcatList(entries)), 0o600); err != nil {
		// Written BEFORE the process is spawned, so a list that cannot be
		// written is a tier that never starts rather than one that respawn-loops
		// on a file it will never find.
		e.log.Error("playlist: cannot write the concat list", "path", listPath, "err", err)
		_ = hub.Close()
		return
	}

	proc := supervisor.New(e.log, supervisor.Spec{
		Name: "playlist", Kind: "source", Bin: e.tools.FFmpeg,
		Args: playlistFeedArgs(listPath, hub.InputURL()),
		// AutoRestart, unlike a selector feed: this process publishes into its
		// OWN hub and carries no timestamp offset, so there is nothing for a
		// sweep to rebuild and no reason to make an operator wait for one. A
		// file that FFmpeg cannot open backs off toward five seconds and says so
		// every time, which is what the supervisor's backoff is for.
		AutoRestart: true,
		MinBackoff:  500 * time.Millisecond,
		MaxBackoff:  5 * time.Second,
		OnLog:       e.onLog, OnState: e.onState, LogSink: logSink{e},
	})

	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		_ = hub.Close()
		// No tier is recorded, so nothing else will ever own this file. It is
		// removed here or not at all.
		_ = os.Remove(listPath)
		return
	}
	e.playlist = &playlistTier{proc: proc, hub: hub, sig: want, listPath: listPath}
	e.mu.Unlock()

	proc.Start()
	e.log.Info("playlist started", "list", listPath, "items", len(entries),
		"relayPort", hub.Port(),
		"reason", "a playlist publishes into a hub of its own, so a file on air "+
			"never makes the primary ingest read live")
}

func (e *Engine) teardownPlaylist(p *playlistTier) {
	if p == nil {
		return
	}
	if p.proc != nil {
		ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		_ = p.proc.Stop(ctx)
		cancel()
	}
	if p.hub != nil {
		_ = p.hub.Close()
	}
	// AFTER Stop returns, and only the path this tier wrote.
	//
	// After, because the concat demuxer re-reads the list at every wrap: remove
	// it while the process is still running and a tier that was asked to stop
	// spends its last seconds failing to reopen its own input, logging as though
	// something were wrong. Only this path, because another source may hold an
	// identically-hashed list of its own -- which is why the filename carries a
	// source id at all.
	//
	// The error is dropped deliberately. A list that is already gone is the
	// ordinary outcome of a data directory cleaned up underneath us, and there
	// is nothing a stopping tier could usefully do about it.
	if p.listPath != "" {
		_ = os.Remove(p.listPath)
	}
}

// ------------------------------------------------------- failover: operator

// SwitchSource puts one source on air by hand.
//
// "auto" hands the decision back to the detector. Anything else is honoured
// only while that source is delivering, which is what makes a manual return
// safe: a pin cannot strand the broadcast on an input that has since died.
func (e *Engine) SwitchSource(kind string) error {
	var want sourceKind
	switch sourceKind(strings.ToLower(strings.TrimSpace(kind))) {
	case sourcePrimary:
		want = sourcePrimary
	case sourceBackup:
		want = sourceBackup
	case sourceSlate:
		want = sourceSlate
	case sourcePlaylist:
		// Accepted only now that a playlist feed can actually be built. The pin
		// itself has been honoured by the decision since the candidate was
		// added; this list was the one thing keeping an operator from setting
		// it, and it stayed shut on purpose while a decision of "playlist"
		// would have reached a feed layer that could not run one.
		want = sourcePlaylist
	case sourceNone, "auto":
		want = sourceNone
	default:
		return fmt.Errorf("unknown source %q (primary, backup, slate, playlist, auto)", kind)
	}

	e.selMu.Lock()
	defer e.selMu.Unlock()

	e.mu.Lock()
	if e.sel == nil || e.sel.hub == nil {
		e.mu.Unlock()
		return fmt.Errorf("failover is not enabled, so there is nothing to switch between")
	}
	prev := e.sel.pinned
	e.sel.pinned = want
	e.mu.Unlock()

	// The pin has to be committed before the decision, because the decision
	// reads it. That makes rolling it back the only way to keep the pin and
	// what actually happened in step: a failed decision switched nothing, and a
	// pin that survived one would be LATCHED -- re-read by every 500ms sweep,
	// re-panicking on the same input forever, with the dashboard showing a
	// pinned source that is not the active one and the operator holding an
	// HTTP 200 that said it worked. Rolled back under e.mu exactly as it was
	// set, and still inside the selMu the whole method holds, so no sweep can
	// observe the pin between the failure and the rollback. Nothing is
	// published: the state is what it was before the call.
	if err := e.applySourceChoice(e.Settings(), e.heldSilence(), time.Now()); err != nil {
		e.mu.Lock()
		if e.sel != nil {
			e.sel.pinned = prev
		}
		e.mu.Unlock()
		e.log.Error("source selection was not applied; the pin was rolled back",
			"source", e.sourceID, "requested", kind, "err", err)
		return fmt.Errorf("could not select source %q: %w", kind, err)
	}

	// Logged only once the switch has actually been applied. Announcing it
	// first is how the log came to say a source was selected on sweeps where
	// nothing was.
	if want == sourceNone {
		e.log.Info("source selection returned to automatic")
	} else {
		e.log.Info("source selected by operator", "source", string(want))
	}
	e.publishStatus()
	return nil
}

// FailoverStatus is the tier as the dashboard reports it. Absent entirely when
// the tier is not running, which is the default.
type FailoverStatus struct {
	Active sourceKind `json:"active"`
	Reason string     `json:"reason,omitempty"`
	// Pinned is the operator's manual choice, empty when the detector is in
	// charge.
	Pinned     sourceKind `json:"pinned,omitempty"`
	SwitchedAt time.Time  `json:"switchedAt"`
	Switches   int        `json:"switches"`
	Error      string     `json:"error,omitempty"`
	RelayPort  int        `json:"relayPort,omitempty"`

	PrimaryLive   bool `json:"primaryLive"`
	BackupLive    bool `json:"backupLive"`
	BackupEnabled bool `json:"backupEnabled"`
	SlateEnabled  bool `json:"slateEnabled"`

	Feed   *supervisor.Status `json:"feed,omitempty"`
	Backup *supervisor.Status `json:"backup,omitempty"`
}

// Failover returns the tier's live state, or nil when there is none.
func (e *Engine) Failover() *FailoverStatus {
	s := e.Settings()
	now := time.Now()
	grace := failoverGrace(s)

	// Everything the tier owns is copied out under the lock — the mutable
	// fields as VALUES, not through the selector pointer, because the failover
	// sweep is writing them from another goroutine. Only the two process
	// handles leave the lock, and those are read the way every other status
	// reads them: after it is released, so a state callback cannot deadlock
	// against a snapshot being taken.
	e.mu.RLock()
	sel := e.sel
	if sel == nil {
		e.mu.RUnlock()
		return nil
	}
	st := &FailoverStatus{
		Active:        sel.active,
		Reason:        sel.reason,
		Pinned:        sel.pinned,
		SwitchedAt:    sel.switchedAt,
		Switches:      sel.switches,
		Error:         sel.err,
		BackupEnabled: s.Failover.Backup.Enabled,
		SlateEnabled:  s.Failover.Slate.Enabled,
	}
	if sel.hub != nil {
		st.RelayPort = sel.hub.Port()
	}
	if sel.live != nil {
		st.PrimaryLive = sel.live[sourcePrimary].alive(now, grace)
		st.BackupLive = st.BackupEnabled && sel.live[sourceBackup].alive(now, grace)
	}
	var feedProc, backupProc *supervisor.Process
	if sel.feed != nil {
		feedProc = sel.feed.proc
	}
	if e.backup != nil {
		backupProc = e.backup.proc
	}
	e.mu.RUnlock()

	st.Feed = procStatus(feedProc)
	st.Backup = procStatus(backupProc)
	return st
}
