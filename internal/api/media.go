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

// handleDeleteMedia removes one stored upload.
func (s *Server) handleDeleteMedia(w http.ResponseWriter, r *http.Request) {
	store, err := s.uploadStore()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	name := chi.URLParam(r, "name")
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
