package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/clipper"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/jobs"
)

// TestClipExportDownloadServesOnlyFilesInsideTheExportsDir is the confinement
// test for the one route in this product that serves a path read out of a
// database row.
//
// The path was written by this server, so the guard is a belt on top of braces
// -- but a job's Result survives a database somebody edited, and the thing this
// route must never do is hand over an arbitrary file. clipPathIn is already
// unit-tested; what is NOT tested anywhere else is that handleDownloadClipExport
// actually calls it, which is a different claim and the one a refactor breaks.
//
// The positive case runs FIRST, on purpose. A confinement test that only
// refuses things passes trivially with the whole route deleted, and that is the
// usual way this shape of test goes green while the feature is broken -- the
// same reasoning as TestStemDownloadServesAStemInsideTheStemsDirectory.
//
// The refusal is judged on the BYTES and not on the status. An unmatched /api
// path falls through to the SPA handler, which answers 200 with index.html on a
// machine that has run `make ui`; reading that as "the file was served" produced
// a confinement failure report that did not exist.
func TestClipExportDownloadServesOnlyFilesInsideTheExportsDir(t *testing.T) {
	h, sign, store := jobsFixture(t)
	srv := serverUnderTest(t, h)

	dir := srv.clipExportDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir exports: %v", err)
	}
	const body = "CLIPBYTES"
	inside := filepath.Join(dir, "highlight.mp4")
	if err := os.WriteFile(inside, []byte(body), 0o644); err != nil {
		t.Fatalf("seed clip: %v", err)
	}

	job := seedDoneClipExport(t, store, inside)
	path := "/api/v1/clipper/jobs/" + strconv.FormatInt(job.ID, 10) + "/download"

	got := send(t, h, sign, http.MethodGet, path, nil, http.StatusOK)
	if string(got) != body {
		t.Fatalf("served %q, want the exported clip's contents", got)
	}

	// Now point the SAME job at a file outside the exports directory, exactly
	// as a tampered or migrated row would.
	canary := plantCanary(t, srv.eng().Recordings().Dir(), "secret.mp4")
	setClipResultPath(t, store, job.ID, canary)
	mustNotLeak(t, h, sign, http.MethodGet, path)

	// And one aimed at the canary from inside the exports directory via
	// traversal, which is the form a hand-edited row is most likely to take.
	//
	// ONE `..`, not two. dir is <recordings>/exports and the canary is at
	// <recordings>/secret.mp4, so two overshot to <DataDir>/secret.mp4 -- a file
	// that does not exist, which 404s whether confinement runs or not. A
	// reviewer measured it: with the clipExportPath guard removed AND the
	// preceding absolute-path case deleted, this still reported ok. The
	// assertion named traversal and tested the absence of a file.
	setClipResultPath(t, store, job.ID,
		filepath.Join(dir, "..", filepath.Base(canary)))
	mustNotLeak(t, h, sign, http.MethodGet, path)
}

// seedDoneClipExport stores a finished clip.export job whose Result names path.
func seedDoneClipExport(t *testing.T, store *db.DB, path string) *jobs.Job {
	t.Helper()
	j, err := store.EnqueueJob(jobs.Job{
		Kind:   clipper.JobKind,
		Target: jobs.RecordingTarget(1),
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	if err := store.FinishJob(j.ID, jobs.StateDone, "", time.Now()); err != nil {
		t.Fatalf("FinishJob: %v", err)
	}
	setClipResultPath(t, store, j.ID, path)
	return j
}

func setClipResultPath(t *testing.T, store *db.DB, id int64, path string) {
	t.Helper()
	raw, err := json.Marshal(clipper.JobResult{Path: path, Mode: clipper.ModeFast})
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if err := store.SetJobResult(id, raw, time.Now()); err != nil {
		t.Fatalf("SetJobResult: %v", err)
	}
}
