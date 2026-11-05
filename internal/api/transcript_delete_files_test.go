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

// Labels are reduced to a file name, so two tracks can share one: "Host" and
// "host!" are both "-host", and a label of "track2" is the second track's own
// fallback name. The worker already writes both tracks into that file, so
// deleting one of them must leave it for the other.
func TestDeletingATranscriptTrackKeepsAFileAnotherTrackShares(t *testing.T) {
	dir := t.TempDir()
	s, h, store := testServer(t, config.Config{DataDir: dir})
	sign := login(t, h)
	sub := filepath.Join(s.cfg.RecordingsDir(), transcribe.TranscriptsSubdir)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	seg := func(track int) []db.TranscriptSegment {
		return []db.TranscriptSegment{{Track: track, StartMS: 0, EndMS: 1000, Text: "words"}}
	}
	for _, tc := range []struct {
		name     string
		file     string
		speakers [2]string
		files    []string
		want     []string
	}{
		{
			name: "two labels, one file name", file: "rec-20240116-100000.mkv",
			speakers: [2]string{"Host", "host!"},
			// -track2 is track 1's own fallback file and nobody else's: it goes.
			files: []string{"rec-20240116-100000-host.srt", "rec-20240116-100000-track2.srt"},
			want:  []string{"rec-20240116-100000-host.srt"},
		},
		{
			name: "a label that is another track's fallback", file: "rec-20240117-100000.mkv",
			speakers: [2]string{"track2", ""},
			files:    []string{"rec-20240117-100000-track2.srt"},
			want:     []string{"rec-20240117-100000-track2.srt"},
		},
	} {
		if err := store.UpsertRecording(&db.Recording{Filename: tc.file, StartedAt: time.Now().Add(-time.Hour)}); err != nil {
			t.Fatal(err)
		}
		recs, err := store.ListRecordings()
		if err != nil {
			t.Fatal(err)
		}
		var id int64
		for _, r := range recs {
			if r.Filename == tc.file {
				id = r.ID
			}
		}
		if err := store.SaveTranscript(&db.Transcript{RecordingID: id, Tracks: []db.TranscriptTrack{
			{Track: 0, Speaker: tc.speakers[0], Segments: seg(0)},
			{Track: 1, Speaker: tc.speakers[1], Segments: seg(1)},
		}}); err != nil {
			t.Fatal(err)
		}
		for _, n := range tc.files {
			if err := os.WriteFile(filepath.Join(sub, n), []byte("words"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		send(t, h, sign, http.MethodDelete, "/api/v1/library/recordings/"+itoa(id)+"/transcript?track=1", nil, http.StatusOK)
		var left []string
		for _, n := range tc.files {
			if _, err := os.Stat(filepath.Join(sub, n)); err == nil {
				left = append(left, n)
			}
		}
		if !slices.Equal(left, tc.want) {
			t.Errorf("%s: after deleting track 1 these remain %v, want %v", tc.name, left, tc.want)
		}
	}
}
