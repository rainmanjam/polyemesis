package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
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
// It sits behind the same session + CSRF middleware as every other mutation, so
// an API token cannot upload: tokens are for automation, and writing arbitrary
// bytes to the server's disk is not something a leaked token should reach.

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

		file, err := store.Save(part, part.FileName(), MaxUploadBytes, UploadFreeFloor)
		part.Close()
		if err != nil {
			writeUploadError(w, err)
			return
		}
		// Probed before it is reported as accepted, and removed again if it is
		// not media.
		//
		// Nothing checked this before. The extension allowlist looks like a
		// gate and is not one -- SafeName only uses it to decide what to keep
		// from the client's filename, and anything unrecognised is stored as
		// ".bin" and still listed. A PDF, a zip or a truncated download all
		// landed in the Library looking exactly like a video, and the first
		// sign of trouble was a playlist normalise job failing, or the file
		// reaching air.
		//
		// The reject is the point, but the numbers it collects on the way are
		// what the Library then shows: an operator choosing between two similar
		// files could previously see a name, a size and a date, none of which
		// say whether the thing carries the three audio tracks they are about
		// to route.
		if info, probeErr := s.probeUpload(r.Context(), store, file.Name); probeErr != nil {
			if delErr := store.Delete(file.Name); delErr != nil {
				// Worth a line: the request is answered either way, but a file
				// nothing will ever list is now occupying the volume.
				s.log.Warn("could not remove a rejected upload",
					"name", file.Name, "err", delErr)
			}
			s.log.Info("media rejected", "name", file.Name, "err", probeErr)
			writeError(w, http.StatusBadRequest, probeErr.Error())
			return
		} else if info != nil {
			// Best-effort: the file is good and already stored, so failing the
			// upload because a cache could not be written would throw away
			// minutes of the operator's time to protect a nicety.
			if err := store.PutMedia(file.Name, *info); err != nil {
				s.log.Warn("could not record media info", "name", file.Name, "err", err)
			}
			file.Media = info
		}
		s.log.Info("media uploaded",
			"name", file.Name, "bytes", file.Bytes, "origin", file.Origin)
		writeJSON(w, http.StatusCreated, file)
		return
	}

	writeError(w, http.StatusBadRequest,
		fmt.Sprintf("no %q part in the multipart body", uploadFieldName))
}

// probeUploadTimeout bounds the inspection of one stored file.
//
// Generous, because this runs against a file on local disk that may be several
// gigabytes and on a machine that is also encoding a live broadcast. The cost
// of being too tight is rejecting a perfectly good upload, which is worse than
// the cost of waiting.
const probeUploadTimeout = 30 * time.Second

// probeUpload inspects a freshly stored file and reports whether it is media.
//
// A nil error with a nil result means "could not check" rather than "not
// media": with no ffprobe on the box there is nothing to judge with, and
// refusing every upload because the server cannot inspect them would break a
// working install for the sake of a check it cannot perform. The gate closes
// only when ffprobe ran and disagreed.
func (s *Server) probeUpload(ctx context.Context, store *uploads.Store, name string) (*uploads.MediaInfo, error) {
	bin := s.probeBin
	if bin == "" {
		// s.mgr is nil in every test in this package, and Manager.Default takes
		// a read lock on the manager, so an unguarded s.eng() turns POST
		// /api/v1/media into a panic under `go test ./internal/api`. It is
		// reachable on a real install too: Manager.reconcile logs and continues
		// when engine.New fails, so an install whose video pipeline will not
		// build has no default engine -- and refusing every upload because of
		// that would be a worse outage than the one it is guarding against.
		if s.mgr == nil {
			return nil, nil
		}
		eng := s.eng()
		if eng == nil {
			return nil, nil
		}
		tools := eng.Tools()
		if tools == nil || tools.FFprobe == "" {
			return nil, nil
		}
		bin = tools.FFprobe
	}
	path, err := store.Resolve(name)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, probeUploadTimeout)
	defer cancel()

	res, err := ffmpeg.ProbeFile(ctx, bin, path)
	if err != nil {
		// ffprobe's own words. "moov atom not found" tells somebody their
		// download was truncated; "could not read this file" tells them
		// nothing they can act on.
		return nil, fmt.Errorf("this file could not be read as media: %s", err)
	}
	if res.Video == nil && len(res.Audio) == 0 {
		// ffprobe parsed it and found nothing playable. A container with no
		// streams is the shape a renamed archive or document arrives in.
		return nil, errors.New("this file carries no video or audio stream")
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
	return &info, nil
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
func writeUploadError(w http.ResponseWriter, err error) {
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
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}
