package recording

import (
	"errors"
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

	// Hours apart, and hours old, because Delete now refuses anything inside
	// the live window. Both segments used to be stamped with the same
	// time.Now(), which described no install that has ever existed -- two
	// segments cannot start in the same second on the same recorder -- and
	// which would now read as two files a recorder is holding open.
	seed := func(name string, startedAt time.Time) db.Recording {
		t.Helper()
		writeFile(t, dir, name, 16)
		if err := store.UpsertRecording(&db.Recording{Filename: name, StartedAt: startedAt}); err != nil {
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
	target := seed("rec-20240115-100000.mkv", now.Add(-6*time.Hour))
	neighbour := seed("rec-20240115-110000.mkv", now.Add(-3*time.Hour))

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

// TestDeleteRefusesASegmentARecorderIsStillWriting pins the guard the UI's
// delete button never had.
//
// Sweep has refused to unlink the open segment since #504; Delete, which is
// what the button reaches, went straight to os.Remove and answered
// {"status":"deleted"}. The recorder then appended to an inode with no name:
// footage that cannot be re-shot, gone, with the disk still charged for it and
// Usage() no longer counting it because Usage() reads the index.
//
// The control cases are the point of the table. A guard that refused every
// delete would satisfy the first two rows and fail the last two, and the last
// two are the whole reason the button exists.
func TestDeleteRefusesASegmentARecorderIsStillWriting(t *testing.T) {
	// One recorder per programme, all writing into the one install-wide
	// recordings directory, so several files are open at once and their start
	// times sit within a segment length of each other.
	tests := []struct {
		name       string
		age        time.Duration
		wantRefuse bool
	}{
		{
			name:       "the newest segment is the one the recorder holds open",
			age:        0,
			wantRefuse: true,
		},
		{
			name:       "a second recorder's open segment started seconds earlier and is just as live",
			age:        45 * time.Second,
			wantRefuse: true,
		},
		{
			// The install's configured segment length is what decides this,
			// not the slack: segments default to an hour, so a file half an
			// hour old is one a recorder is halfway through writing. Delete is
			// an HTTP handler and is handed no settings, so this is also what
			// pins it reading them from the store.
			name:       "a half-hour-old segment is mid-write while segments run an hour",
			age:        30 * time.Minute,
			wantRefuse: true,
		},
		{
			name:       "a segment from the previous hour has been closed and is the operator's to delete",
			age:        3 * time.Hour,
			wantRefuse: false,
		},
		{
			name:       "an old segment nobody has touched all day is deletable",
			age:        20 * time.Hour,
			wantRefuse: false,
		},
	}

	// Every age above is measured back from this one instant, so the segments
	// form a single index in which exactly the recent ones are live.
	ages := make([]time.Duration, len(tests))
	for i, tc := range tests {
		ages[i] = tc.age
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, dir, store := newManager(t)
			now := time.Now()
			names := writeSegments(t, dir, now, ages, nil)
			if _, err := m.Scan(); err != nil {
				t.Fatalf("scan: %v", err)
			}

			recs, err := store.ListRecordings()
			if err != nil {
				t.Fatalf("list recordings: %v", err)
			}
			var target db.Recording
			for _, r := range recs {
				if r.Filename == names[i] {
					target = r
				}
			}
			if target.ID == 0 {
				t.Fatalf("segment %s was not indexed", names[i])
			}

			err = m.Delete(target.ID)
			path := filepath.Join(dir, target.Filename)

			if !tc.wantRefuse {
				if err != nil {
					t.Fatalf("Delete(%s) refused a segment no recorder is writing: %v", target.Filename, err)
				}
				if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
					t.Errorf("Delete(%s) reported success and left the file on disk (%v)", target.Filename, statErr)
				}
				return
			}

			if err == nil {
				t.Fatalf("Delete(%s) unlinked a segment the recorder is still writing into and "+
					"reported success; the recorder is now appending to a deleted inode", target.Filename)
			}
			if !errors.Is(err, ErrSegmentLive) {
				t.Errorf("Delete(%s) failed with %v, which does not identify itself as the live-segment "+
					"refusal, so a caller cannot tell it apart from a broken store", target.Filename, err)
			}
			// db.ErrStateConflict is what writeStoreError turns into a 409;
			// without it the API answers 500 and the operator reads a live
			// segment as a server fault.
			if !errors.Is(err, db.ErrStateConflict) {
				t.Errorf("Delete(%s) failed with %v, which the HTTP surface maps to 500 rather than "+
					"409 conflict", target.Filename, err)
			}
			if _, statErr := os.Stat(path); statErr != nil {
				t.Errorf("Delete(%s) refused and removed the file anyway (%v)", target.Filename, statErr)
			}
			if _, getErr := store.GetRecording(target.ID); getErr != nil {
				t.Errorf("Delete(%s) refused and de-indexed the row anyway (%v)", target.Filename, getErr)
			}
		})
	}
}

// TestDeleteAllowsTheLoneSegmentOfAFinishedSession is the control on the live
// window's ANCHOR.
//
// Anchoring it on the newest row in the index reads as the safer choice and is
// a trap: the newest row is always inside a window measured from itself, so on
// an install that stopped recording last week the last segment of every session
// becomes undeletable for ever and the operator cannot reclaim the space by any
// route the UI offers. Anchored on the clock, a segment that started yesterday
// is plainly not open and the button works.
func TestDeleteAllowsTheLoneSegmentOfAFinishedSession(t *testing.T) {
	m, dir, store := newManager(t)

	writeSegments(t, dir, time.Now(), []time.Duration{30 * time.Hour}, nil)
	if _, err := m.Scan(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	recs, err := store.ListRecordings()
	if err != nil {
		t.Fatalf("list recordings: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("indexed %d segments, want 1", len(recs))
	}

	if err := m.Delete(recs[0].ID); err != nil {
		t.Fatalf("Delete(%s) refused the only segment in the index, recorded yesterday: %v; "+
			"nothing is writing it and no other path can free that space", recs[0].Filename, err)
	}
	if got := filesOnDisk(t, dir); len(got) != 0 {
		t.Errorf("files on disk %v, want none", got)
	}
}
