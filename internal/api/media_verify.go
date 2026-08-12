package api

import (
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rainmanjam/polyemesis/internal/uploads"
	"github.com/rainmanjam/polyemesis/internal/uploadverify"
)

// handleVerifyMedia queues a re-inspection of one stored upload.
//
// #202, and the route exists for one reason: until now, the Library could say
// "Not checked" and offer no way to change it. The only remedy was to upload the
// bytes again, which is not a remedy at all for a file the operator no longer
// has a local copy of -- and it is actively wrong for a file that was refused,
// because the same bytes earn the same refusal.
//
// A NEW ROUTE RATHER THAN A KIND ON THE EXISTING ONE. Job submission already
// exists at POST /library/recordings/{id}/jobs/{kind}, and buildJob is keyed on
// a db.Recording: it loads the row, and every kind it builds takes a filename
// and a duration off it. An upload is not a recording -- it has no row, no ID
// and no retention policy -- so squeezing it through that handler would mean
// either inventing a recording for it or teaching the handler to sometimes not
// have one. This is the upload-scoped route instead.
//
// A WRITE, and it is grouped with the media writes rather than the reads. It
// runs a subprocess against operator-supplied bytes and it REPLACES a record
// that other endpoints gate on -- playlistUploadProblems and
// pullSourceUploadProblems both refuse a save based on what this can change. A
// token built for unattended automation lists media and does not rewrite the
// server's conclusions about it.
func (s *Server) handleVerifyMedia(w http.ResponseWriter, r *http.Request) {
	if !s.requireJobs(w) {
		return
	}
	store, err := s.uploadStore()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	name := chi.URLParam(r, "name")

	// THE SAME TWO CHECKS handleDeleteMedia MAKES, IN THE SAME ORDER, and for
	// the reason written out at length there: uploads.Listable owns the rule
	// about which names the product admits to having, and Resolve owns the path
	// confinement. Asking only Resolve would leave `.probe-<name>.json` a legal
	// {name} here, and a job that probes a sidecar and then writes a verdict
	// BESIDE the sidecar is a file this product has no other way to create.
	if !uploads.Listable(name) {
		writeError(w, http.StatusBadRequest, "no such upload")
		return
	}
	path, err := store.Resolve(name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// 404 rather than queueing a job that will fail: the operator pressed a
	// button on a row, and "there is no such file" is an answer they can have
	// now. The worker checks again when it runs, because the file can go away
	// in between, and that check is the one that matters -- this one only saves
	// a job row.
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "no such upload")
			return
		}
		writeError(w, http.StatusInternalServerError, "this upload could not be read")
		return
	}

	job, err := uploadverify.NewJob(uploadverify.Params{Upload: name})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	out, created, err := s.jobq.Submit(job)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	stats := s.jobq.Stats()
	// No recording names, because an upload is not a recording: the target is
	// "upload:<name>" and jobView leaves RecordingID zero for it.
	view := s.view(*out, s.snapshot(), stats.Paused, stats.Running < s.concurrency(), nil, time.Now())
	status := http.StatusCreated
	if !created {
		// 200, not 201: Unique folded this into a re-check that was already
		// queued or running. Pressing the button twice asks once, and telling
		// the client it created something would have it counting two.
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"job": view, "created": created})
}
