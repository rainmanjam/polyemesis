package db

import (
	"testing"
	"time"
)

// A RE-INDEX MUST NOT FORGET WHEN A SEGMENT FINISHED.
//
// The scanner re-indexes every segment on every pass (ScanAndSweep runs every
// 30 s), and it builds db.Recording{Filename, StartedAt, Bytes} with FinishedAt
// zero, because the file's end is not something a directory listing knows. The
// upsert's finished_at clause was unconditional, so each pass wrote 0 over a
// finish time an earlier writer had recorded. Found by #940.
//
// Mutation: restore `finished_at=excluded.finished_at` in UpsertRecording.
// Observed: FAIL, the finish time reads back as zero after a plain re-index.
func TestAPlainReindexOfASegmentKeepsTheFinishTimeAnEarlierWriterRecorded(t *testing.T) {
	d := testDB(t)
	finished := time.Unix(1_800_000_000, 0)

	if err := d.UpsertRecording(&Recording{Filename: "f.mkv", Bytes: 10, FinishedAt: finished}); err != nil {
		t.Fatalf("upsert with a finish time: %v", err)
	}
	// What the scanner does on every pass: the same segment, a new size, and no
	// finish time of its own.
	if err := d.UpsertRecording(&Recording{Filename: "f.mkv", Bytes: 20}); err != nil {
		t.Fatalf("re-index: %v", err)
	}
	got := findRecording(t, d, "f.mkv")
	if got.Bytes != 20 {
		t.Fatalf("re-index did not refresh the size: bytes = %d, want 20", got.Bytes)
	}
	if got.FinishedAt.IsZero() || got.FinishedAt.Unix() != finished.Unix() {
		t.Fatalf("a re-index with no finish time wiped it: finished = %v, want %v", got.FinishedAt, finished)
	}

	// And a later, real finish time still wins: the clause keeps the old value
	// only when the new one is absent.
	later := finished.Add(time.Minute)
	if err := d.UpsertRecording(&Recording{Filename: "f.mkv", Bytes: 30, FinishedAt: later}); err != nil {
		t.Fatalf("upsert with a later finish time: %v", err)
	}
	if got := findRecording(t, d, "f.mkv"); got.FinishedAt.Unix() != later.Unix() {
		t.Fatalf("a new finish time did not replace the old one: finished = %v, want %v", got.FinishedAt, later)
	}
}
