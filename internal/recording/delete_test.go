package recording

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/media"
)

// TestDeleteRemovesTheSegmentAndItsDerivedFilesButNotItsClips is the only test
// that watches Manager.Delete touch the DISK.
//
// Everything else about deletion is pinned at the row level, and a row-level
// assertion cannot tell a delete that removed the file from one that only
// de-indexed it. That second behaviour is a silent disk leak: the library stops
// showing the recording, the operator believes the space is back, and the
// hours-long master and its proxy are still there.
//
// The negative halves matter as much as the positive ones. A clip is the
// artifact the operator chose to keep out of a recording they were willing to
// throw away -- the same claim TestClipsAreNotSweptWithTheirRecording makes
// about retention, made here about the delete button -- and the neighbour is
// what catches a delete that reached wider than the id it was given.
func TestDeleteRemovesTheSegmentAndItsDerivedFilesButNotItsClips(t *testing.T) {
	m, dir, store := newManager(t)
	now := time.Now()

	seed := func(name string) db.Recording {
		t.Helper()
		writeFile(t, dir, name, 16)
		if err := store.UpsertRecording(&db.Recording{Filename: name, StartedAt: now}); err != nil {
			t.Fatalf("upsert %s: %v", name, err)
		}
		recs, err := store.ListRecordings()
		if err != nil {
			t.Fatalf("list recordings: %v", err)
		}
		for _, r := range recs {
			if r.Filename == name {
				return r
			}
		}
		t.Fatalf("recording %s was not indexed", name)
		return db.Recording{}
	}
	target := seed("rec-20240115-100000.mkv")
	neighbour := seed("rec-20240115-110000.mkv")

	targetPath := filepath.Join(dir, target.Filename)
	neighbourPath := filepath.Join(dir, neighbour.Filename)

	// Derived media for BOTH, so "the target's derived directory is gone"
	// cannot pass by having removed the whole derived tree.
	layout := media.LayoutFor(dir, target.Filename)
	neighbourLayout := media.LayoutFor(dir, neighbour.Filename)
	for _, l := range []media.Layout{layout, neighbourLayout} {
		if err := os.MkdirAll(l.Dir, 0o755); err != nil {
			t.Fatalf("mkdir derived: %v", err)
		}
		for _, p := range []string{l.Proxy, l.Poster} {
			if err := os.WriteFile(p, []byte("derived"), 0o644); err != nil {
				t.Fatalf("seed derived %s: %v", p, err)
			}
		}
	}

	// The clip lives in its own directory beside the masters and must outlive
	// the recording it was cut from.
	clipDir := filepath.Join(dir, "clips")
	if err := os.MkdirAll(clipDir, 0o755); err != nil {
		t.Fatalf("mkdir clips: %v", err)
	}
	clip := filepath.Join(clipDir, "rec-20240115-100000-highlight.mp4")
	if err := os.WriteFile(clip, []byte("the bit worth keeping"), 0o644); err != nil {
		t.Fatalf("seed clip: %v", err)
	}

	// A session holding only the target, so pruning it is observable. The
	// neighbour gets its own, which must survive.
	//
	// Both are AUTOMATIC sessions. Measured, not assumed: PruneEmptySessions
	// drops `auto = 1` only, because an empty session a human made is a
	// placeholder they are about to fill rather than litter. A manual session
	// here would make the prune assertion below unfalsifiable.
	lonely, err := store.CreateSession(db.Metadata{Title: "just the target"}, true)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := store.AddRecordingToSession(lonely.ID, target.ID); err != nil {
		t.Fatalf("AddRecordingToSession: %v", err)
	}
	survivor, err := store.CreateSession(db.Metadata{Title: "the neighbour's"}, true)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := store.AddRecordingToSession(survivor.ID, neighbour.ID); err != nil {
		t.Fatalf("AddRecordingToSession: %v", err)
	}

	if err := m.Delete(target.ID); err != nil {
		t.Fatalf("Delete(%d): %v", target.ID, err)
	}

	// What must be gone.
	if _, err := os.Stat(targetPath); !os.IsNotExist(err) {
		t.Errorf("the segment file %s is still on disk after Delete: the row went "+
			"and the bytes did not (%v)", target.Filename, err)
	}
	if _, err := os.Stat(layout.Dir); !os.IsNotExist(err) {
		t.Errorf("the derived media directory %s survived Delete: the proxy and poster "+
			"of a deleted recording still occupy disk (%v)", layout.Dir, err)
	}
	if _, err := store.GetRecording(target.ID); err == nil {
		t.Errorf("recording %d is still indexed after Delete", target.ID)
	}
	if _, err := store.GetSession(lonely.ID); err == nil {
		t.Errorf("session %d held only the deleted recording and was not pruned", lonely.ID)
	}

	// What must remain.
	if _, err := os.Stat(clip); err != nil {
		t.Errorf("the clip %s was deleted with its source recording; a clip is the "+
			"artifact, not a byproduct (%v)", clip, err)
	}
	if _, err := os.Stat(neighbourPath); err != nil {
		t.Errorf("the neighbouring recording's file %s was deleted too (%v)",
			neighbour.Filename, err)
	}
	if _, err := os.Stat(neighbourLayout.Proxy); err != nil {
		t.Errorf("the neighbouring recording's derived media was removed too (%v)", err)
	}
	if _, err := store.GetRecording(neighbour.ID); err != nil {
		t.Errorf("the neighbouring recording %d was de-indexed too (%v)", neighbour.ID, err)
	}
	if _, err := store.GetSession(survivor.ID); err != nil {
		t.Errorf("session %d still holds a recording and was pruned (%v)", survivor.ID, err)
	}
}
