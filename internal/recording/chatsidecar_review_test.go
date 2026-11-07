package recording

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

func sidecarPath(dir, filename string) string {
	return filepath.Join(dir, strings.TrimSuffix(filename, filepath.Ext(filename))+".chat.json")
}

// The archive job bumps a master's mtime hours later; by then the purge has
// taken part of the window. The rewrite must not replace a complete file with
// the tail.
func TestARewriteNeverDowngradesACompleteSidecar(t *testing.T) {
	m, dir, store := newManager(t)
	start := time.Now().Add(-5 * time.Hour).Truncate(time.Second)
	rec := seedFinished(t, m, dir, start, 600_000)
	say(t, store, "before", start.Add(-time.Hour), "old", "before the recording") // proves completeness
	say(t, store, "1", start.Add(time.Minute), "ana", "early line")
	say(t, store, "2", start.Add(9*time.Minute), "bo", "late line")
	if !m.writeChatSidecars() {
		t.Fatal("first save")
	}
	sc := sidecarPath(dir, rec.Filename)
	if raw, _ := os.ReadFile(sc); !strings.Contains(string(raw), `"complete":true`) || !strings.Contains(string(raw), "early line") {
		t.Fatalf("first save should be complete: %s", raw)
	}

	// The purge takes the early part; the keep floor leaves a row inside the window.
	if _, err := store.PurgeChatMessages(start.Add(5*time.Minute), 1); err != nil {
		t.Fatal(err)
	}
	// The master is touched again (archive replaceOriginal), newer than the sidecar.
	written := start.Add(time.Hour)
	if err := os.Chtimes(sc, written, written); err != nil {
		t.Fatal(err)
	}
	later := start.Add(2 * time.Hour)
	if err := os.Chtimes(filepath.Join(dir, rec.Filename), later, later); err != nil {
		t.Fatal(err)
	}
	if m.writeChatSidecars() {
		t.Error("a downgrade was written")
	}
	if raw, _ := os.ReadFile(sc); !strings.Contains(string(raw), "early line") || !strings.Contains(string(raw), `"complete":true`) {
		t.Errorf("the complete sidecar was replaced: %s", raw)
	}
}

func TestTheSweepKeepsChatOfAnUnindexedMasterOnDisk(t *testing.T) {
	m, dir, _ := newManager(t)
	indexed := seedFinished(t, m, dir, time.Now().Add(-6*time.Hour).Truncate(time.Second), 600_000)
	// A master the scan has not indexed yet, with its sidecar.
	writeFile(t, dir, "rec-20250101-000000.mkv", 4)
	keep := filepath.Join(dir, "rec-20250101-000000.chat.json")
	// A temp-looking file that is not a sidecar's temp must be left alone.
	notTemp := filepath.Join(dir, ".notes.chat.something.tmp")
	for _, p := range []string{keep, notTemp} {
		if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-3 * time.Hour)
	_ = os.Chtimes(notTemp, old, old)

	m.sweepChatSidecars(map[string]bool{indexed.Filename: true})
	for _, p := range []string{keep, notTemp} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed", filepath.Base(p))
		}
	}
}

// A message at exactly the next file's start belongs to that file only.
func TestAMessageOnTheSeamLandsInOneSidecar(t *testing.T) {
	m, dir, store := newManager(t)
	start := time.Now().Add(-6 * time.Hour).Truncate(time.Second)
	a := seedFinished(t, m, dir, start, 3_600_400) // overshoots the next start by 400ms
	b := seedFinished(t, m, dir, start.Add(time.Hour), 600_000)
	say(t, store, "in-a", start.Add(time.Hour-time.Second), "ana", "last of a")
	say(t, store, "seam", b.StartedAt, "bo", "exactly on the seam")
	say(t, store, "in-b", b.StartedAt.Add(time.Second), "cy", "first of b")
	m.writeChatSidecars()

	ra, _ := os.ReadFile(sidecarPath(dir, a.Filename))
	rb, _ := os.ReadFile(sidecarPath(dir, b.Filename))
	if !strings.Contains(string(ra), "last of a") || strings.Contains(string(ra), "exactly on the seam") {
		t.Errorf("a: %s", ra)
	}
	if !strings.Contains(string(rb), "exactly on the seam") || !strings.Contains(string(rb), "first of b") ||
		strings.Contains(string(rb), "last of a") {
		t.Errorf("b: %s", rb)
	}
}

func TestSharedStemKeepsTheSidecarUntilTheLastMasterGoes(t *testing.T) {
	m, dir, store := newManager(t)
	start := time.Now().Add(-6 * time.Hour).Truncate(time.Second)
	mkv := seedFinished(t, m, dir, start, 600_000)
	ts := strings.TrimSuffix(mkv.Filename, ".mkv") + ".ts"
	writeFile(t, dir, ts, 16)
	old := start.Add(time.Hour)
	_ = os.Chtimes(filepath.Join(dir, ts), old, old)
	if err := store.UpsertRecording(&db.Recording{Filename: ts, StartedAt: start, DurationMS: 600_000}); err != nil {
		t.Fatal(err)
	}
	say(t, store, "1", start.Add(time.Minute), "ana", "x")
	m.writeChatSidecars()
	if !m.HasChatSidecar(mkv.Filename) {
		t.Fatal("no sidecar")
	}

	if err := m.Delete(mkv.ID); err != nil {
		t.Fatal(err)
	}
	if !m.HasChatSidecar(ts) {
		t.Fatal("deleting x.mkv removed the sidecar x.ts still needs")
	}
	recs, _ := store.ListRecordings()
	if err := m.Delete(recs[0].ID); err != nil {
		t.Fatal(err)
	}
	if m.HasChatSidecar(ts) {
		t.Error("the last master with the stem went, the sidecar stayed")
	}
}

func TestASymlinkedSidecarIsNotServed(t *testing.T) {
	m, dir, _ := newManager(t)
	target := filepath.Join(t.TempDir(), "secret.json")
	_ = os.WriteFile(target, []byte("secret"), 0o644)
	if err := os.Symlink(target, filepath.Join(dir, "rec-20260101-000000.chat.json")); err != nil {
		t.Skip("no symlinks here")
	}
	if m.HasChatSidecar("rec-20260101-000000.mkv") {
		t.Error("a planted symlink counted as a sidecar")
	}
}

func TestChatHoldNamesTheOldestRecordingStillBeingWrittenOrJustFinished(t *testing.T) {
	m, dir, _ := newManager(t)
	now := time.Now()
	seedFinished(t, m, dir, now.Add(-10*time.Hour).Truncate(time.Second), 600_000) // long finished
	if _, ok := m.ChatHold(now); ok {
		t.Fatal("a long-finished recording holds nothing")
	}
	// A 5-hour-old segment still being written: its mtime is now.
	at := now.Add(-5 * time.Hour).Truncate(time.Second)
	name := segmentName(at)
	writeFile(t, dir, name, 8)
	if err := m.store.UpsertRecording(&db.Recording{Filename: name, StartedAt: at}); err != nil {
		t.Fatal(err)
	}
	got, ok := m.ChatHold(now)
	if !ok || !got.Equal(at) {
		t.Errorf("hold = %v %v, want %v", got, ok, at)
	}
}
