package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/playlistmedia"
	"github.com/rainmanjam/polyemesis/internal/uploads"
)

// Media upload: put a file on the server without shell access to the box.
//
// This is the only endpoint where a remote caller supplies both the bytes and
// a filename, which is the shape ../../SECURITY.md's path-confinement section
// exists to defend against. The rules, all enforced in internal/uploads:
//
//   - the client's filename is a HINT and is discarded; the server names the file
//   - the body streams to a temp file and is renamed on success, so a cancelled
//     or oversized upload leaves nothing that looks selectable
//   - free space is checked BEFORE the write, because an upload can fill the
//     volume the database and the recorder live on
//
// Uploading and deleting are registered in the session-only router group in
// api.go, so an API token cannot reach them: tokens are for automation, and
// writing arbitrary bytes to the server's disk is not something a leaked one
// should reach. Listing is not in that group and stays token-reachable.
//
// This comment used to say "behind the same session + CSRF middleware as every
// other mutation", which was wrong in the way that matters: requireCSRF passes
// a token principal through deliberately, so the CSRF layer was never a session
// check and the routes had no session check at all. #140 is what that cost. If
// you add another route here, the group in api.go is what decides who reaches
// it -- this paragraph decides nothing.

const (
	// MaxUploadBytes caps one upload. 8 GiB is a couple of hours of a decent
	// broadcast and far beyond any plausible pre-recorded segment; the point is
	// to bound the write, not to be generous.
	MaxUploadBytes = 8 << 30

	// UploadFreeFloor is how much room must remain AFTER the request is
	// accepted. Same reasoning as the recorder's free-space guard: a volume
	// filled to zero does not fail alone, it takes the database and the HLS
	// preview with it.
	UploadFreeFloor = 2 << 30

	// uploadFieldName is the multipart field carrying the file.
	uploadFieldName = "file"
)

// handleUploadMedia accepts one multipart file.
func (s *Server) handleUploadMedia(w http.ResponseWriter, r *http.Request) {
	store, err := s.uploadStore()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// MaxBytesReader on the whole body, not just the file part. The multipart
	// envelope is caller-controlled too, so a body that is 99% headers would
	// otherwise be read in full before the file limit ever applied.
	r.Body = http.MaxBytesReader(w, r.Body, MaxUploadBytes+(1<<20))

	// Stream the parts rather than ParseMultipartForm, which buffers to memory
	// up to its limit and spills the rest to a temp file we would not control.
	// A multi-gigabyte upload must never be an allocation.
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "expected a multipart/form-data body")
		return
	}

	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// A truncated body is the common case here -- a cancelled upload --
			// and is a client problem rather than a server one.
			writeError(w, http.StatusBadRequest, "malformed multipart body")
			return
		}
		if part.FormName() != uploadFieldName {
			part.Close()
			continue
		}

		// STAGED, THEN PROBED, THEN PUBLISHED, and that order is the fix rather
		// than a tidy-up.
		//
		// Nothing checked this before. The extension allowlist looks like a
		// gate and is not one -- SafeName only uses it to decide what to keep
		// from the client's filename, and anything unrecognised is stored as
		// ".bin" and still listed. A PDF, a zip or a truncated download all
		// landed in the Library looking exactly like a video, and the first
		// sign of trouble was a playlist normalise job failing, or the file
		// reaching air.
		//
		// The first version of this fix probed AFTER store.Save, which renames
		// the file into place. For the length of the probe -- tens of
		// milliseconds usually, up to probeUploadTimeout -- the file was listed
		// by GET /api/v1/media with a working pullUrl, and PUT /api/v1/settings
		// would accept it as a playlist item. Rejecting then deleted it with a
		// raw store.Delete, taking neither settingsMu nor the in-use check, and
		// left a stored playlist item naming a file that no longer exists:
		// exactly the state handleDeleteMedia holds a global lock to prevent
		// and answers 409 for. Driven end to end, twice.
		//
		// Staging first closes it at the source rather than by adding a second
		// lock. A Pending is under ".partial-", which List skips, so there is
		// no window in which a file that is about to be refused is visible, no
		// pullUrl for it, and nothing for a settings save to reference.
		//
		// The reject is the point, but the numbers it collects on the way are
		// what the Library then shows: an operator choosing between two similar
		// files could previously see a name, a size and a date, none of which
		// say whether the thing carries the three audio tracks they are about
		// to route.
		pending, err := store.Stage(part, part.FileName(), MaxUploadBytes, UploadFreeFloor)
		part.Close()
		if err != nil {
			writeUploadError(w, s.log, err)
			return
		}
		// Safe next to the Commit below: Discard is a no-op once committed.
		defer func() { _ = pending.Discard() }()

		verdict, probeErr := s.probeUpload(r.Context(), pending.Path(), pending.Name())
		if probeErr != nil {
			// THE FULL SENTENCE IS LOGGED; THE SCRUBBED ONE IS RETURNED.
			//
			// ffprobe names its input in almost every diagnostic it prints, and
			// probeUpload passes those words through on purpose -- "moov atom not
			// found" is what tells an operator their download was truncated. What
			// came with them was the absolute path of the staged file, which is
			// the server's data directory plus an internal ".partial-" name, in
			// the 400 body. That was the last unscrubbed subprocess-stderr-to-
			// client path in a repo that had just been through argv scrubbing and
			// path-disclosure removal (#181).
			//
			// Scrubbed HERE, at the egress, rather than inside probeUpload: this
			// is the one place a probe error becomes a response body, so a refusal
			// shape added to probeUpload later is covered without anybody
			// remembering to scrub it. The same reasoning as writeUploadError's
			// default arm and internal/api/redact.go -- redact where the bytes
			// leave, not where they are made.
			s.log.Info("media rejected", "name", pending.Name(), "err", probeErr)
			writeError(w, http.StatusBadRequest,
				scrubProbePaths(probeErr.Error(), pending.Path(), pending.Name(), s.cfg.DataDir))
			return
		}

		// THE VERDICT GOES DOWN WITH THE FILE, in one call, before the file is
		// listable. See uploads.Pending.Commit. The two used to be separate --
		// commit, then best-effort write the metadata -- which meant an upload
		// nobody had inspected and an upload whose record had not landed yet
		// were the same thing on disk, and "no record" could therefore mean
		// nothing at all.
		file, err := pending.Commit(verdict)
		if err != nil {
			// The detail is LOGGED, not returned. Every error that reaches here
			// carries an absolute server path -- the data directory, the
			// ".partial-" temp name, or both -- and the caller who just filled
			// the volume is the last person who needs the layout of it. See
			// writeUploadError, which had the same leak on the same path.
			s.log.Error("could not finalise an upload", "name", pending.Name(), "err", err)
			writeError(w, http.StatusInternalServerError, "this upload could not be stored")
			return
		}
		if !verdict.Verified {
			// STATED AT WARN AND STATED IN THE RESPONSE. The log line alone was
			// the whole of the previous design and it is not enough: nothing in
			// the product reads logs, so a file accepted unchecked was
			// indistinguishable from one that passed to every consumer that
			// mattered. The record on disk is what fixes that; this line is for
			// the operator watching the process.
			s.log.Warn("an upload was stored without being inspected",
				"name", file.Name, "reason", verdict.Reason)
		}
		s.log.Info("media uploaded",
			"name", file.Name, "bytes", file.Bytes, "origin", file.Origin,
			"verified", file.Verified)
		writeJSON(w, http.StatusCreated, file)
		return
	}

	writeError(w, http.StatusBadRequest,
		fmt.Sprintf("no %q part in the multipart body", uploadFieldName))
}

// probePathPlaceholder stands in for the staged file when a probe message names
// it and there is no operator-facing name to put there instead.
const probePathPlaceholder = "the uploaded file"

// scrubProbePaths removes this server's paths from a probe diagnostic, keeping
// the rest of ffprobe's sentence.
//
// THE PATH IS THE DISCLOSURE, NOT THE MESSAGE. ffprobe prints its input's name
// in front of nearly everything it says -- `/srv/data/uploads/.partial-1216776868.ts:
// Invalid data found when processing input` -- and that string went into the 400
// body verbatim. A blanket "this file could not be read" would cost the operator
// the only actionable half of it, so the path is replaced and the words are
// kept.
//
// staged is replaced with the name the file WOULD have been given, which is the
// name the operator typed and the only one they can match against anything they
// can see; the ".partial-" temp name appears nowhere else in the product. The
// directory and the data directory are then removed on their own, because a
// message can name the directory without the file (a permission error on the
// uploads directory, say) and because the replacements must be complete rather
// than cover the one shape that was measured.
//
// Longest first, and that ordering is load-bearing: dataDir is a prefix of the
// staged file's directory, which is a prefix of the staged path, so removing the
// short one first would leave the tails of the long ones behind.
func scrubProbePaths(msg, staged, name, dataDir string) string {
	display := strings.TrimSpace(name)
	if display == "" {
		display = probePathPlaceholder
	}
	replace := func(old, new string) {
		if strings.TrimSpace(old) == "" {
			return
		}
		msg = strings.ReplaceAll(msg, old, new)
		// The absolute spelling as well as the one we were handed. uploadStore
		// resolves the data directory with filepath.Abs before building the
		// store, so these are normally the same string -- but a relative
		// s.cfg.DataDir makes them differ, and the one ffprobe was handed is the
		// absolute one.
		if abs, err := filepath.Abs(old); err == nil && abs != old {
			msg = strings.ReplaceAll(msg, abs, new)
		}
	}
	replace(staged, display)
	if staged != "" {
		if dir := filepath.Dir(staged); dir != "" && dir != "." {
			replace(dir+string(filepath.Separator), "")
			replace(dir, "")
		}
	}
	if dir := strings.TrimSpace(dataDir); dir != "" {
		replace(strings.TrimSuffix(dir, string(filepath.Separator))+string(filepath.Separator), "")
		replace(strings.TrimSuffix(dir, string(filepath.Separator)), "")
	}
	return msg
}

// probeUploadTimeout bounds the inspection of one stored file.
//
// Generous, because this runs against a file on local disk that may be several
// gigabytes and on a machine that is also encoding a live broadcast. The cost
// of being too tight is rejecting a perfectly good upload, which is worse than
// the cost of waiting.
//
// YES, THIS RUNS A SUBPROCESS INSIDE AN HTTP HANDLER, AND playlist_normalise.go
// ARGUES AGAINST DOING THAT. Both are right and the reasoning is written out in
// full at the top of that file; the short version is that the two are different
// work. A probe is a bounded HEADER read whose answer IS the response -- the
// handler has to decide before it can reply, and deferring the decision means
// publishing the file first, which is the publish-before-probe window #118
// removed. A normalise is an unbounded TRANSCODE of every frame whose answer is
// a file on disk, which no request is waiting for. #188 filed the fact that
// neither comment said so; a reader who found one first concluded the other
// code was wrong.
//
// What keeps this side of the line defensible is that both bounds are real:
// this constant caps one probe, and MaxConcurrentUploadProbes caps how many run
// at once. Raising either without the other is what would make the comparison
// stop holding.
const probeUploadTimeout = 30 * time.Second

// MaxConcurrentUploadProbes bounds how many ffprobe children the upload
// endpoint may have alive at once, across every session.
//
// Package-level rather than per-Server, because the resource being bounded is
// the MACHINE -- cores that a live encode is also using, and process slots --
// and a process serves one machine. Four is chosen the same way NormaliseLimit
// is chosen: this is work that competes with an encoder that must not stutter,
// and more of it in parallel is not more throughput.
//
// The bound is on CHILDREN. The request goroutines still queue behind it, and
// bounding those is a separate job for a middleware that does not exist yet --
// issue #203, which also records what would make the residual unsafe. What this
// stops is 25 uploads becoming 25 simultaneous subprocesses.
const MaxConcurrentUploadProbes = 4

var probeSlots = make(chan struct{}, MaxConcurrentUploadProbes)

// probeUpload inspects staged bytes and reports the VERDICT to store beside
// them.
//
// A nil error with an unverified verdict means "could not check" rather than
// "not media": with no ffprobe on the box there is nothing to judge with, and
// refusing every upload because the server cannot inspect them would break a
// working install for the sake of a check it cannot perform.
//
// THE THIRD STATE IS NOW WRITTEN DOWN, and that is what this function returns
// rather than the (nil, nil) it used to. An upload is in exactly one of three
// states -- inspected and accepted, refused, or STORED WITHOUT BEING INSPECTED
// -- and the third one is reachable on demand by a remote client, because the
// context this probe runs under is the REQUEST's context and the client decides
// when that ends. Send a complete multipart body, RST the socket, and the probe
// is cancelled, and a cancelled probe is not a verdict, so the bytes are kept.
// That is the correct outcome for the bytes and it was an invisible one for
// everything downstream: `media` was simply absent from the listing, which is
// also how every upload predating this feature looks, so a 44-byte ffconcat
// script published this way was byte-identical in the API to a real file and
// was a legal playlist item. Driven over a real socket, twice.
//
// The fix is not to go back to deleting. It is to make the state SAYABLE:
// uploads.Verdict, written beside the file before the file exists under its
// final name, and read by every consumer that used to assume stored implied
// checked. See uploads.Verdict and playlistUploadProblems.
//
// THE GATE CLOSES WHEN FFPROBE RAN AND DISAGREED, AND ONLY THEN. That sentence
// used to be here as a description and was false: every non-nil error took the
// reject path, including a context error, and the reject path deleted the file.
// So a client disconnecting after the bytes had already landed -- routine on an
// 8 GiB limit with no WriteTimeout and proxies in front -- answered 400 and
// DESTROYED a perfectly good upload while asserting something false about it.
// Reproduced end to end before it was fixed; the body was
//
//	{"error":"this file could not be read as media: signal: killed"}
//
// and the uploads directory was empty afterwards.
//
// Interruption is now a third answer, and it joins the could-not-check path:
// the probe did not run, so it did not disagree, so there is nothing to refuse
// on. Both interruptions land here and the difference is worth knowing:
//
//   - the client went away (context.Canceled). Nobody is listening for the
//     response. Keeping the bytes is the only outcome that does not throw away
//     the operator's completed transfer.
//   - probeUploadTimeout expired (context.DeadlineExceeded). This is a real
//     hole and is stated rather than hidden: a file that takes ffprobe more
//     than 30 seconds is accepted unchecked. It is the same hole as having no
//     ffprobe at all, reached by a different route, and closing it by deleting
//     instead would mean a slow disk deletes valid media. It is logged at Warn,
//     which is the only way to notice it.
//
// path is the staged file to inspect; name is what it will be CALLED if it is
// accepted, and every log line here uses the second one. The first is a
// ".partial-" temp name that appears nowhere else in the product, so an
// operator who greps for the warning below could not match it to anything they
// can see in the Library.
func (s *Server) probeUpload(ctx context.Context, path, name string) (uploads.Verdict, error) {
	bin := s.probeBin
	if bin == "" {
		// EVERY ONE OF THESE LOGS BEFORE IT RETURNS, and that is the whole
		// reason unchecked is a survivable outcome.
		//
		// docs/TROUBLESHOOTING.md tells an operator how to find out whether
		// their uploads are being checked. It used to point at the startup log,
		// which says nothing of the sort: main.go logs ffmpeg's path and never
		// ffprobe's, and no line anywhere reports the engine coming up. So the
		// documented way to detect a silently-open gate did not exist, in the
		// same paragraph that introduced the gate. The line below is what the
		// documentation now points at, and it is per upload rather than per
		// boot, so it appears next to the upload it applies to.
		unchecked := func(reason string) (uploads.Verdict, error) {
			s.log.Warn("no ffprobe available; accepting this upload unchecked",
				"reason", reason, "name", name)
			// The operator-facing sentence is uploads.ReasonNoProber, which is
			// what goes on disk and into the API; reason above is the internal
			// detail, which stays in the log where it is useful.
			return uploads.UnverifiedVerdict(uploads.ReasonNoProber), nil
		}
		// s.mgr is nil in every test in this package, and Manager.Default takes
		// a read lock on the manager, so an unguarded s.eng() turns POST
		// /api/v1/media into a panic under `go test ./internal/api`. It is
		// reachable on a real install too: Manager.reconcile logs and continues
		// when engine.New fails, so an install whose video pipeline will not
		// build has no default engine -- and refusing every upload because of
		// that would be a worse outage than the one it is guarding against.
		if s.mgr == nil {
			return unchecked("this build has no engine manager")
		}
		eng := s.eng()
		if eng == nil {
			return unchecked("no default engine")
		}
		tools := eng.Tools()
		if tools == nil || tools.FFprobe == "" {
			return unchecked("the engine reports no ffprobe binary")
		}
		bin = tools.FFprobe
	}
	timeout := s.probeTimeout
	if timeout <= 0 {
		timeout = probeUploadTimeout
	}

	// ONE SLOT PER CONCURRENT PROBE, AND THE WAIT IS OUTSIDE THE TIMEOUT.
	//
	// Nothing bounded this: 25 concurrent uploads spawned 25 ffprobe children and
	// held 25 request goroutines for the full 30 seconds each, measured. The same
	// branch argues in playlist_normalise.go that a synchronous subprocess in the
	// request path is "a price this endpoint should not pay" -- this endpoint pays
	// it, so the least it can do is pay it a bounded number of times at once.
	//
	// The wait USED TO COUNT against the probe's own deadline, and that made the
	// semaphore the third instance in one change of the shape this whole design
	// exists to remove: A BRANCH WHOSE TAKEN-NESS A REMOTE CALLER CHOOSES (#216).
	// ctx.Err() let a remote disconnect decide whether the gate ran; a remote
	// DELETE let a caller decide which of four states a file was in; and a
	// four-deep semaphore inside a 30-second budget let REMOTE CONCURRENCY decide.
	// Sixteen concurrent uploads meant twelve waiting, and a caller who wanted
	// their file to go uninspected no longer needed to disconnect -- they needed
	// eleven tabs. Measured: with a 2s budget and the four slots held, a single
	// upload came back verified:false, reason "interrupted", having never been
	// looked at.
	//
	// So the queue is now a QUEUE. The wait runs on the REQUEST's context, which
	// bounds it by the only thing that legitimately ends it -- the client going
	// away, which is F1's already-recorded state -- and the deadline starts when
	// the probe does. That separates "the machine is busy", which is a delay, from
	// "this file could not be inspected", which is a verdict; they used to be the
	// same outcome. A busy server now makes an upload SLOW rather than UNCHECKED.
	//
	// What this does NOT do is bound the request goroutines themselves: sixteen
	// uploads still hold sixteen goroutines, now for longer each. That is the same
	// residual the bound had before -- the bound was always on CHILDREN -- and it
	// belongs to the middleware filed as #203. Trading a goroutine for a verdict
	// is the right way round: a goroutine costs a stack, and the alternative costs
	// the inspection that every consumer downstream assumes happened.
	select {
	case probeSlots <- struct{}{}:
		defer func() { <-probeSlots }()
	case <-ctx.Done():
		s.log.Warn("upload probe was interrupted; accepting the file unchecked",
			"name", name, "cause", ctx.Err(), "err", "waiting for a probe slot")
		return uploads.UnverifiedVerdict(uploads.ReasonInterrupted), nil
	}

	// The deadline starts HERE, with the slot in hand, so it bounds the probe and
	// nothing else. Shadowing ctx deliberately: everything below this line asks
	// the probe's own context whether the probe was cut short, and asking the
	// request's would answer a different question.
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	res, err := ffmpeg.ProbeFile(ctx, bin, path)
	if err != nil {
		// Interruption first, because it is not a verdict about the file.
		//
		// CTX.ERR() IS THE CLAUSE THAT ACTUALLY WORKS, and the errors.Is arms
		// alone do not. Measured: with only the errors.Is arms, the
		// client-disconnect reproduction still answers 400 "signal: killed" and
		// still deletes the file. exec.CommandContext kills the child, and what
		// comes back is a plain *exec.ExitError carrying no context error to
		// match on -- so errors.Is finds nothing.
		//
		// Asking the context we handed the probe whether it is done answers the
		// actual question, "was this probe cut short", without depending on how
		// the os/exec of the day chooses to report it. The errors.Is arms stay
		// for the cases where the chain IS present, which is why ProbeFile now
		// wraps with %w on both of its error branches.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
			ctx.Err() != nil {
			s.log.Warn("upload probe was interrupted; accepting the file unchecked",
				"name", name, "cause", ctx.Err(), "err", err)
			return uploads.UnverifiedVerdict(uploads.ReasonInterrupted), nil
		}
		// ASKED AS ONE QUESTION, not as a sequence of arms. ffmpeg.Refused owns
		// the list of "ffprobe ran and this is a verdict about the file",
		// because the default below is FAIL-OPEN and a handler has no way to
		// know when it has missed a case. Measured: with the
		// ErrUnsupportedContainer arm disabled, a GIF was not refused with a
		// worse message -- it was stored, 201, unchecked.
		if ffmpeg.Refused(err) {
			switch {
			case errors.Is(err, ffmpeg.ErrIndirectContainer):
				// Not "could not read": ffprobe read it fine and reported
				// another file's streams. Saying "could not be read as media"
				// here would be the false diagnosis all over again.
				return uploads.Verdict{}, errors.New(
					"this file is a playlist or script naming other files, not media itself")
			case errors.Is(err, ffmpeg.ErrUnsupportedContainer):
				// A REAL FILE IN A FORMAT WE DO NOT TAKE, which used to get the
				// sentence above. AIFF, DV, y4m, IVF, CAF and GIF were all
				// measured being told they were "a playlist or script naming
				// other files" -- untrue about every one of them, and
				// unactionable, because the operator goes looking for a script
				// that does not exist. If an operator is refused here for
				// something they should be able to upload, the fix is an entry
				// in ffmpeg.selfContainedFormats, not a widening of the gate.
				return uploads.Verdict{}, fmt.Errorf("%s; re-save it as MP4 or MPEG-TS", err)
			}
			// A refusal this handler has no sentence of its own for. ffprobe's
			// words, and a REJECTION -- the point of routing through Refused is
			// that a new refusal shape lands here rather than in the fail-open
			// arm below.
			return uploads.Verdict{}, fmt.Errorf("this file could not be read as media: %s", err)
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			// FFPROBE NEVER RAN, OR RAN AND SAID SOMETHING THIS PROCESS CANNOT
			// READ. Both are facts about this server, and neither is a verdict
			// about the operator's bytes -- which is the same rule the
			// interruption arm above enforces, on the routes that arm does not
			// reach.
			//
			// It was reachable and it destroyed data: with probeBin pointing at
			// a path that does not exist, is not executable, or is a directory,
			// every upload got 400 "this file could not be read as media:
			// fork/exec ...: no such file or directory" and the uploads
			// directory was empty afterwards. exec's start failures are
			// *exec.Error and fs.ErrPermission, never *exec.ExitError, and
			// ctx.Err() is nil for them, so both existing guards missed. A fork
			// that fails with EAGAIN on a box that is also encoding live video
			// reaches this without any misconfiguration at all.
			//
			// A probe that exits 0 and prints something that is not JSON lands
			// here too, and for the same reason: "parse ffprobe output: invalid
			// character 'o'" is a sentence about the binary this server was
			// pointed at, and it was being reported as a verdict about a file.
			s.log.Warn("the upload probe could not be run; accepting the file unchecked",
				"name", name, "err", err)
			return uploads.UnverifiedVerdict(uploads.ReasonProbeUnusable), nil
		}
		// ffprobe's own words, because "could not read this file" tells the
		// operator nothing they can act on and ffprobe's message usually does.
		//
		// NOT A COMPLETENESS CHECK, and the comment here used to imply it was:
		// it said "moov atom not found tells somebody their download was
		// truncated", which is true of that one message and gets read as "a
		// truncated file is caught". It is not. Measured: an MP4 written with
		// -movflags +faststart and a Matroska file are both ACCEPTED when cut
		// to a tenth of their length, and both report the WHOLE file's
		// duration, because the header that carries it survived. Only an MP4
		// with its index at the end -- the default layout -- fails, and it
		// fails on the missing index rather than on being short. The boundary
		// is pinned by internal/ffmpeg.TestProbeFileAcceptsMostTruncatedMedia
		// and written down in docs/TROUBLESHOOTING.md.
		return uploads.Verdict{}, fmt.Errorf("this file could not be read as media: %s", err)
	}
	if res.Video == nil && len(res.Audio) == 0 {
		// ffprobe parsed it and found nothing playable. A container with no
		// streams is the shape a renamed archive or document arrives in.
		return uploads.Verdict{}, errors.New("this file carries no video or audio stream")
	}

	info := uploads.MediaInfo{
		DurationSeconds: res.DurationSeconds,
		AudioTracks:     len(res.Audio),
		ProbedAt:        time.Now().UTC(),
	}
	if res.Video != nil {
		info.VideoCodec = res.Video.Codec
		info.Width = res.Video.Width
		info.Height = res.Video.Height
		info.FrameRate = res.Video.FrameRate
	}
	if len(res.Audio) > 0 {
		// The first track's shape, which is what a listing has room for. The
		// count above is the number that matters for routing; per-track detail
		// belongs on a detail view, not in a table row.
		info.AudioCodec = res.Audio[0].Codec
		info.AudioChannels = res.Audio[0].Channels
		info.AudioLayout = res.Audio[0].Layout
	}
	return uploads.VerifiedVerdict(info), nil
}

// handleListMedia returns the stored uploads, newest first.
func (s *Server) handleListMedia(w http.ResponseWriter, r *http.Request) {
	store, err := s.uploadStore()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	list, err := store.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleDeleteMedia removes one stored upload, every derivative version made
// from it, and nothing else -- refusing outright when a stored playlist item
// still names the upload.
//
// THE IN-USE GUARD is new here, and the reason it can exist now is B2, not a
// change of mind about the risk. B1 shipped without one deliberately: the
// settings page had no playlist control and always PUTs the whole document
// back, so refusing a delete would have locked an operator out of every
// future settings save with no in-product way to clear the offending item --
// see playlistUploadProblems for that history. B2 ships the control that
// makes refusing defensible: an operator who hits the 409 below can go remove
// the item and retry, instead of being stuck.
//
// EVERY DERIVATIVE VERSION is removed, via playlistmedia.DerivativeVersions,
// rather than only the one name playlistmedia.DerivativePath computes today.
// ProfileVersion is at 2, so a v1 file can genuinely still be on disk beside a
// v2, and removing only the current name would orphan it with nothing left in
// the product that ever looks for it again.
//
// DerivativeVersions reads the directory and compares names; it does NOT build
// a glob. The name here is a URL path segment, and `*`, `?` and `[` are all
// legal in a filename, so a pattern built from it is a pattern the caller
// controls. That is not hypothetical: this handler previously globbed, and
// `DELETE /api/v1/media/%2A` removed every derivative in the install before the
// name was validated at all.
//
// RECONCILES afterward, like a settings save does, so a file the engine was
// relying on does not leave its view stale until the next unrelated change
// happens to trigger one.
func (s *Server) handleDeleteMedia(w http.ResponseWriter, r *http.Request) {
	store, err := s.uploadStore()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	name := chi.URLParam(r, "name")

	// settingsMu -- see its declaration on Server. Held across the reference
	// check below and the removal it gates, the same way handlePutSettings
	// holds it across its own check-and-store: without a shared lock, a PUT
	// that already passed playlistUploadProblems could still store a fresh
	// reference to this exact upload in the gap between the check and the
	// removal, which is the freshly-saved-item-points-at-nothing state this
	// guard exists to make impossible rather than merely unlikely.
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()

	if referenced, idx, err := s.uploadIsReferenced(name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	} else if referenced {
		writeError(w, http.StatusConflict, fmt.Sprintf(
			"playlist item %d names this upload; remove it from the playlist before deleting the file", idx))
		return
	}

	// THE NAME IS VALIDATED BEFORE ANYTHING IS REMOVED, and the ordering is the
	// point rather than a tidy-up.
	//
	// It used to be validated by store.Delete AFTER the sweep below, which is
	// how `DELETE /api/v1/media/%2A` managed to destroy every derivative in the
	// install and then answer "no such upload". That instance is closed --
	// DerivativeVersions matches by equality and builds no pattern -- but the
	// ORDERING is what closes the class, and one instance of a class is not the
	// class.
	//
	// The remaining reachable case is Windows: filepath.Base(`..\victim.ts`) is
	// `victim.ts` on that platform and the raw name is what uploadIsReferenced
	// compares, so a traversal spelled with a backslash slips past the in-use
	// guard and reaches the sweep. Resolving first makes the guard's answer and
	// the sweep's target the same name on every platform.
	// AND THE NAME HAS TO BE ONE THE LIBRARY LISTS. Resolve only refuses
	// separators, so `.probe-<name>.json` was a legal {name} here -- and deleting
	// a verdict sidecar is a PRIVILEGE UPGRADE, because the design's load-bearing
	// distinction is "recorded unverified" versus "no record at all", and
	// removing the record moves a file from the first to the second.
	//
	// Measured through the real router: DELETE /api/v1/media/.probe-attack.ts.json
	// returned 204, the listing went from UnverifiedReason set to
	// UnverifiedReason empty, and a settings save that was 400 before the delete
	// became 200 after it. The same session-holder who created an unchecked file
	// could erase the evidence that it was unchecked.
	//
	// uploads.Listable is asked because its own comment says the rule has one
	// home; this was the caller that did not go there. It also covers the
	// `.partial-` name of an upload still in flight.
	if !uploads.Listable(name) {
		writeError(w, http.StatusBadRequest, "no such upload")
		return
	}
	if _, err := store.Resolve(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Derivatives before the upload itself: if the process died between the
	// two removals below, an orphaned derivative next to an upload that is
	// still there is a smaller problem to notice than a deleted upload whose
	// derivative is still on disk claiming to be current.
	matches, err := playlistmedia.DerivativeVersions(s.cfg.DataDir, name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, m := range matches {
		if err := os.Remove(m); err != nil && !os.IsNotExist(err) {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	if err := store.Delete(name); err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "no such upload")
			return
		}
		// Resolve's rejections land here, and they are the caller's fault:
		// a name with a separator in it is a bad request, not a server error.
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Optional, like every other piece of the post-production tier: a test
	// fixture that never touches the engine must still be able to delete a
	// file (see testServer's comment). A running server always has one.
	if s.mgr != nil {
		if err := s.mgr.Reconcile(); err != nil {
			writeError(w, http.StatusInternalServerError, "media deleted but reconcile failed: "+err.Error())
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// uploadStore returns the store rooted at the configured data directory.
//
// Built per call rather than held on Server: it owns no state beyond a path,
// and constructing it here means a data directory that appears after startup
// still works rather than being cached as missing.
func (s *Server) uploadStore() (*uploads.Store, error) {
	dir := s.cfg.DataDir
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("no data directory is configured")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	return uploads.New(abs)
}

// writeUploadError maps a store error to the status the caller deserves.
//
// Each of these is the caller's problem or the machine's, and they are told
// apart because "your file is too big" and "this server has no disk left" call
// for completely different responses from whoever is looking at the toast.
//
// THE DEFAULT ARM NO LONGER ECHOES THE ERROR. Everything that reaches it is an
// os.PathError over a path this server chose, so the body carried the absolute
// data directory and the internal ".partial-" temp name out to whoever sent the
// request. It was reachable without any misconfiguration: filling the volume
// mid-write produced `write upload: write /srv/data/uploads/.partial-1216776868.ts:
// no space left on device` with a 500 attached, where the pre-check path for the
// same condition is careful to answer 507 and say nothing about paths. That
// particular case is now classified as ErrNoSpace at the source
// (uploads.Stage); this arm covers the rest of the class rather than the one
// instance of it.
func writeUploadError(w http.ResponseWriter, log *slog.Logger, err error) {
	switch {
	case errors.Is(err, uploads.ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("upload exceeds the %d GiB limit", MaxUploadBytes>>30))
	case errors.Is(err, uploads.ErrEmpty):
		writeError(w, http.StatusBadRequest, "the uploaded file is empty")
	case errors.Is(err, uploads.ErrNoSpace):
		// 507, not 500: the request was fine and the server cannot store it,
		// which is exactly what Insufficient Storage means.
		writeError(w, http.StatusInsufficientStorage,
			"not enough free disk space to store this upload")
	default:
		if log != nil {
			log.Error("an upload could not be stored", "err", err)
		}
		writeError(w, http.StatusInternalServerError, "this upload could not be stored")
	}
}
