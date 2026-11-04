package api

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/media"
	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// The UI's RecordingJobKind union and buildJob's accepted kinds are one list
// written twice. Every kind in the union must be one buildJob knows, and every
// kind buildJob builds must be in the union, or the type is lying in one
// direction (poka-yoke audit 2026-11-04).
func TestTheUIsRecordingJobKindsAreTheOnesTheServerBuilds(t *testing.T) {
	raw, err := os.ReadFile("../../ui/src/lib/types.ts")
	if err != nil {
		t.Fatalf("read types.ts: %v", err)
	}
	m := regexp.MustCompile(`export type RecordingJobKind = ([^;]+);`).FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatal("ui/src/lib/types.ts has no RecordingJobKind union; this guard would check nothing")
	}
	var ui []string
	for _, lit := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(m[1], -1) {
		ui = append(ui, lit[1])
	}
	slices.Sort(ui)

	server := []string{string(transcribe.KindTranscribe), string(media.KindProxy),
		string(media.KindThumbnails), string(media.KindArchive)}
	slices.Sort(server)
	if !slices.Equal(ui, server) {
		t.Errorf("RecordingJobKind = %v, server builds %v", ui, server)
	}

	s, _, _ := testServer(t, config.Config{})
	for _, k := range ui {
		_, err := s.buildJob(jobs.Kind(k), db.Recording{ID: 1, Filename: "rec.mkv", DurationMS: 60000}, submitRequest{})
		if err != nil && strings.Contains(err.Error(), "unknown job kind") {
			t.Errorf("the UI may submit %q, which buildJob does not know", k)
		}
	}
}
