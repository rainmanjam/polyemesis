package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"

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
		s.log.Info("media uploaded",
			"name", file.Name, "bytes", file.Bytes, "origin", file.Origin)
		writeJSON(w, http.StatusCreated, file)
		return
	}

	writeError(w, http.StatusBadRequest,
		fmt.Sprintf("no %q part in the multipart body", uploadFieldName))
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
// EVERY DERIVATIVE VERSION is removed, via playlistmedia.DerivativeGlob,
// rather than only the one name playlistmedia.DerivativePath would compute
// right now. See DerivativeGlob's own comment for why it is written against a
// versioned naming scheme even though nothing produces a versioned derivative
// yet: the point is that removing only today's exact name would silently stop
// being enough the day something does, orphaning every earlier version with
// nothing left in the product that ever looks for them again.
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

	// Derivatives before the upload itself: if the process died between the
	// two removals below, an orphaned derivative next to an upload that is
	// still there is a smaller problem to notice than a deleted upload whose
	// derivative is still on disk claiming to be current.
	matches, err := filepath.Glob(playlistmedia.DerivativeGlob(s.cfg.DataDir, name))
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
