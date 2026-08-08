package uploads

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mediaStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func writeUpload(t *testing.T, s *Store, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestMediaInfoSurvivesARoundTrip(t *testing.T) {
	s := mediaStore(t)
	writeUpload(t, s, "show-abcd1234.mp4", "not really an mp4")

	want := MediaInfo{
		DurationSeconds: 75.321,
		VideoCodec:      "h264",
		Width:           1920,
		Height:          1080,
		FrameRate:       30,
		AudioTracks:     3,
		AudioCodec:      "aac",
		AudioChannels:   2,
		AudioLayout:     "stereo",
		ProbedAt:        time.Now().UTC().Truncate(time.Second),
	}
	if err := s.PutMedia("show-abcd1234.mp4", want); err != nil {
		t.Fatalf("PutMedia: %v", err)
	}

	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List returned %d files, want 1: %+v", len(list), list)
	}
	got := list[0].Media
	if got == nil {
		t.Fatal("the listing carried no media info")
	}
	// The audio track count is the field this feature exists for -- routing is
	// per track, so "does this file have three" is the question.
	if got.AudioTracks != 3 {
		t.Errorf("AudioTracks = %d, want 3", got.AudioTracks)
	}
	if got.Width != 1920 || got.Height != 1080 {
		t.Errorf("resolution = %dx%d, want 1920x1080", got.Width, got.Height)
	}
	if got.DurationSeconds != 75.321 {
		t.Errorf("DurationSeconds = %v, want 75.321", got.DurationSeconds)
	}
}

// The sidecar is a file in the same directory, so the listing has to skip it.
// Left in, it would be offered as a pull source -- a JSON document presented as
// something to broadcast.
func TestListDoesNotOfferSidecarsAsMedia(t *testing.T) {
	s := mediaStore(t)
	writeUpload(t, s, "clip-11112222.ts", "x")
	if err := s.PutMedia("clip-11112222.ts", MediaInfo{AudioTracks: 1}); err != nil {
		t.Fatalf("PutMedia: %v", err)
	}
	// Present on disk, so this test is about the filter rather than about
	// whether the file was written.
	entries, _ := os.ReadDir(s.dir)
	if len(entries) != 2 {
		t.Fatalf("expected the upload and its sidecar on disk, got %d entries", len(entries))
	}

	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List returned %d files, want only the upload: %+v", len(list), list)
	}
	if strings.HasPrefix(list[0].Name, sidecarPrefix) {
		t.Errorf("List offered the sidecar itself: %q", list[0].Name)
	}
}

// A stale probe outliving its file would describe something that is gone.
func TestDeleteTakesTheSidecarWithIt(t *testing.T) {
	s := mediaStore(t)
	writeUpload(t, s, "old-99998888.mkv", "x")
	if err := s.PutMedia("old-99998888.mkv", MediaInfo{AudioTracks: 2}); err != nil {
		t.Fatalf("PutMedia: %v", err)
	}
	if err := s.Delete("old-99998888.mkv"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	entries, _ := os.ReadDir(s.dir)
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("Delete left %v behind", names)
	}
}

// Anything uploaded before probing existed has no sidecar, and must still list.
// A nil here is what the UI keys its "unknown" rendering off.
func TestAnUploadWithoutASidecarStillLists(t *testing.T) {
	s := mediaStore(t)
	writeUpload(t, s, "legacy-77776666.mp4", "x")
	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List returned %d files, want 1", len(list))
	}
	if list[0].Media != nil {
		t.Errorf("Media = %+v, want nil for a file with no sidecar", list[0].Media)
	}
}

// An unreadable sidecar costs a column, not the listing. The alternative is one
// corrupt cache file making the whole Library page fail to load.
func TestACorruptSidecarDoesNotBreakTheListing(t *testing.T) {
	s := mediaStore(t)
	writeUpload(t, s, "good-55554444.mp4", "x")
	if err := os.WriteFile(
		filepath.Join(s.dir, sidecarName("good-55554444.mp4")),
		[]byte("{ this is not json"), 0o600,
	); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	list, err := s.List()
	if err != nil {
		t.Fatalf("List returned an error for a corrupt sidecar: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List returned %d files, want 1", len(list))
	}
	if list[0].Media != nil {
		t.Errorf("Media = %+v, want nil when the sidecar could not be parsed", list[0].Media)
	}
}

// PutMedia takes a stored name, which is generated here and never
// client-supplied -- but it writes a path, so it refuses a traversal rather
// than trusting that.
func TestPutMediaRefusesANameThatIsAPath(t *testing.T) {
	s := mediaStore(t)
	for _, bad := range []string{"../escape.mp4", "sub/dir.mp4", `back\slash.mp4`, ""} {
		if err := s.PutMedia(bad, MediaInfo{}); err == nil {
			t.Errorf("PutMedia(%q) was accepted", bad)
		}
	}
}
