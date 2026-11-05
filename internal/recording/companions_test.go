package recording

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// #6 of the 2026-11-04 lingering-state audit. A recording deleted by hand kept
// its stems until every OLDER recording was gone (SweepStems only removes stems
// before the oldest surviving master), and when it was the last recording in
// the index its stems and transcripts stayed for good -- both sweeps do nothing
// on an empty index, by design. The delete dialog promised otherwise.

type companionFixture struct {
	m     *Manager
	dir   string
	store *db.DB
	recs  map[string]db.Recording
}

func newCompanionFixture(t *testing.T) *companionFixture {
	t.Helper()
	m, dir, store := newManager(t)
	for _, sub := range []string{StemsSubdir, transcribe.TranscriptsSubdir} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return &companionFixture{m: m, dir: dir, store: store, recs: map[string]db.Recording{}}
}

// master indexes a recording well outside the live window, so Delete takes it.
func (f *companionFixture) master(t *testing.T, name string, age time.Duration) {
	t.Helper()
	writeFile(t, f.dir, name, 16)
	if err := f.store.UpsertRecording(&db.Recording{Filename: name, StartedAt: time.Now().Add(-age)}); err != nil {
		t.Fatal(err)
	}
	recs, err := f.store.ListRecordings()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.Filename == name {
			f.recs[name] = r
		}
	}
}

func (f *companionFixture) file(t *testing.T, sub, name string) {
	t.Helper()
	writeFile(t, filepath.Join(f.dir, sub), name, 8)
}

func (f *companionFixture) listing(t *testing.T, sub string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(f.dir, sub))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func (f *companionFixture) del(t *testing.T, name string) {
	t.Helper()
	if err := f.m.Delete(f.recs[name].ID); err != nil {
		t.Fatalf("Delete(%s): %v", name, err)
	}
}

func TestDeletingARecordingRemovesItsStemsAndTranscriptsButNoNeighbours(t *testing.T) {
	f := newCompanionFixture(t)
	f.master(t, "rec-s1-20240115-100000.mkv", 9*time.Hour)
	f.master(t, "rec-s1-20240115-110000.mkv", 8*time.Hour)
	f.master(t, "rec-s1-20240115-120000.mkv", 7*time.Hour)
	// A recording whose name starts with the deleted one's. Its transcript must
	// not be taken for the shorter name's.
	f.master(t, "rec-s1-20240115-110000-b.mkv", 6*time.Hour)

	// Each master's stem is stamped two seconds EARLY, the way a stem that cut
	// on its segment boundary is, so an exact-name match would miss it.
	for _, s := range []string{
		"rec-s1-20240115-095958-mic.flac", "rec-s1-20240115-105958-mic.flac", "rec-s1-20240115-105958-guest.flac",
		"rec-s1-20240115-115958-mic.flac",
		// Another programme's stem, inside the deleted recording's window.
		"rec-s9-20240115-110001-mic.flac",
	} {
		f.file(t, StemsSubdir, s)
	}
	for _, base := range []string{"rec-s1-20240115-100000", "rec-s1-20240115-110000", "rec-s1-20240115-120000"} {
		for _, suffix := range []string{".json", "-mic.srt", "-all.vtt"} {
			f.file(t, transcribe.TranscriptsSubdir, base+suffix)
		}
	}
	f.file(t, transcribe.TranscriptsSubdir, "rec-s1-20240115-110000-b.json")

	f.del(t, "rec-s1-20240115-110000.mkv")

	wantStems := []string{"rec-s1-20240115-095958-mic.flac", "rec-s1-20240115-115958-mic.flac",
		"rec-s9-20240115-110001-mic.flac"}
	if got := f.listing(t, StemsSubdir); !slices.Equal(got, wantStems) {
		t.Errorf("stems after the delete: %v\nwant %v", got, wantStems)
	}
	wantTranscripts := []string{
		"rec-s1-20240115-100000-all.vtt", "rec-s1-20240115-100000-mic.srt", "rec-s1-20240115-100000.json",
		"rec-s1-20240115-110000-b.json",
		"rec-s1-20240115-120000-all.vtt", "rec-s1-20240115-120000-mic.srt", "rec-s1-20240115-120000.json",
	}
	if got := f.listing(t, transcribe.TranscriptsSubdir); !slices.Equal(got, wantTranscripts) {
		t.Errorf("transcripts after the delete: %v\nwant %v", got, wantTranscripts)
	}
}

// The last recording: both sweeps stand down on an empty index, so only the
// delete can take its companions.
func TestDeletingTheOnlyRecordingRemovesItsStemsAndTranscripts(t *testing.T) {
	f := newCompanionFixture(t)
	f.master(t, "rec-s1-20240115-100000.mkv", 9*time.Hour)
	f.file(t, StemsSubdir, "rec-s1-20240115-095959-mic.flac")
	f.file(t, transcribe.TranscriptsSubdir, "rec-s1-20240115-100000.json")
	f.file(t, transcribe.TranscriptsSubdir, "rec-s1-20240115-100000-mic.srt")

	f.del(t, "rec-s1-20240115-100000.mkv")

	if got := f.listing(t, StemsSubdir); len(got) != 0 {
		t.Errorf("the only recording's stems survive its delete: %v", got)
	}
	if got := f.listing(t, transcribe.TranscriptsSubdir); len(got) != 0 {
		t.Errorf("the only recording's transcripts survive its delete: %v", got)
	}
}

// A stem two masters claim equally is kept: the irreversible mistake is taking
// a neighbour's audio, so a tie is not this delete's.
func TestAStemEquallyNearTwoMastersIsKept(t *testing.T) {
	f := newCompanionFixture(t)
	f.master(t, "rec-s1-20240115-100000.mkv", 9*time.Hour)
	f.master(t, "rec-s1-20240115-100040.mkv", 8*time.Hour)
	f.file(t, StemsSubdir, "rec-s1-20240115-100020-mic.flac")
	f.del(t, "rec-s1-20240115-100000.mkv")
	if got := f.listing(t, StemsSubdir); len(got) != 1 {
		t.Errorf("a stem as near the surviving master as the deleted one was removed: %v", got)
	}
}

// #7's half in this package: one track's delete takes the merged files and
// that track's own, and leaves the other track's.
func TestRemoveTranscriptFilesForOneTrack(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, transcribe.TranscriptsSubdir)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"rec-a.json", "rec-a-all.srt", "rec-a-all.vtt", "rec-a-host.srt", "rec-a-guest.srt",
		"rec-a-b.json", "rec-a-b-guest.srt", "rec-z.json"} {
		writeFile(t, sub, n, 4)
	}
	removed, err := RemoveTranscriptFiles(dir, "rec-a.mkv", []string{"rec-a-b.mkv", "rec-z.mkv"},
		&TranscriptTrackFiles{SpeakerParts: []string{"guest", "track2"}})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(removed)
	want := []string{"rec-a-all.srt", "rec-a-all.vtt", "rec-a-guest.srt", "rec-a.json"}
	if !slices.Equal(removed, want) {
		t.Errorf("removed %v, want %v", removed, want)
	}
	// Whole-transcript delete takes the rest of rec-a's and nothing of rec-a-b's.
	removed, err = RemoveTranscriptFiles(dir, "rec-a.mkv", []string{"rec-a-b.mkv", "rec-z.mkv"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(removed, []string{"rec-a-host.srt"}) {
		t.Errorf("whole delete removed %v, want only rec-a-host.srt", removed)
	}
}
