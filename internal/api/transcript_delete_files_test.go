package api

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// #7 of the 2026-11-04 lingering-state audit. Deleting a transcript removed the
// rows and left every sidecar file -- the words included -- under
// recordings/transcripts, where the offloader uploads them to any target with
// offloadTranscripts on. Nothing else removed them while the recording existed.
func TestDeletingATranscriptRemovesItsFiles(t *testing.T) {
	dir := t.TempDir()
	s, h, store := testServer(t, config.Config{DataDir: dir})
	sign := login(t, h)
	sub := filepath.Join(s.cfg.RecordingsDir(), transcribe.TranscriptsSubdir)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	index := func(name string) int64 {
		t.Helper()
		if err := store.UpsertRecording(&db.Recording{Filename: name, StartedAt: time.Now().Add(-time.Hour)}); err != nil {
			t.Fatal(err)
		}
		recs, err := store.ListRecordings()
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range recs {
			if r.Filename == name {
				return r.ID
			}
		}
		t.Fatalf("%s not indexed", name)
		return 0
	}
	id := index("rec-20240115-100000.mkv")
	index("rec-20240115-100000-b.mkv")
	if err := store.SaveTranscript(&db.Transcript{RecordingID: id, Tracks: []db.TranscriptTrack{
		{Track: 0, Speaker: "Host", Segments: []db.TranscriptSegment{{Track: 0, StartMS: 0, EndMS: 1000, Text: "hello"}}},
		{Track: 1, Speaker: "Guest", Segments: []db.TranscriptSegment{{Track: 1, StartMS: 0, EndMS: 1000, Text: "hi"}}},
	}}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"rec-20240115-100000.json", "rec-20240115-100000-host.srt",
		"rec-20240115-100000-guest.vtt", "rec-20240115-100000-all.srt",
		"rec-20240115-100000-b.json", "unrelated.srt"} {
		if err := os.WriteFile(filepath.Join(sub, n), []byte("words"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	listing := func() []string {
		entries, err := os.ReadDir(sub)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range entries {
			out = append(out, e.Name())
		}
		return out
	}

	// One track: the merged files and that track's own go, the other track's stays.
	send(t, h, sign, http.MethodDelete, "/api/v1/library/recordings/"+itoa(id)+"/transcript?track=1", nil, http.StatusOK)
	want := []string{"rec-20240115-100000-b.json", "rec-20240115-100000-host.srt", "unrelated.srt"}
	if got := listing(); !slices.Equal(got, want) {
		t.Fatalf("after deleting track 1: %v\nwant %v", got, want)
	}

	// The whole transcript: everything of this recording's, nothing of the
	// longer-named neighbour's.
	send(t, h, sign, http.MethodDelete, "/api/v1/library/recordings/"+itoa(id)+"/transcript", nil, http.StatusOK)
	want = []string{"rec-20240115-100000-b.json", "unrelated.srt"}
	if got := listing(); !slices.Equal(got, want) {
		t.Fatalf("after deleting the transcript: %v\nwant %v", got, want)
	}
}
