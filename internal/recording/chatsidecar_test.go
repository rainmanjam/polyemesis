package recording

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/chatexport"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// seedFinished indexes a segment whose file was last written long ago, so the
// manager reads it as finished, and returns its row.
func seedFinished(t *testing.T, m *Manager, dir string, start time.Time, durMS int64) db.Recording {
	t.Helper()
	name := segmentName(start)
	path := writeFile(t, dir, name, 16)
	end := start.Add(time.Duration(durMS) * time.Millisecond)
	if durMS == 0 {
		end = start.Add(30 * time.Minute)
	}
	if err := os.Chtimes(path, end, end); err != nil {
		t.Fatal(err)
	}
	if err := m.store.UpsertRecording(&db.Recording{Filename: name, StartedAt: start, DurationMS: durMS}); err != nil {
		t.Fatal(err)
	}
	recs, _ := m.store.ListRecordings()
	for _, r := range recs {
		if r.Filename == name {
			return r
		}
	}
	t.Fatalf("%s not indexed", name)
	return db.Recording{}
}

func say(t *testing.T, store *db.DB, id string, at time.Time, author, text string) {
	t.Helper()
	if _, err := store.AppendChatMessages([]db.ChatMessage{{Platform: db.PlatformTwitch, Account: "a",
		MessageID: id, AuthorID: "u-" + author, AuthorName: author, Text: text, At: at}}); err != nil {
		t.Fatal(err)
	}
}

func TestFinishedSegmentGetsItsChatSavedBesideIt(t *testing.T) {
	m, dir, store := newManager(t)
	start := time.Now().Add(-3 * time.Hour).Truncate(time.Second)
	rec := seedFinished(t, m, dir, start, 600_000)
	say(t, store, "1", start.Add(1500*time.Millisecond), "ana", "first")
	say(t, store, "2", start.Add(9*time.Minute), "bo", "=SUM(A1)")
	say(t, store, "3", start.Add(11*time.Minute), "late", "after the segment")
	// A message the operator sent never lands in a sidecar.
	if _, err := store.AppendChatMessages([]db.ChatMessage{{Platform: db.PlatformTwitch, MessageID: "e",
		AuthorName: "me", Text: "ours", Echo: true, At: start.Add(time.Minute)}}); err != nil {
		t.Fatal(err)
	}

	if !m.writeChatSidecars() {
		t.Fatal("expected a sidecar to be written")
	}
	if !m.HasChatSidecar(rec.Filename) {
		t.Fatal("HasChatSidecar = false after the write")
	}
	raw, err := os.ReadFile(filepath.Join(dir, strings.TrimSuffix(rec.Filename, ".mkv")+".chat.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schema       int `json:"schema"`
		MessageCount int `json:"messageCount"`
		Recording    struct {
			DurationMs int64  `json:"durationMs"`
			EndSource  string `json:"endSource"`
		} `json:"recording"`
		Messages []struct {
			OffsetMs int64  `json:"offsetMs"`
			Author   string `json:"author"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("sidecar is not json: %v\n%s", err, raw)
	}
	if doc.Schema != 1 || doc.MessageCount != 2 || len(doc.Messages) != 2 ||
		doc.Messages[0].OffsetMs != 1500 || doc.Messages[1].Author != "bo" ||
		doc.Recording.DurationMs != 600_000 || doc.Recording.EndSource != "probe" {
		t.Errorf("sidecar = %s", raw)
	}
	vtt, err := os.ReadFile(filepath.Join(dir, strings.TrimSuffix(rec.Filename, ".mkv")+".chat.vtt"))
	if err != nil || !strings.Contains(string(vtt), "00:00:01.500 --> 00:00:06.500\n[TWITCH] ana: first") {
		t.Errorf("vtt = %q (%v)", vtt, err)
	}
	// Idempotent: a second pass has nothing to do and no temporary file remains.
	if m.writeChatSidecars() {
		t.Error("second pass should write nothing")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("leftover temporary file %s", e.Name())
		}
	}
}

// The last segment of a stream is the newest in the index and is never probed,
// so no duration exists. It must still be saved, ended at its last write.
func TestTheLastSegmentOfAStreamIsSavedFromItsLastWrite(t *testing.T) {
	m, dir, store := newManager(t)
	start := time.Now().Add(-5 * time.Hour).Truncate(time.Second)
	rec := seedFinished(t, m, dir, start, 0) // mtime = start+30m
	say(t, store, "1", start.Add(20*time.Minute), "ana", "inside")
	say(t, store, "2", start.Add(40*time.Minute), "bo", "after the last write")

	if !m.writeChatSidecars() {
		t.Fatal("the newest, unmeasured segment should still be saved")
	}
	raw, _ := os.ReadFile(filepath.Join(dir, strings.TrimSuffix(rec.Filename, ".mkv")+".chat.json"))
	if !strings.Contains(string(raw), `"endSource":"mtime"`) || !strings.Contains(string(raw), "inside") ||
		strings.Contains(string(raw), "after the last write") {
		t.Errorf("sidecar = %s", raw)
	}
}

func TestASegmentStillBeingWrittenIsLeftAlone(t *testing.T) {
	m, dir, store := newManager(t)
	start := time.Now().Add(-10 * time.Minute).Truncate(time.Second)
	name := segmentName(start)
	writeFile(t, dir, name, 16) // mtime is now
	if err := store.UpsertRecording(&db.Recording{Filename: name, StartedAt: start}); err != nil {
		t.Fatal(err)
	}
	say(t, store, "1", start.Add(time.Minute), "ana", "live")
	if m.writeChatSidecars() || m.HasChatSidecar(name) {
		t.Error("a file written to a moment ago is not finished")
	}
}

func TestAWindowWithNoChatGetsNoSidecarAndIsNotAskedAgain(t *testing.T) {
	m, dir, _ := newManager(t)
	rec := seedFinished(t, m, dir, time.Now().Add(-4*time.Hour).Truncate(time.Second), 600_000)
	if m.writeChatSidecars() || m.HasChatSidecar(rec.Filename) {
		t.Error("an empty window must not leave an empty file that reads as 'nobody chatted'")
	}
	fi, _ := os.Stat(filepath.Join(dir, rec.Filename))
	if !m.chatSettled(rec.Filename, fi.ModTime()) {
		t.Error("a quiet recording should be remembered, not re-queried every scan")
	}
}

func TestSidecarsAreRemovedWithTheirRecordingAndSweptWhenOrphaned(t *testing.T) {
	m, dir, store := newManager(t)
	start := time.Now().Add(-6 * time.Hour).Truncate(time.Second)
	rec := seedFinished(t, m, dir, start, 600_000)
	other := seedFinished(t, m, dir, start.Add(-2*time.Hour), 600_000)
	say(t, store, "1", start.Add(time.Minute), "ana", "x")
	say(t, store, "2", start.Add(-2*time.Hour).Add(time.Minute), "ana", "y")
	m.writeChatSidecars()
	if !m.HasChatSidecar(rec.Filename) || !m.HasChatSidecar(other.Filename) {
		t.Fatal("both should have sidecars")
	}

	// The delete button.
	if err := m.Delete(rec.ID); err != nil {
		t.Fatal(err)
	}
	if m.HasChatSidecar(rec.Filename) {
		t.Error("Delete left the json sidecar")
	}
	if _, ok := m.ChatSidecar(rec.Filename, true); ok {
		t.Error("Delete left the vtt sidecar")
	}
	if !m.HasChatSidecar(other.Filename) {
		t.Error("Delete reached a neighbour's sidecar")
	}

	// Retention (the sweeper's delete path).
	if !m.delete(other, "test") {
		t.Fatal("delete refused")
	}
	if m.HasChatSidecar(other.Filename) {
		t.Error("retention left the sidecar")
	}

	// Orphans: a recording that vanished from the index, a stale temp file, and
	// the guard that an empty index deletes nothing.
	keep := seedFinished(t, m, dir, start.Add(time.Hour), 600_000)
	jsonName, _ := chatexport.SidecarNames("rec-20200101-000000.mkv")
	orphan := filepath.Join(dir, jsonName)
	stale := filepath.Join(dir, ".x.chat.json.1.tmp")
	fresh := filepath.Join(dir, ".y.chat.json.2.tmp")
	for _, p := range []string{orphan, stale, fresh} {
		if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if n := m.sweepChatSidecars(map[string]bool{}); n != 0 {
		t.Errorf("an empty index removed %d files", n)
	}
	if n := m.sweepChatSidecars(map[string]bool{keep.Filename: true}); n != 2 {
		t.Errorf("swept %d, want the orphan and the stale temp", n)
	}
	for p, want := range map[string]bool{orphan: false, stale: false, fresh: true} {
		if _, err := os.Stat(p); (err == nil) != want {
			t.Errorf("%s exists = %v, want %v", filepath.Base(p), err == nil, want)
		}
	}
}

func TestChatWindowClosesTheSeamBetweenConsecutiveSegments(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	src := int64(3)
	a := db.Recording{ID: 1, StartedAt: t0, DurationMS: 3_600_400, SourceID: &src}
	b := db.Recording{ID: 2, StartedAt: t0.Add(time.Hour), DurationMS: 100, SourceID: &src}
	other := int64(4)
	c := db.Recording{ID: 3, StartedAt: t0.Add(time.Hour + time.Second), DurationMS: 100, SourceID: &other}
	all := []db.Recording{a, b, c}

	from, to, endSource, ok := ChatWindow(a, all, time.Time{})
	if !ok || endSource != "probe" || from != t0.UnixMilli() || to != b.StartedAt.UnixMilli() {
		t.Errorf("window = %d..%d %s %v; the 400ms overshoot should close up to the next file's start", from, to, endSource, ok)
	}
	// A different programme's file is not this one's neighbour.
	_, to, _, _ = ChatWindow(b, all, time.Time{})
	if to != b.StartedAt.UnixMilli()+100 {
		t.Errorf("end = %d", to)
	}
	// A real gap is left alone.
	far := db.Recording{ID: 4, StartedAt: t0.Add(2 * time.Hour), SourceID: &src}
	if _, to, _, _ := ChatWindow(a, []db.Recording{a, far}, time.Time{}); to != t0.UnixMilli()+3_600_400 {
		t.Errorf("a gap of an hour was closed: %d", to)
	}
	// No duration and a stale mtime before the start: no window.
	if _, _, _, ok := ChatWindow(db.Recording{StartedAt: t0}, nil, t0.Add(-time.Second)); ok {
		t.Error("a window that ends before it starts should not exist")
	}
}

func TestChatPlanForAnUnmeasuredLiveSegmentEndsAtItsLastWrite(t *testing.T) {
	m, dir, store := newManager(t)
	start := time.Now().Add(-time.Hour).Truncate(time.Second)
	rec := seedFinished(t, m, dir, start, 0)
	say(t, store, "1", start.Add(time.Minute), "ana", "x")
	plan, err := m.ChatPlan(rec)
	if err != nil || plan.Header.MessageCount != 1 || !plan.Retained() {
		t.Fatalf("plan = %+v err=%v", plan.Header, err)
	}
	if plan.Header.Recording == nil || plan.Header.Recording.EndSource != "mtime" {
		t.Errorf("recording = %+v", plan.Header.Recording)
	}
}

// A recorder that sat idle past the settle window and then carried on writing
// leaves a sidecar older than its master; the next scan saves the chat again.
func TestASidecarOlderThanItsMasterIsSavedAgain(t *testing.T) {
	m, dir, store := newManager(t)
	start := time.Now().Add(-5 * time.Hour).Truncate(time.Second)
	rec := seedFinished(t, m, dir, start, 0) // mtime = start+30m
	say(t, store, "1", start.Add(10*time.Minute), "ana", "early")
	if !m.writeChatSidecars() {
		t.Fatal("first save")
	}
	// The recorder wakes up and appends: the master is now newer than the sidecar.
	// (The sidecar was written "now"; put its time where it would have been, an
	// hour after the segment first went quiet.)
	sidecar := filepath.Join(dir, strings.TrimSuffix(rec.Filename, ".mkv")+".chat.json")
	written := start.Add(time.Hour)
	if err := os.Chtimes(sidecar, written, written); err != nil {
		t.Fatal(err)
	}
	say(t, store, "2", start.Add(2*time.Hour), "bo", "after the pause")
	master := filepath.Join(dir, rec.Filename)
	later := start.Add(3 * time.Hour)
	if err := os.Chtimes(master, later, later); err != nil {
		t.Fatal(err)
	}
	if !m.writeChatSidecars() {
		t.Fatal("a master newer than its sidecar should be saved again")
	}
	raw, _ := os.ReadFile(filepath.Join(dir, strings.TrimSuffix(rec.Filename, ".mkv")+".chat.json"))
	if !strings.Contains(string(raw), "after the pause") {
		t.Errorf("the rewrite did not pick up the later chat: %s", raw)
	}
	if m.writeChatSidecars() {
		t.Error("and then it is settled again")
	}
}
