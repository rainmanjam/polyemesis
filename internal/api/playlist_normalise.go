package api

import (
	"fmt"
	"os"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/playlistmedia"
	"github.com/rainmanjam/polyemesis/internal/uploads"
)

// The two things a settings save has to do about a playlist beyond storing it:
// refuse an item that names an upload nobody has, and ask for the normalised
// derivative every item needs before the tier will start.
//
// Both live here rather than in internal/db because both need the uploads
// store, and internal/db must not have one. Putting a Store behind
// Settings.Validate would put an os.MkdirAll and a stat behind every
// db.GetSettings -- roughly twenty callers, several of them per-request
// handlers -- which is a defect this sub-project has already had to undo once.
// The API layer, by contrast, already builds a store per request
// (Server.uploadStore) and is where the operator is standing.

// A PROBE IS NOT A NORMALISE, AND THIS FILE IS NOT AN ARGUMENT AGAINST THE ONE
// media.go RUNS INLINE.
//
// enqueuePlaylistNormalisation's comment below says a synchronous subprocess in
// the request path is "a price this endpoint should not pay", and
// handleUploadMedia now runs ffprobe synchronously inside an HTTP handler for
// up to probeUploadTimeout. Read alone, either one condemns the other. #188 is
// that contradiction, and it is a documentation defect rather than a behaviour
// one: both are right, and the line between them is worth stating once so that
// nobody reconciles them by moving the wrong piece.
//
// The distinction is what the work IS, not where it runs:
//
//   - A PROBE IS A BOUNDED HEADER READ WHOSE ANSWER IS THE RESPONSE. It reads
//     metadata, not frames, and finishes in milliseconds on a file of any size.
//     The handler cannot defer it, because the answer to "is this media" IS the
//     201 or the 400 -- deferring it means answering 201 first and discovering
//     the file is an ffconcat script afterwards, which is the publish-before-
//     probe window #118 removed. It is bounded twice regardless:
//     probeUploadTimeout on each probe, MaxConcurrentUploadProbes on how many
//     may run at once.
//
//   - A NORMALISE IS AN UNBOUNDED TRANSCODE WHOSE ANSWER IS A FILE. It reads
//     and re-encodes every frame of a file that may be several gigabytes, on a
//     machine that may be carrying a live broadcast, and takes minutes to
//     hours. Nothing about the settings save depends on its result: the save
//     succeeds, and the playlist becomes ready later when the derivative
//     exists. Tying an HTTP request to it would hold a connection open for an
//     hour to report something the readiness gate already reports.
//
// SO THE FIX FOR EITHER IS NOT TO MOVE THE OTHER. Moving the upload probe into
// the queue would mean publishing the file before it could be scheduled, which
// re-creates the exact window that PR removed and in a worse form. Making
// normalisation synchronous would tie a request to a full transcode. The
// mirror of this paragraph is in media.go, above probeUploadTimeout.

// playlistUploadProblems reports why an item the operator is INTRODUCING
// cannot be used, or nil. want is the incoming playlist; stored is the one
// already saved.
//
// EXISTENCE, which db.PlaylistSettings.PlaylistFileProblem deliberately cannot
// check. The spec makes this binding -- "a missing upload is a settings error"
// -- and the reason is the failure it names as the worst one: without it an
// operator can save an item pointing at a file that is not there, get a 200,
// and then watch a playlist that never goes on air with nothing anywhere
// telling them why. The only trace would be an Info log line saying "not every
// item has been normalised yet", which is the wrong sentence for "that file
// does not exist".
//
// VALIDATION REJECTS WHAT THE OPERATOR IS INTRODUCING; IT MUST NOT PUNISH THEM
// FOR PRE-EXISTING STATE. That is why stored is a parameter, and the reason has
// been replaced once already without the scoping changing.
//
// It began as lockout avoidance, and that argument has EXPIRED: DELETE
// /api/v1/media/{name} had no in-use guard and the settings page had no
// playlist control, so deleting a file a saved item named made every
// subsequent PUT /settings 400 -- for an unrelated change like an alert
// threshold, with the playlist disabled, and with no in-product way to clear
// the item, because the page GETs the whole document and PUTs it back. B2
// closed both halves: handleDeleteMedia now refuses with 409 while a stored
// item names the upload (see uploadIsReferenced below), and PlaylistEditor is
// a real control that can remove the item.
//
// WHAT KEEPS THE SCOPING IS THAT AN INHERITED BROKEN ITEM IS NOT NECESSARILY
// BROKEN. The 409 stops an operator stranding an item through the product; it
// cannot stop a sweep, a tidied disk or a restore that missed a file. And since
// B2 the tier plays DERIVATIVES, not uploads -- an inherited item whose upload
// vanished out of band goes on playing from the derivative it already has (see
// engine.playlistItemsReady and TestAPlaylistPlaysOnWhenOnlyTheOriginalUploadIsGone).
// Checking every item unconditionally would refuse an unrelated settings save
// over an item that is on air at that moment.
//
// THE SAFETY PROPERTY IS NOT WEAKENED, but not by the mechanism this comment
// used to name. It once said a stale item still fails engine.playlistItemsReady
// "which stats the resolved upload"; Task 4 deliberately removed that stat, and
// readiness now asks only whether a derivative exists. The property survives
// through the derivative instead: an item the operator INTRODUCES naming an
// upload nobody has can never become ready by any route, because
// enqueuePlaylistNormalisation has no file to transcode, so no derivative is
// ever written, so the whole list stays off air. What the operator gets instead
// of silence is GET /failover/playlist, which names the item and the reason.
// Validation answers "may this be saved", readiness answers "may this go to
// air", and those are still different jobs.
//
// "Introducing" is by NAME rather than by position, so re-ordering an existing
// list is not mistaken for typing two new items. A name already somewhere in
// the stored list is state the operator inherited; anything else they are
// adding or editing now.
//
// Resolution goes through the uploads store, never a join, exactly as the
// engine's readiness gate does -- it is the boundary that makes items upload
// NAMES rather than paths.
func (s *Server) playlistUploadProblems(want, stored db.PlaylistSettings) error {
	if len(want.Items) == 0 {
		return nil
	}
	inherited := make(map[string]bool, len(stored.Items))
	for _, item := range stored.Items {
		inherited[db.PlaylistUploadName(item.Upload)] = true
	}

	var store *uploads.Store
	for i, item := range want.Items {
		name := db.PlaylistUploadName(item.Upload)
		if inherited[name] {
			continue
		}
		if store == nil {
			// Built lazily, so a save that introduces nothing does no
			// filesystem work at all -- which is most saves, since the settings
			// page PUTs the whole document back on every unrelated change.
			var err error
			if store, err = s.uploadStore(); err != nil {
				// Fail closed. Not being able to ask is not the same as the
				// answer being yes, and this only happens when the data
				// directory is unusable -- which the operator needs told
				// regardless.
				return fmt.Errorf("playlist items cannot be checked: %w", err)
			}
		}
		path, err := store.Resolve(name)
		if err != nil {
			return fmt.Errorf("playlist item %d: %w", i, err)
		}
		// Listable BEFORE Stat, and it is not a tidy-up.
		//
		// Stat asks "are there bytes at this path", which is not the question.
		// The uploads directory also holds ".partial-" files -- an upload whose
		// bytes have landed and which is being probed, and which will be
		// deleted in a moment if it is not media -- and ".probe-" sidecars,
		// which are JSON. Both stat perfectly well, neither is ever listed, and
		// a playlist item naming one is a reference to something the operator
		// cannot see and cannot remove from the Library.
		if !uploads.Listable(name) {
			return fmt.Errorf("playlist item %d: there is no upload named %q; "+
				"upload the file again or remove the item", i, name)
		}
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("playlist item %d: there is no upload named %q; "+
				"upload the file again or remove the item", i, name)
		}
		// STORED DOES NOT IMPLY CHECKED, and this is where that stops being an
		// assumption.
		//
		// A remote client decides whether the upload gate runs: the probe uses
		// the REQUEST's context, so sending a complete body and dropping the
		// connection interrupts it, and an interrupted probe is not a verdict,
		// so the bytes are kept. Keeping them is right. What was wrong is that
		// the result was then indistinguishable -- it stat'd, it was Listable,
		// and this function returned nil for it, which made a 44-byte ffconcat
		// script a legal playlist item on its way to an FFmpeg with no format
		// allowlist. Driven end to end over a real socket.
		//
		// So an item the operator is INTRODUCING whose upload carries a recorded
		// "nobody read this" is refused, with the remedy in the sentence.
		//
		// THE TEST IS "RECORDED AS UNVERIFIED", NOT "NOT RECORDED AS VERIFIED",
		// and the difference is deliberate. Every upload stored before verdicts
		// existed has no record at all, and refusing those would strand media an
		// operator has had for a year over a file that was never written. Those
		// are covered instead by the normalise worker, which re-runs the format
		// allowlist on whatever it is handed -- see playlistmedia's verifySource.
		// That is the gate that holds for files this validator never sees:
		// inherited items, which are skipped above by design, and anything
		// placed in the directory by hand.
		if v, recorded := store.Verdict(name); recorded && !v.Verified {
			return fmt.Errorf("playlist item %d: %q was stored without being checked "+
				"(%s), so it cannot be added to a playlist; upload it again", i, name, v.Reason)
		}
	}
	return nil
}

// uploadIsReferenced reports whether the STORED playlist -- not any document
// a caller is proposing -- names this upload, and at which index. It is the
// in-use guard DELETE /api/v1/media/{name} refuses to proceed past; see
// handleDeleteMedia.
//
// Every stored item is checked, unlike playlistUploadProblems above, which
// deliberately skips items the operator inherited rather than introduced.
// That distinction is about which items a SAVE may be refused for; a DELETE
// is not saving anything, so there is no "introducing" to compare against --
// an upload named anywhere in what is already on disk is in use.
func (s *Server) uploadIsReferenced(name string) (bool, int, error) {
	settings, err := s.store.GetSettings()
	if err != nil {
		return false, 0, fmt.Errorf("playlist reference cannot be checked: %w", err)
	}
	want := db.PlaylistUploadName(name)
	for i, item := range settings.Failover.Playlist.Items {
		if db.PlaylistUploadName(item.Upload) == want {
			return true, i, nil
		}
	}
	return false, 0, nil
}

// enqueuePlaylistNormalisation asks for the derivative every item needs.
//
// THIS IS THE ONLY PRODUCTION SUBMITTER OF playlistmedia.KindNormalise. The
// engine will not put a playlist on air until every item has a normalised
// derivative on disk, and nothing else in the product creates one, so without
// this call the readiness gate is a permanent refusal and the playlist feature
// cannot start at all.
//
// ONE DERIVATIVE PER UPLOAD, not per playlist entry, and THE QUEUE IS WHAT
// GUARANTEES THAT rather than a check here. NewNormaliseJob sets Unique with a
// Target of playlistmedia.NormaliseTarget(upload) -- keyed on the upload name
// and nothing else -- and Queue.Submit folds a Unique submission onto an
// already-active job with the same kind and target, returning it with
// created=false. So a playlist whose second and fifth entries are the same file
// submits twice and transcodes once. Deduplicating again in this loop would be
// a second copy of a rule that already has an owner, and a second place for it
// to drift from the target the worker is keyed on.
//
// An item whose derivative already exists is skipped rather than submitted.
// The worker would return "already normalised; nothing to do" in any case, but
// the queue's Unique fold only looks at ACTIVE jobs, so a finished one does not
// suppress a resubmission -- and every unrelated settings save would otherwise
// add a row per item to the job history for work that is already done.
//
// Called from the settings handler, never from the engine. Enqueuing from
// inside reconcilePlaylist would put a queue write under selMu, the lock an
// operator's failover POST already queues behind.
//
// Errors are logged, not returned. The settings ARE saved by the time this
// runs, and a queue that refused a submission must not turn a successful save
// into a 500 -- the operator would retry the save, which would succeed, and be
// no better informed. A job that never got queued shows up as a playlist that
// stays unavailable, and the log line here is what explains it.
func (s *Server) enqueuePlaylistNormalisation(p db.PlaylistSettings) {
	if s.jobq == nil || len(p.Items) == 0 {
		return
	}
	for _, item := range p.Items {
		// db.PlaylistUploadName, never a TrimSpace of our own: this and the
		// existence check above are the two internal/api sites the whitespace
		// rule has to reach, and a private copy here is how the engine and the
		// validator once came to disagree about what an item names.
		name := db.PlaylistUploadName(item.Upload)
		if name == "" {
			continue
		}
		if st, err := os.Stat(playlistmedia.DerivativePath(s.cfg.DataDir, name)); err == nil && st.Size() > 0 {
			continue
		}
		// DurationMS IS DELIBERATELY LEFT AT ZERO, and it costs something worth
		// naming: the job's progress bar cannot show a percentage, and the
		// disk guard falls back to estimating from the source's size rather
		// than from duration x bitrate, which it reports as an unbounded
		// estimate. Both are documented at NormaliseParams.DurationMS. The
		// alternative is running ffprobe on operator media inside an HTTP
		// handler, on a machine that may be carrying a live stream -- a
		// synchronous subprocess in the request path buys a nicer progress bar
		// at a price this endpoint should not pay. The worker probes the file
		// itself anyway, in the queue, where slow work belongs.
		job, err := playlistmedia.NewNormaliseJob(playlistmedia.NormaliseParams{Upload: name})
		if err != nil {
			s.log.Warn("cannot build a playlist normalisation job",
				"upload", name, "error", err)
			continue
		}
		queued, created, err := s.jobq.Submit(job)
		switch {
		case err != nil:
			s.log.Warn("cannot queue a playlist item for normalisation; "+
				"the playlist will not go on air until it has been normalised",
				"upload", name, "error", err)
		case created:
			s.log.Info("playlist item queued for normalisation",
				"upload", name, "job", queued.ID)
		}
	}
}
