package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/clipper"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// The models directory is spelled in two packages — config cannot import
// transcribe without dragging ffmpeg and jobs into a leaf package — so the two
// spellings have to be pinned against each other. A drift here downloads a
// model to one directory and looks for it in another.
func TestModelsDirMatchesTheTranscribePackage(t *testing.T) {
	cfg := config.Config{DataDir: "/var/lib/polyemesis"}
	if got, want := cfg.ModelsDir(), transcribe.ModelsDir(cfg.DataDir); got != want {
		t.Errorf("config.ModelsDir() = %q, transcribe.ModelsDir() = %q", got, want)
	}
}

// Every heavy child has to go out through the governor's priority wrapper. The
// two callback shapes exist because two packages spell the same callback
// differently; they must not become two policies.
func TestNiceWrapperRewritesTheSameCommandTheGovernorWould(t *testing.T) {
	gov := jobs.NewGovernor(slog.Default(), nil)

	wantName, wantArgs := gov.NiceCommand("ffmpeg", "-i", "in.mkv")
	gotName, gotArgs := niceWrapper(gov)("ffmpeg", []string{"-i", "in.mkv"})

	if gotName != wantName {
		t.Errorf("wrapped name = %q, want %q", gotName, wantName)
	}
	if strings.Join(gotArgs, " ") != strings.Join(wantArgs, " ") {
		t.Errorf("wrapped args = %v, want %v", gotArgs, wantArgs)
	}
}

// The clip worker's fallback resolver. The editor sends its segments inline, so
// this only runs for a job submitted without them — and it must refuse rather
// than guess when the target names nothing it can find.
func TestRecordingTimelineResolvesOnlyARealRecordingTarget(t *testing.T) {
	dir := t.TempDir()
	store, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	rec := &db.Recording{Filename: "rec-20240101-100000.mkv", StartedAt: time.Now(), DurationMS: 90_000}
	if err := store.UpsertRecording(rec); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	list, err := store.ListRecordings()
	if err != nil || len(list) != 1 {
		t.Fatalf("list recordings: %v (%d rows)", err, len(list))
	}
	id := list[0].ID

	recordings := filepath.Join(dir, "recordings")
	resolve := recordingTimeline(store, recordings)

	tests := []struct {
		name    string
		target  string
		wantErr bool
	}{
		{name: "a real recording target resolves to its own file", target: jobs.RecordingTarget(id)},
		{name: "a target that is not a recording is refused", target: "session:7", wantErr: true},
		{name: "an empty target is refused", target: "", wantErr: true},
		{name: "a recording that is not indexed is refused", target: jobs.RecordingTarget(id + 999), wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tl, err := resolve(context.Background(), tc.target)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolve(%q) succeeded, want an error", tc.target)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolve(%q): %v", tc.target, err)
			}
			if got, want := tl.Duration(), 90*time.Second; got != want {
				t.Errorf("timeline duration = %v, want %v", got, want)
			}
			segs := tl.Segments()
			if len(segs) != 1 {
				t.Fatalf("timeline has %d segments, want 1", len(segs))
			}
			if want := filepath.Join(recordings, rec.Filename); segs[0].Path != want {
				t.Errorf("segment path = %q, want %q", segs[0].Path, want)
			}
		})
	}
}

// A retention bound of zero means "keep forever" in the settings, and turning
// that into a zero cutoff would delete every finished job on the first start.
func TestPurgeJobHistoryTreatsZeroDaysAsKeepForever(t *testing.T) {
	dir := t.TempDir()
	store, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	old := time.Now().AddDate(0, 0, -400)
	seed := func() int64 {
		j, err := store.EnqueueJob(jobs.Job{Kind: "test.kind", Target: "x", Attempts: 1, MaxAttempts: 1})
		if err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		if err := store.FinishJob(j.ID, jobs.StateDone, "", old); err != nil {
			t.Fatalf("finish: %v", err)
		}
		return j.ID
	}
	id := seed()

	q := jobs.New(slog.Default(), store)
	purgeJobHistory(slog.Default(), q, db.PostProdSettings{RetainDays: 0, RetainJobs: 0})
	if _, err := store.GetJob(id); err != nil {
		t.Fatalf("a job was purged with retention set to keep forever: %v", err)
	}

	purgeJobHistory(slog.Default(), q, db.PostProdSettings{RetainDays: 30, RetainJobs: 0})
	if _, err := store.GetJob(id); err == nil {
		t.Error("a 400-day-old job survived a 30-day retention bound")
	}
}

// clipper's job kind is the queue's deduplication key. A second spelling would
// mean pressing Export twice cuts the clip twice.
func TestTheClipKindIsSpeltOnceAcrossTheWiring(t *testing.T) {
	if clipper.JobKind == "" {
		t.Fatal("clipper.JobKind is empty")
	}
	for _, other := range []jobs.Kind{transcribe.KindTranscribe} {
		if other == clipper.JobKind {
			t.Errorf("two features registered the same job kind %q", other)
		}
	}
}
