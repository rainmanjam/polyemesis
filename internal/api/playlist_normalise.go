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
// FOR PRE-EXISTING STATE THEY HAVE NO CONTROL TO EDIT. That is why stored is a
// parameter. Checking every item unconditionally looked stricter and was
// strictly worse: DELETE /api/v1/media/{name} has no in-use guard, so deleting
// a file that a saved item names made EVERY subsequent PUT /settings 400 --
// with the playlist disabled, and for an unrelated change like an alert
// threshold. The operator could not clear it either, because the settings UI
// has no playlist control yet (FailoverSettings in ui/src/lib/types.ts carries
// no playlist field until B2) and the page GETs the whole document and PUTs it
// back, so the stale item round-trips untouched. Deleting a file bricked the
// settings page with no in-product recovery, which is worse than the problem
// this check was added to solve.
//
// THE SAFETY PROPERTY IS NOT WEAKENED, because validation is not the runtime
// gate. A stale item whose upload is gone still fails engine.playlistItemsReady
// -- which stats the resolved upload on every reconcile -- so the playlist
// still never goes on air, and the operator still cannot get a broken item PAST
// this check by introducing one. Validation answers "may this be saved", the
// readiness gate answers "may this go to air", and those are different jobs.
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
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("playlist item %d: there is no upload named %q; "+
				"upload the file again or remove the item", i, name)
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
