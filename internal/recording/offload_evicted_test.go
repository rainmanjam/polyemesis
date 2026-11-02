package recording

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// evict marks name as moved to a storage target and removes the local file,
// the way move-mode offload leaves it.
func evict(t *testing.T, store *db.DB, dir, name string) {
	t.Helper()
	id, err := store.CreateStorageTarget(nil, db.StorageTarget{Name: "t", Provider: "minio", Endpoint: "http://h", Bucket: "b",
		Mode: db.OffloadMove, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutOffloadItem(db.OffloadItem{TargetID: id, RelPath: name, Kind: db.OffloadKindRecording,
		Key: "k/" + name, State: db.OffloadVerified, Evicted: true}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
}

func TestAScanKeepsTheRowOfARecordingOffloadMovedAway(t *testing.T) {
	m, dir, store := newManager(t)
	now := time.Now()
	names := writeSegments(t, dir, now, []time.Duration{1 * time.Hour, 50 * time.Hour, 80 * time.Hour}, nil)
	if _, err := m.Scan(); err != nil {
		t.Fatal(err)
	}
	evict(t, store, dir, names[2])
	// And one that simply vanished, which must still be dropped.
	if err := os.Remove(filepath.Join(dir, names[1])); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Scan(); err != nil {
		t.Fatal(err)
	}
	got := indexedFilenames(t, store)
	want := []string{names[0], names[2]}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("indexed %v, want %v (evicted row kept, vanished row dropped)", got, want)
	}
}

func TestRetentionNeitherDeletesNorCountsAnEvictedRecording(t *testing.T) {
	m, dir, store := newManager(t)
	now := time.Now()
	names := writeSegments(t, dir, now, []time.Duration{1 * time.Hour, 80 * time.Hour}, nil)
	if _, err := m.Scan(); err != nil {
		t.Fatal(err)
	}
	evict(t, store, dir, names[1])
	if _, err := m.Sweep(db.RecordingSettings{MaxAgeHours: 24}); err != nil {
		t.Fatal(err)
	}
	got := indexedFilenames(t, store)
	if !slices.Contains(got, names[1]) {
		t.Fatalf("age retention dropped the evicted recording's row: %v", got)
	}
	items, _ := store.OffloadItemsFor(names[1])
	if len(items) != 1 {
		t.Fatalf("offload rows %v", items)
	}
}

func TestLiveSetNamesTheNewestAndRecentSegments(t *testing.T) {
	now := time.Now()
	recs := []db.Recording{
		{Filename: "old", StartedAt: now.Add(-3 * time.Hour)},
		{Filename: "recent", StartedAt: now.Add(-30 * time.Second)},
	}
	live := LiveSet(recs, 60, now)
	if !live["recent"] || live["old"] {
		t.Fatalf("live %v", live)
	}
	only := LiveSet(recs[:1], 60, now)
	if !only["old"] {
		t.Fatal("the newest segment is always live")
	}
}

func coverWith(t *testing.T, store *db.DB, name, mode string) {
	t.Helper()
	id, err := store.CreateStorageTarget(nil, db.StorageTarget{Name: "c-" + mode, Provider: "s3", Endpoint: "http://h", Bucket: "b",
		Mode: mode, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutOffloadItem(db.OffloadItem{TargetID: id, RelPath: name, Kind: db.OffloadKindRecording,
		Key: "k/" + mode + "/" + name, State: db.OffloadVerified, SHA256: "digest", VerifiedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

// Review #1: a recording fetched back from offload storage is pinned against
// retention, so it is not deleted mid-clip and its offload rows are not then
// pruned into an orphaned remote copy.
func TestRetentionLeavesAFetchedBackRecordingAloneForThePin(t *testing.T) {
	m, dir, store := newManager(t)
	now := time.Now()
	names := writeSegments(t, dir, now, []time.Duration{1 * time.Hour, 80 * time.Hour}, nil)
	if _, err := m.Scan(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, names[1]))
	evict(t, store, dir, names[1])
	_ = os.WriteFile(filepath.Join(dir, names[1]), data, 0o644) // fetched back
	its, _ := store.OffloadItemsFor(names[1])
	for _, it := range its {
		it.Evicted, it.FetchedAt = false, time.Now()
		_ = store.PutOffloadItem(it)
	}
	if _, err := m.Sweep(db.RecordingSettings{MaxAgeHours: 24}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(indexedFilenames(t, store), names[1]) {
		t.Fatal("retention deleted a recording inside its fetch pin")
	}
	if _, err := os.Stat(filepath.Join(dir, names[1])); err != nil {
		t.Fatalf("fetched-back file gone: %v", err)
	}
}

// Review #1, second half: when retention reaches a recording every enabled
// target holds a verified copy of (one of them in move mode), it is evicted --
// file gone, row and offload state kept -- not deleted.
func TestRetentionEvictsACoveredRecordingInsteadOfDeletingIt(t *testing.T) {
	m, dir, store := newManager(t)
	now := time.Now()
	names := writeSegments(t, dir, now, []time.Duration{1 * time.Hour, 80 * time.Hour}, nil)
	if _, err := m.Scan(); err != nil {
		t.Fatal(err)
	}
	coverWith(t, store, names[1], db.OffloadMove)
	if _, err := m.Sweep(db.RecordingSettings{MaxAgeHours: 24}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, names[1])); !os.IsNotExist(err) {
		t.Fatal("retention left the file")
	}
	if !slices.Contains(indexedFilenames(t, store), names[1]) {
		t.Fatal("retention deleted the row of a recording whose only copy is now in the bucket")
	}
	if ev, _ := store.EvictedRecordings(); !ev[names[1]] {
		t.Fatal("not marked evicted")
	}
}

func TestRetentionStillDeletesWhatNoMoveTargetCovers(t *testing.T) {
	m, dir, store := newManager(t)
	now := time.Now()
	names := writeSegments(t, dir, now, []time.Duration{1 * time.Hour, 80 * time.Hour}, nil)
	if _, err := m.Scan(); err != nil {
		t.Fatal(err)
	}
	coverWith(t, store, names[1], db.OffloadMirror) // mirror keeps local retention unchanged
	if _, err := m.Sweep(db.RecordingSettings{MaxAgeHours: 24}); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(indexedFilenames(t, store), names[1]) {
		t.Fatal("mirror mode changed retention")
	}
}

// Review #11: an evicted master no longer holds the stem cutoff back, so the
// stems of masters retention deleted are swept -- while the evicted master's
// own stems are kept.
func TestAnEvictedMasterNeitherPinsOrphanStemsNorLosesItsOwn(t *testing.T) {
	m, dir, store := newManager(t)
	now := time.Now()
	names := writeSegments(t, dir, now, []time.Duration{1 * time.Hour, 50 * time.Hour, 80 * time.Hour}, nil)
	if _, err := m.Scan(); err != nil {
		t.Fatal(err)
	}
	evict(t, store, dir, names[2])
	_ = os.MkdirAll(m.StemsDir(), 0o755)
	stemOf := func(name string) string { return strings.TrimSuffix(name, ".mkv") + "-mic.flac" }
	for _, n := range names[1:] {
		if err := os.WriteFile(filepath.Join(m.StemsDir(), stemOf(n)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Sweep(db.RecordingSettings{MaxAgeHours: 24}); err != nil { // deletes the 50h master
		t.Fatal(err)
	}
	if _, err := m.SweepStems(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.StemsDir(), stemOf(names[1]))); !os.IsNotExist(err) {
		t.Fatal("the stem of a deleted master survived because an evicted row held the cutoff")
	}
	if _, err := os.Stat(filepath.Join(m.StemsDir(), stemOf(names[2]))); err != nil {
		t.Fatal("the evicted master's own stem was swept")
	}
}
