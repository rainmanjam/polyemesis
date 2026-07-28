package playout

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// writeSegment creates a file of size bytes with a given modification time, so
// a test can build an aged directory without waiting.
func writeSegment(t *testing.T, root, rel string, size int, age time.Duration) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	mod := epoch.Add(-age)
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
	return path
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestSweepDeletesOldestSegmentsFirst(t *testing.T) {
	root := t.TempDir()
	// Six 100-byte segments, oldest to newest. keepNewestPerVariant protects
	// the last four, so only the first two are ever candidates.
	var paths []string
	for i := 0; i < 6; i++ {
		age := time.Duration(6-i) * time.Minute
		paths = append(paths, writeSegment(t, root, "hd/seg_0000"+string(rune('0'+i))+".ts", 100, age))
	}

	u, err := sweep(root, 450, os.Remove)
	if err != nil {
		t.Fatal(err)
	}
	if u.Deleted != 2 {
		t.Fatalf("deleted = %d, want 2", u.Deleted)
	}
	if exists(paths[0]) || exists(paths[1]) {
		t.Fatal("the two oldest segments survived the cap")
	}
	for _, p := range paths[2:] {
		if !exists(p) {
			t.Fatalf("%s was deleted; only the oldest should go", filepath.Base(p))
		}
	}
	if u.Bytes != 400 {
		t.Fatalf("bytes = %d, want 400", u.Bytes)
	}
	if u.OverLimit {
		t.Fatal("reported over limit after a successful sweep")
	}
}

func TestSweepNeverDeletesTheSegmentsViewersAreMidPlaybackOn(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 3; i++ {
		writeSegment(t, root, "hd/seg_0000"+string(rune('0'+i))+".ts", 1000, time.Duration(3-i)*time.Minute)
	}

	// A cap far below what the live window alone occupies. The right answer is
	// to say so, not to delete the stream out from under every viewer.
	u, err := sweep(root, 100, os.Remove)
	if err != nil {
		t.Fatal(err)
	}
	if u.Deleted != 0 {
		t.Fatalf("deleted = %d, want 0: the newest segments are protected", u.Deleted)
	}
	if !u.OverLimit {
		t.Fatal("overLimit = false; a cap that cannot be met must be reported")
	}
}

func TestSweepProtectsPlaylistsManifestsAndInitSegments(t *testing.T) {
	root := t.TempDir()
	keep := []string{
		writeSegment(t, root, "hd/"+MediaPlaylist, 500, time.Hour),
		writeSegment(t, root, "hd/"+DASHManifest, 500, time.Hour),
		writeSegment(t, root, "hd/init-0.m4s", 500, time.Hour),
		writeSegment(t, root, MasterPlaylist, 500, time.Hour),
	}
	// Enough disposable bytes that the sweeper has a real choice to make.
	for i := 0; i < 8; i++ {
		writeSegment(t, root, "hd/chunk-0-0000"+string(rune('0'+i))+".m4s", 1000, time.Duration(8-i)*time.Minute)
	}

	if _, err := sweep(root, 1, os.Remove); err != nil {
		t.Fatal(err)
	}
	for _, p := range keep {
		if !exists(p) {
			t.Fatalf("%s was deleted; removing it makes every segment beside it unreachable", filepath.Base(p))
		}
	}
}

func TestSweepKeepsEachVariantsNewestSegments(t *testing.T) {
	root := t.TempDir()
	for _, v := range []string{"hd", "sd"} {
		for i := 0; i < 6; i++ {
			writeSegment(t, root, v+"/seg_0000"+string(rune('0'+i))+".ts", 100, time.Duration(6-i)*time.Minute)
		}
	}

	if _, err := sweep(root, 1, os.Remove); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"hd", "sd"} {
		entries, err := os.ReadDir(filepath.Join(root, v))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != keepNewestPerVariant {
			t.Fatalf("%s kept %d segments, want %d: protection is per variant, not global",
				v, len(entries), keepNewestPerVariant)
		}
	}
}

func TestSweepCollectsOrphansLeftByAPreviousRun(t *testing.T) {
	// The case the cap exists for: nothing in memory remembers these, so the
	// filesystem has to be the source of truth.
	root := t.TempDir()
	var old []string
	for i := 0; i < 5; i++ {
		old = append(old, writeSegment(t, root, "hd/seg_1000"+string(rune('0'+i))+".ts", 1000, 48*time.Hour))
	}
	for i := 0; i < 4; i++ {
		writeSegment(t, root, "hd/seg_0000"+string(rune('0'+i))+".ts", 1000, time.Duration(4-i)*time.Second)
	}

	u, err := sweep(root, 4000, os.Remove)
	if err != nil {
		t.Fatal(err)
	}
	if u.Deleted != 5 {
		t.Fatalf("deleted = %d, want the 5 orphans", u.Deleted)
	}
	for _, p := range old {
		if exists(p) {
			t.Fatalf("orphan %s survived", filepath.Base(p))
		}
	}
}

func TestSweepMeasuresWithoutDeletingWhenThereIsNoCap(t *testing.T) {
	root := t.TempDir()
	writeSegment(t, root, "hd/seg_00000.ts", 700, time.Minute)

	u, err := sweep(root, 0, func(string) error {
		t.Fatal("sweep deleted something with no cap set")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if u.Bytes != 700 || u.Files != 1 {
		t.Fatalf("usage = %+v, want 700 bytes in 1 file", u)
	}
}

func TestSweepOfAMissingDirectoryIsNotAnError(t *testing.T) {
	// The first sweep runs before the first reconcile has created anything.
	_, err := sweep(filepath.Join(t.TempDir(), "never-created"), 100, os.Remove)
	if err != nil {
		t.Fatalf("sweep of a missing directory returned %v", err)
	}
}

func TestScanSegmentsAttributesFilesToTheirVariant(t *testing.T) {
	root := t.TempDir()
	writeSegment(t, root, "hd/seg_00000.ts", 10, time.Minute)
	writeSegment(t, root, "sd/seg_00000.ts", 10, time.Minute)
	writeSegment(t, root, "stray.ts", 10, time.Minute)

	files, err := scanSegments(root)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.variant)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != ",hd,sd" {
		t.Fatalf("variants = %v, want a file at the root to belong to no variant", got)
	}
}

func TestClearVariantDirRemovesTheStalePlaylistButKeepsTheDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "hd")
	writeSegment(t, root, "hd/"+MediaPlaylist, 10, time.Minute)
	writeSegment(t, root, "hd/seg_00000.ts", 10, time.Minute)

	if err := clearVariantDir(dir); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("the directory itself should survive: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("%d files left, want none", len(entries))
	}
}

func TestClearVariantDirOfAMissingDirectoryIsNotAnError(t *testing.T) {
	if err := clearVariantDir(filepath.Join(t.TempDir(), "absent")); err != nil {
		t.Fatalf("clearVariantDir returned %v", err)
	}
}
