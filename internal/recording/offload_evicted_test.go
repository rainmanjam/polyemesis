package recording

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// evict marks name as moved to a storage target and removes the local file,
// the way move-mode offload leaves it.
func evict(t *testing.T, store *db.DB, dir, name string) {
	t.Helper()
	id, err := store.CreateStorageTarget(db.StorageTarget{Name: "t", Provider: "minio", Endpoint: "http://h", Bucket: "b",
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
