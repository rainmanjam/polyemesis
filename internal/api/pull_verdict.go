package api

import (
	"fmt"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/uploads"
)

// The third consumer of "stored does not imply checked".
//
// #118 made "stored but never inspected" a state the product can SAY -- a
// uploads.Verdict beside the file, `verified` on every listing -- and taught
// two consumers to stop assuming stored implied checked:
// playlistUploadProblems refuses a playlist item the operator is introducing
// whose upload carries a recorded "nobody read this", and playlistmedia's
// normalise worker re-runs ffmpeg.ProbeFile's format allowlist on whatever it
// is handed.
//
// THE PULL SOURCE WAS THE ONE THAT WAS MISSED. uploads.File.PullURL is
// "file://uploads/<name>", it is offered copyable in the Library for exactly
// this purpose, and pasting it into Settings -> Ingest -> Pull hands the path
// to the ENGINE's FFmpeg -- a different code path from ffmpeg.ProbeFile,
// carrying neither the selfContainedFormats allowlist nor the
// -protocol_whitelist file pin. So an upload the operator's own dropped
// connection kept this server from inspecting could be routed to air without
// anything having read a byte of it.
//
// WHY IT IS A REAL GATE AND NOT A DUPLICATE OF THE LIBRARY'S MARKER. The marker
// is good and it was the argument for deferring this: the operator has to copy
// the URL out of a row that says "Not checked". But #201 also names what
// removes the marker from the path -- a pull source configured by automation
// from a listing, which never sees a row at all -- and the settings API is
// reachable by exactly that. A warning in the UI is not a check in the server.
//
// WHY HERE AND NOT IN internal/db.Settings.Validate, which is where every other
// pull-URL rule lives (ffmpeg.ValidatePullURL is called from there twice). The
// same reason playlistUploadProblems is here: this needs an uploads store, and
// putting one behind Settings.Validate would put an os.MkdirAll and a stat
// behind every db.GetSettings -- roughly twenty callers, several of them
// per-request handlers. That is a defect this sub-project has already had to
// undo once.
//
// THE REAL FIX IS STILL BIGGER, and #201 says so: pushing the allowlist into
// the engine's file-input path would give one gate instead of a per-consumer
// list. This is the per-consumer gate, and it closes the reachable hole today
// without touching the engine's input construction.

// pullSourceUploadProblems reports why a pull source the operator is
// INTRODUCING cannot be used, or nil.
//
// want is the document about to be stored; storedPrimary and storedBackup are
// the two pull URLs already saved, captured before the incoming payload was
// decoded over them.
//
// SCOPED TO WHAT THIS SAVE CHANGES, exactly as playlistUploadProblems is, and
// for the same reason spelled out there: validation rejects what the operator
// is introducing and must not punish them for pre-existing state. The settings
// page PUTs the whole document back on every unrelated change, so an
// unconditional check would refuse a save about an alert threshold because a
// pull source configured last year names a file that was stored unchecked --
// with the operator given no way to tell which of the two things they touched
// was the problem. An unchanged URL is state they inherited.
//
// A URL that does not name an upload at all -- rtsp://, https://, a file://
// pointing somewhere else under the data directory -- is not this function's
// business and returns nothing. ffmpeg.ValidatePullURL, called from
// Settings.Validate, is what decides whether it is a legal source.
func (s *Server) pullSourceUploadProblems(want db.Settings, storedPrimary, storedBackup string) error {
	type candidate struct {
		where  string
		url    string
		stored string
	}
	// The backup is checked even when Enabled is false. Storing a source that
	// is off today and switched on during an outage is the case this exists
	// for: the moment it matters is the moment nobody is going to re-read the
	// Library row.
	for _, c := range []candidate{
		{"the pull source", want.Ingest.Pull.URL, storedPrimary},
		{"the backup pull source", want.Failover.Backup.Pull.URL, storedBackup},
	} {
		if c.url == c.stored {
			continue
		}
		name, ok := uploads.UploadFromPullURL(c.url)
		if !ok {
			continue
		}
		store, err := s.uploadStore()
		if err != nil {
			// Fail closed, as playlistUploadProblems does. Not being able to
			// ask is not the same as the answer being yes.
			return fmt.Errorf("%s cannot be checked: %w", c.where, err)
		}
		if what, remedy, bad := uploadObjection(store, name); bad {
			return fmt.Errorf("%s names %q, which %s; %s", c.where, name, what, remedy)
		}
	}
	return nil
}

// uploadObjection answers the one question every consumer of "stored does not
// imply checked" asks: may a pull source name this upload, and if not, what do
// you tell the operator. what completes "...which %s" and remedy is the
// sentence after it, so a caller supplies only its own subject.
//
// RECORDED AS A PROBLEM, NOT "NOT RECORDED AS FINE", which is the same
// distinction playlistUploadProblems draws and for the same reason: every
// upload stored before verdicts existed has no record at all, and refusing
// those would strand media an operator has had for a year over a file that was
// never written. See uploads.Store.Verdict's second return.
//
// SPELLED ONCE BECAUSE FOUR CALLERS MUST AGREE -- pullSourceUploadProblems
// above, sourceIngestUploadProblem below, Server.pullUploadUnchecked, which
// reports rather than refuses, and now Engine.pullUploadRefusal, which stops an
// inherited ingest. Two refuse a save, one puts a sentence on a card and one
// takes a programme off air; a copy of this condition that drifted would let
// them disagree about which files are safe, and the disagreement would be
// invisible. #264 inlined this switch into pullSourceUploadProblems, which was
// right when that was the only caller and stopped being right at the third.
//
// THE SWITCH ITSELF NOW LIVES IN internal/uploads, and this is the adapter that
// keeps the three handlers' call shape. It moved for one reason: internal/api
// imports internal/engine (sourceView carries an engine.ListenerHealth), so the
// engine cannot import this package to reach it, and #255's decision gives the
// engine a fourth callsite. The alternative was a second copy of the switch in
// the engine -- the exact drift this comment exists to prevent. See
// uploads.Store.Objection, which also carries the Refused flag the engine needs
// and these three callers do not: they refuse or report either way, and only
// the engine treats the two states differently.
func uploadObjection(store *uploads.Store, name string) (what, remedy string, bad bool) {
	o, bad := store.Objection(name)
	return o.What, o.Remedy, bad
}

// sourceIngestUploadProblem is pullSourceUploadProblems for the route that
// actually decides what the engine pulls, and #255 is that this route had no
// gate at all.
//
// MEASURED BEFORE IT WAS WRITTEN. `PUT /api/v1/sources/1` carrying
// `ingest.pull.url = file://uploads/<an upload recorded unchecked>` answered
// 200 and stored it; `POST /api/v1/sources` answered 201. Neither went through
// pullSourceUploadProblems, which is reached only from the settings handler.
//
// AND THE SETTINGS GATE IS NOT A SUBSTITUTE, because engine.effectiveSettings
// does `settings.Ingest = src.Ingest` -- the source row's ingest REPLACES the
// settings one for every engine. PUT /settings mirrors its ingest block into
// the DEFAULT source (see handlers.go), which is what kept that gate meaningful
// at all; it has never covered a second programme, and the Sources page edits
// every programme through this route. So the gated path was the legacy one and
// the ungated path was the one the UI uses.
//
// SCOPED TO WHAT THE SAVE INTRODUCES, exactly as the settings gate is. stored
// is the URL already on the row, empty for a create -- where everything is
// introduced by definition. The Sources card PUTs the whole ingest block on
// every unrelated change (a port, a passphrase, the mode), so an unconditional
// check would refuse an edit to an SRT latency because of a pull URL configured
// before this existed, with nothing on the form to say which field was wrong.
// The inherited case is REPORTED instead, not refused -- see pullUploadUnchecked.
func (s *Server) sourceIngestUploadProblem(want db.IngestSettings, stored string) error {
	if want.Pull.URL == stored {
		return nil
	}
	name, ok := uploads.UploadFromPullURL(want.Pull.URL)
	if !ok {
		return nil
	}
	store, err := s.uploadStore()
	if err != nil {
		// Fail closed, as both other save-time gates do. Not being able to ask
		// is not the same as the answer being yes.
		return fmt.Errorf("the pull source cannot be checked: %w", err)
	}
	if what, remedy, bad := uploadObjection(store, name); bad {
		return fmt.Errorf("the pull source names %q, which %s; %s", name, what, remedy)
	}
	return nil
}

// pullUploadUnchecked is the INHERITED case, and it reports rather than
// refuses. Empty when there is nothing to say.
//
// #255 offers two directions for a pull source that already names an unchecked
// upload -- re-check at engine reconcile, which fails closed, or surface it on
// the card, which fails open -- and says the choice turns on whether an
// operator would rather lose an ingest or air an uninspected file. This is the
// second, and the reason is that the first question is not the one a reconcile
// gate would actually be answering.
//
// AN UNVERIFIED VERDICT IS A FACT ABOUT THIS SERVER, NEVER ABOUT THE FILE.
// All four reasons say so in their own words: uploads.ReasonNoProber ("this
// server had no ffprobe available"), ReasonProbeUnusable ("this server could
// not run its media inspection"), ReasonInterrupted ("the inspection was cut
// short"), ReasonNotInspected. A file ffprobe DID read and reject never becomes
// an unverified verdict at all -- probeUpload returns an error and the upload
// is refused with 400 and not stored. So "unchecked" means "nobody looked", and
// on an install with no ffprobe it means that of EVERY upload, by construction.
// A fail-closed reconcile gate on that condition is not a check keyed to a bad
// file; it is a kill switch keyed to this server's toolchain, and its blast
// radius on an ffprobe-less install is every file:// pull ingest, all at once.
//
// AND IT WOULD LAND WHERE NOBODY IS STANDING. reconcileIngest runs on every
// source switch and every supervisor respawn, so the refusal arrives at 3am as
// a stopped stream and a log line. internal/ffmpeg/build.go already refuses to
// pay this price for a subprocess -- "playlist_normalise refuses that price for
// a mere HTTP request; the live stream cannot pay a higher one" -- and making
// the check cheap does not make the OUTAGE cheap. The cost that was being
// weighed there was never the probe; it was the fail-closed outcome.
//
// THE WORST CASE THIS DECLINES TO PREVENT IS BOUNDED, and build.go measured it
// on FFmpeg 8.1.2: with -protocol_whitelist file pinned on the engine's file
// pull and concat's safe=1 default, an ffconcat naming "http://..." is refused,
// and what still resolves is a SIBLING FILE -- another upload in the operator's
// own directory. Airing the wrong one of your own files is not worth taking the
// programme off air for.
//
// WHAT MAKES THIS MORE THAN A BADGE is that the server computes it and puts it
// in the /sources response. pull_verdict.go's own objection to the Library
// marker was "a warning in the UI is not a check in the server", and the case
// it named was automation configuring a pull source from a listing, which never
// sees a row. That automation reads this field. It is still fail-open and this
// is not claimed as a closure of #255's hole -- an operator who ignores it airs
// an uninspected file.
//
// #264 ADDED A STATE THIS ARGUMENT DOES NOT COVER, and it is named here rather
// than folded in silently. Everything above turns on one claim: "an unverified
// verdict is a fact about this server, never about the file", which is what
// makes a fail-closed reconcile gate a kill switch keyed to a missing ffprobe
// rather than a check keyed to bad media. OutcomeRefused breaks that claim in
// its own documentation -- "a statement about the FILE, it is permanent, and
// trying again is not a remedy". So for a refusal:
//
//   - the blast-radius objection does not apply. A refusal can only exist where
//     an inspection RAN and rejected the bytes, so it cannot be true of every
//     upload on an ffprobe-less install, which was the whole shape of the
//     outage being avoided.
//   - the bounded-worst-case argument is weaker too. What airs is not "the
//     wrong one of your own files" but bytes this server has already read and
//     concluded are not media.
//
// THE MAINTAINER DECIDED IT AS A SPLIT, and #255 is closed against exactly the
// asymmetry above: unverified is warned about here and keeps streaming, and a
// refusal now stops the ingest at engine reconcile. See
// engine.Engine.pullUploadRefusal, which is the fail-closed half and carries
// the reasoning; everything above it is why the fail-open argument covers the
// first state and not the second.
//
// SO THIS FUNCTION KEEPS BOTH SENTENCES, and that is not an oversight. For an
// unchecked upload it is the whole remedy. For a refused one the ingest is
// already down, and the card is still where the operator finds out which file
// and why -- a stopped programme with no sentence beside it is how the old
// fail-closed proposal was going to land at 3am. The reconcile also records the
// same sentence as a stop note (engine.reloadStop), so a save made afterwards
// answers with it too.
//
// What DOES change in the sentence: the old one said "nothing has read a byte
// of it, so upload it again", which for a refused file is false twice over, and
// uploads.go says exactly why every consumer of the unverified state must stop
// saying it. See uploads.Store.Objection, which owns both halves.
func (s *Server) pullUploadUnchecked(src *db.Source) string {
	if src == nil || src.Ingest.Mode != db.IngestPull {
		return ""
	}
	name, ok := uploads.UploadFromPullURL(src.Ingest.Pull.URL)
	if !ok {
		return ""
	}
	// Built only for a pull source that names an upload, which is what keeps a
	// listing of RTMP and SRT programmes from paying an os.MkdirAll per row.
	store, err := s.uploadStore()
	if err != nil {
		// Nothing to SAY, rather than a claim that all is well. The save-time
		// gates fail closed here because they can refuse; a card has no honest
		// refusal to make, and inventing a warning out of "the data directory
		// is missing" would put the wrong sentence in front of the operator.
		return ""
	}
	what, remedy, bad := uploadObjection(store, name)
	if !bad {
		return ""
	}
	return fmt.Sprintf("this source pulls from %q, which %s; %s", name, what, remedy)
}
