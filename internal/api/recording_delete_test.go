package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/media"
	"github.com/rainmanjam/polyemesis/internal/recording"
)

// TestDeletingARecordingRemovesItAndSparesItsNeighbour is the SEAM test for the
// delete button: that DELETE /recordings/{id} reaches the recording manager
// with the id it was given, and reaches it at all.
//
// What deletion means on disk -- the segment, its derived media, and the clips
// that must survive it -- is owned by internal/recording's
// TestDeleteRemovesTheSegmentAndItsDerivedFilesButNotItsClips, where the
// manager can be driven without an HTTP fixture. This one is about the wiring
// and about scope: the neighbour is what catches a handler that reached wider
// than the id in its path.
func TestDeletingARecordingRemovesItAndSparesItsNeighbour(t *testing.T) {
	h, store, sign := sourceServer(t)
	srv := serverUnderTest(t, h)
	dir := srv.eng().Recordings().Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir recordings: %v", err)
	}

	seed := func(name string) db.Recording {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("MASTER"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
		if err := store.UpsertRecording(&db.Recording{
			Filename: name, StartedAt: time.Now(), Bytes: 6,
		}); err != nil {
			t.Fatalf("index %s: %v", name, err)
		}
		recs, err := store.ListRecordings()
		if err != nil {
			t.Fatalf("ListRecordings: %v", err)
		}
		for _, r := range recs {
			if r.Filename == name {
				return r
			}
		}
		t.Fatalf("recording %s was not indexed", name)
		return db.Recording{}
	}
	target := seed("rec-20240115-120000.mkv")
	neighbour := seed("rec-20240115-130000.mkv")

	var out map[string]string
	decodeInto(t, send(t, h, sign, http.MethodDelete,
		"/api/v1/recordings/"+strconv.FormatInt(target.ID, 10), nil, http.StatusOK), &out)
	if out["status"] != "deleted" {
		t.Errorf("DELETE answered %v, want status deleted", out)
	}

	if _, err := store.GetRecording(target.ID); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("recording %d is still indexed after DELETE answered 200: %v", target.ID, err)
	}
	if _, err := os.Stat(filepath.Join(dir, target.Filename)); !os.IsNotExist(err) {
		t.Errorf("the file for recording %d is still on disk after DELETE: the row went "+
			"and the bytes did not (%v)", target.ID, err)
	}

	if _, err := store.GetRecording(neighbour.ID); err != nil {
		t.Fatalf("the neighbouring recording %d was de-indexed too (%v)", neighbour.ID, err)
	}
	if _, err := os.Stat(filepath.Join(dir, neighbour.Filename)); err != nil {
		t.Errorf("the neighbouring recording's file was deleted too (%v)", err)
	}

	// Deleting it again is a 404, which is what says the first delete was real
	// rather than idempotent by doing nothing.
	mustJSONError(t, h, sign, http.MethodDelete,
		"/api/v1/recordings/"+strconv.FormatInt(target.ID, 10), nil, http.StatusNotFound)
}

// THE ARCHIVE OUTLIVES THE PIPELINE, and this is the test that says so.
//
// recordings.source_id is ON DELETE SET NULL by design: a recording is a
// record of a session, not a property of the programme that made it, so an
// operator clearing disk after their last source went away is the caller these
// three routes exist for. They used to be reached through
// s.eng().Recordings(), which meant an install with no engine -- one whose
// pipeline will not build, or one between sources -- got a panic on the
// library's most ordinary actions.
//
// Usage, Resolve and Delete are asserted together because they are the three
// verbs on the shared read-only manager: a repoint that missed one would leave
// the other two proving nothing about it.
func TestRecordingReadsAndDeletesSurviveWithNoEngineRunning(t *testing.T) {
	s, h, _, sign := managerServerWithoutEngines(t, defaultTools())
	if s.eng() != nil {
		t.Fatal("the fixture left an engine running, so nothing below is about its absence")
	}
	store := s.store

	dir := s.cfg.RecordingsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir recordings: %v", err)
	}
	const name = "rec-20240115-120000.mkv"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("MASTER"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := store.UpsertRecording(&db.Recording{
		Filename: name, StartedAt: time.Now(), Bytes: 6,
	}); err != nil {
		t.Fatalf("index: %v", err)
	}
	recs, err := store.ListRecordings()
	if err != nil || len(recs) != 1 {
		t.Fatalf("ListRecordings: %v (%d rows)", err, len(recs))
	}
	id := strconv.FormatInt(recs[0].ID, 10)

	// Usage: the free-space card on the library page, which loads on every visit.
	var usage map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/recordings/usage", nil, http.StatusOK), &usage)
	if usage["count"] == nil {
		t.Errorf("usage answered without a file count: %v", usage)
	}

	// Resolve: the download, whose path confinement is the reason a nil-safe
	// accessor answering "" would be worse than no answer at all.
	if body := send(t, h, sign, http.MethodGet,
		"/api/v1/recordings/"+id+"/download", nil, http.StatusOK); string(body) != "MASTER" {
		t.Errorf("download served %q, want the recording's bytes", body)
	}

	// Delete: the button an operator presses to get their disk back.
	send(t, h, sign, http.MethodDelete, "/api/v1/recordings/"+id, nil, http.StatusOK)
	if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
		t.Errorf("the file is still on disk after DELETE answered 200 (%v)", err)
	}
}

// The pure-path half of the same split: a stems download resolves its
// directory from CONFIG, so it needs no manager and no engine to confine
// against. The traversal refusals live in downloads_test.go; this is only
// about where the base comes from.
func TestTheStemsDirectoryIsFoundWithNoEngineRunning(t *testing.T) {
	s, h, _, sign := managerServerWithoutEngines(t, defaultTools())
	name := writeStem(t, recording.StemsDir(s.cfg.RecordingsDir()),
		"rec-20240115-143000-mic.flac", "STEMBYTES")

	body := send(t, h, sign, http.MethodGet,
		"/api/v1/recordings/stems/"+name+"/download", nil, http.StatusOK)
	if string(body) != "STEMBYTES" {
		t.Errorf("served %q, want the stem's contents", body)
	}
}

// The derived media route is the third shape in this split and the one MUST-NOT
// #6 of the design names by hand: media.Resolve CONFINES a client-supplied file
// name against the recording's directory, so the base it is handed has to be a
// real directory and not whatever a nil-safe accessor would have answered.
//
// It comes from config now, so the proxy an operator scrubs through in the clip
// editor is served on an install with no engine -- which is the install whose
// library is the only thing left to look at.
func TestDerivedMediaIsServedWithNoEngineRunning(t *testing.T) {
	s, h, _, sign := managerServerWithoutEngines(t, defaultTools())
	store := s.store

	const name = "rec-20240115-120000.mkv"
	layout := media.LayoutFor(s.cfg.RecordingsDir(), name)
	if err := os.MkdirAll(layout.Dir, 0o755); err != nil {
		t.Fatalf("mkdir derived: %v", err)
	}
	if err := os.WriteFile(layout.Proxy, []byte("PROXYBYTES"), 0o644); err != nil {
		t.Fatalf("write proxy: %v", err)
	}
	if err := store.UpsertRecording(&db.Recording{Filename: name, StartedAt: time.Now()}); err != nil {
		t.Fatalf("index: %v", err)
	}
	recs, err := store.ListRecordings()
	if err != nil || len(recs) != 1 {
		t.Fatalf("ListRecordings: %v (%d rows)", err, len(recs))
	}

	body := send(t, h, sign, http.MethodGet,
		"/api/v1/library/recordings/"+strconv.FormatInt(recs[0].ID, 10)+"/media/"+media.ProxyName,
		nil, http.StatusOK)
	if string(body) != "PROXYBYTES" {
		t.Errorf("served %q, want the proxy's contents", body)
	}
}
