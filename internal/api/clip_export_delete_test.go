package api

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/jobs"
)

// seedExportOnDisk writes a file into the server's exports directory and
// returns its path.
func seedExportOnDisk(t *testing.T, srv *Server, name string) string {
	t.Helper()
	dir := srv.clipExportDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir exports: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("CLIPBYTES"), 0o644); err != nil {
		t.Fatalf("seed export %q: %v", name, err)
	}
	return path
}

// TestDeletingAClipExportJobDeletesTheExportedFile is #222.
//
// The job row is the ONLY reference to the file: the download route is keyed on
// the job, and the exports directory is deliberately outside the rolling
// buffer's pruning, so before this an operator who tidied their jobs list
// silently stranded every clip they had ever exported -- unservable, because
// the only route needs the job, and unswept, because nothing sweeps exports.
func TestDeletingAClipExportJobDeletesTheExportedFile(t *testing.T) {
	h, sign, store := jobsFixture(t)
	srv := serverUnderTest(t, h)

	path := seedExportOnDisk(t, srv, "highlight.mp4")
	job := seedDoneClipExport(t, store, path)

	send(t, h, sign, http.MethodDelete, jobPath(job.ID), nil, http.StatusOK)

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the exported clip outlived the only row that referenced it: %v", err)
	}
}

// TestDeletingAJobLeavesFilesOutsideTheExportsDirAlone is the confinement half.
// The delete path resolves a database-supplied string into an unlink, which is
// the most destructive thing in this file.
func TestDeletingAJobLeavesFilesOutsideTheExportsDirAlone(t *testing.T) {
	h, sign, store := jobsFixture(t)
	srv := serverUnderTest(t, h)

	canary := plantCanary(t, srv.eng().Recordings().Dir(), "session.mkv")
	job := seedDoneClipExport(t, store, canary)

	// The row still goes: the file is what must not.
	send(t, h, sign, http.MethodDelete, jobPath(job.ID), nil, http.StatusOK)

	if _, err := os.Stat(canary); err != nil {
		t.Fatalf("a file outside the exports directory was deleted with a job row: %v", err)
	}
}

// TestDeletingANonExportJobDeletesNoFiles guards the blast radius: every job
// kind goes through the same delete handler, and only clip exports own a file.
//
// The unrelated job is given a result that LOOKS like an export and names a
// real file inside the exports directory, which is the only version of this
// test that can fail. A proxy job with an empty result passes with the kind
// check deleted -- it names no path, so there is nothing to remove either way,
// and the assertion would be testing the absence of a filename.
func TestDeletingANonExportJobDeletesNoFiles(t *testing.T) {
	h, sign, store := jobsFixture(t)
	srv := serverUnderTest(t, h)

	kept := seedExportOnDisk(t, srv, "someone-elses.mp4")
	other := enqueued(t, store, "media.proxy", jobs.RecordingTarget(9))
	if err := store.FinishJob(other.ID, jobs.StateDone, "", time.Now()); err != nil {
		t.Fatalf("FinishJob: %v", err)
	}
	setClipResultPath(t, store, other.ID, kept)

	send(t, h, sign, http.MethodDelete, jobPath(other.ID), nil, http.StatusOK)

	if _, err := os.Stat(kept); err != nil {
		t.Fatalf("deleting a job of another kind removed an export: %v", err)
	}
}

// TestPurgingJobHistoryDeletesTheExportedFiles is the half that made a
// delete-only fix read as a fix without being one: POST /jobs/purge strands
// exports in bulk, and it is also what the scheduled retention sweep calls.
func TestPurgingJobHistoryDeletesTheExportedFiles(t *testing.T) {
	h, sign, store := jobsFixture(t)
	srv := serverUnderTest(t, h)

	old := time.Now().Add(-90 * 24 * time.Hour)
	var paths []string
	for _, name := range []string{"one.mp4", "two.mp4"} {
		path := seedExportOnDisk(t, srv, name)
		job := seedDoneClipExport(t, store, path)
		if err := store.FinishJob(job.ID, jobs.StateDone, "", old); err != nil {
			t.Fatalf("age the job: %v", err)
		}
		paths = append(paths, path)
	}

	// keep:0 with a cutoff after both, so both are in scope.
	send(t, h, sign, http.MethodPost, "/api/v1/jobs/purge",
		map[string]any{"days": 1, "keep": 0}, http.StatusOK)

	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("purging the job history stranded %s: %v", filepath.Base(path), err)
		}
	}
}
