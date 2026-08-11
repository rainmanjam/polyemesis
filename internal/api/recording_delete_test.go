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
