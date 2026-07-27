package api

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/clipper"
	"github.com/rainmanjam/polyemesis/internal/db"
)

func TestSafeClipNameReducesOperatorTextToAFilename(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain text keeps its words", "Cold open", "Cold-open"},
		{"a run of separators collapses to one", "a   b///c", "a-b-c"},
		{"path separators cannot survive", "../../etc/passwd", "etc-passwd"},
		{"a leading separator is trimmed", "  -- hello", "hello"},
		{"dots and underscores are kept", "take_2.final", "take_2.final"},
		{"nothing usable yields nothing", "///", ""},
		{"an empty name yields nothing", "   ", ""},
		{"a long name is bounded", string(make([]byte, 400)), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := safeClipName(tc.in); got != tc.want {
				t.Fatalf("safeClipName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestClipOffsetsPrefersWallClockAndFallsBackWhenItCannotBeTrusted(t *testing.T) {
	base := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		recs []db.Recording
		want []int64
		ok   bool
	}{
		{
			name: "consecutive segments keep the gap between them",
			recs: []db.Recording{
				{StartedAt: base},
				{StartedAt: base.Add(time.Hour)},
				// Ten minutes late: a recorder restart, and the gap is real.
				{StartedAt: base.Add(2*time.Hour + 10*time.Minute)},
			},
			want: []int64{0, 3600000, 7800000},
			ok:   true,
		},
		{
			name: "one unindexed start disqualifies the whole clock",
			recs: []db.Recording{{StartedAt: base}, {}},
			ok:   false,
		},
		{
			name: "a first segment with no start disqualifies it too",
			recs: []db.Recording{{}, {StartedAt: base}},
			ok:   false,
		},
		{
			name: "a segment before the anchor is refused rather than placed negative",
			recs: []db.Recording{{StartedAt: base}, {StartedAt: base.Add(-time.Minute)}},
			ok:   false,
		},
		{"no segments at all", nil, nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := clipOffsets(tc.recs)
			if ok != tc.ok {
				t.Fatalf("clipOffsets ok = %v, want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d offsets, want %d", len(got), len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("offset %d = %d, want %d", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestClipAudioOffersOnlyBitExactSelections(t *testing.T) {
	tests := []struct {
		name    string
		body    clipRequestBody
		want    clipper.AudioMode
		wantErr bool
	}{
		{"empty means every track", clipRequestBody{}, clipper.AudioAll, false},
		{"all is spelled out", clipRequestBody{AudioMode: "all"}, clipper.AudioAll, false},
		{"a subset is carried through", clipRequestBody{AudioMode: "tracks", Tracks: []int{1, 3}}, clipper.AudioTracks, false},
		// A mix re-encodes, which would break the bit-exact promise the whole
		// feature is sold on, so the editor never offers it.
		{"a mix is refused", clipRequestBody{AudioMode: "mix"}, "", true},
		{"nonsense is refused", clipRequestBody{AudioMode: "loudest"}, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := clipAudio(tc.body)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				if !errors.Is(err, clipper.ErrInvalidRequest) {
					t.Fatalf("error %v does not wrap ErrInvalidRequest", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Mode != tc.want {
				t.Fatalf("mode = %q, want %q", got.Mode, tc.want)
			}
			if tc.want == clipper.AudioTracks && len(got.Tracks) != len(tc.body.Tracks) {
				t.Fatalf("tracks = %v, want %v", got.Tracks, tc.body.Tracks)
			}
		})
	}
}

func TestClipTimelineSkipsSegmentsThatAreNotOnDisk(t *testing.T) {
	tests := []struct {
		name    string
		parts   []clipPart
		want    int
		wantErr error
	}{
		{
			name: "every file present",
			parts: []clipPart{
				{rec: db.Recording{DurationMS: 1000}, path: "/a.mkv"},
				{rec: db.Recording{DurationMS: 1000}, startMS: 1000, path: "/b.mkv"},
			},
			want: 2,
		},
		{
			name: "a swept file leaves the rest cuttable",
			parts: []clipPart{
				{rec: db.Recording{DurationMS: 1000}, path: ""},
				{rec: db.Recording{DurationMS: 1000}, startMS: 1000, path: "/b.mkv"},
			},
			want: 1,
		},
		{
			name:    "nothing on disk is a refusal, not an empty timeline",
			parts:   []clipPart{{rec: db.Recording{DurationMS: 1000}}},
			wantErr: errClipFilesMissing,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tl, err := clipTimeline{parts: tc.parts}.timeline()
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := len(tl.Segments()); got != tc.want {
				t.Fatalf("got %d segments, want %d", got, tc.want)
			}
		})
	}
}

// The drift is the number this whole feature exists to be honest about, so the
// view has to carry it in the units the browser asked the range in.
func TestClipPlanViewReportsDriftInMilliseconds(t *testing.T) {
	tests := []struct {
		name       string
		plan       clipper.Plan
		wantDrift  int64
		wantKnown  bool
		wantInMS   int64
		wantWarned bool
	}{
		{
			name: "a fast cut that moved back two seconds says so",
			plan: clipper.Plan{
				RequestedIn: 10 * time.Second,
				In:          8 * time.Second,
				Out:         20 * time.Second,
				InDrift:     -2 * time.Second,
				DriftKnown:  true,
			},
			wantDrift: -2000,
			wantKnown: true,
			wantInMS:  8000,
		},
		{
			name: "an unreadable index reports unknown rather than zero",
			plan: clipper.Plan{
				RequestedIn: 10 * time.Second,
				In:          10 * time.Second,
				Out:         20 * time.Second,
				DriftKnown:  false,
				Warnings:    []string{"the keyframe positions could not be read"},
			},
			wantDrift:  0,
			wantKnown:  false,
			wantInMS:   10000,
			wantWarned: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := clipPlanViewOf(tc.plan)
			if got.InDriftMS != tc.wantDrift {
				t.Fatalf("inDriftMs = %d, want %d", got.InDriftMS, tc.wantDrift)
			}
			if got.DriftKnown != tc.wantKnown {
				t.Fatalf("driftKnown = %v, want %v", got.DriftKnown, tc.wantKnown)
			}
			if got.InMS != tc.wantInMS {
				t.Fatalf("inMs = %d, want %d", got.InMS, tc.wantInMS)
			}
			if got.Describe == "" {
				t.Fatal("describe is empty; the confirm line is what a human reads")
			}
			// Never nil: the UI maps over it, and a null would be a crash on a
			// page whose whole job is to report what will happen.
			if got.Warnings == nil {
				t.Fatal("warnings is nil, want an empty slice")
			}
			if tc.wantWarned && len(got.Warnings) == 0 {
				t.Fatal("expected the plan's warnings to survive into the view")
			}
		})
	}
}

func TestClipExportPathRefusesAnythingOutsideTheExportsDirectory(t *testing.T) {
	// The guard is what is under test, not the server's directory layout, so
	// the base is passed in directly.
	base := filepath.Join(t.TempDir(), clipExportSubdir)

	tests := []struct {
		name    string
		stored  string
		wantErr bool
	}{
		{"a file the worker wrote", filepath.Join(base, "clip-000010.mkv"), false},
		{"a traversal out of the directory", filepath.Join(base, "..", "..", "etc", "passwd"), true},
		{"an unrelated absolute path", filepath.Join(string(filepath.Separator), "etc", "passwd"), true},
		{"the directory itself", base, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := clipPathIn(base, tc.stored)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected %q to be refused, got %q", tc.stored, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// The bug this pins was found in a browser, not in a test: on a stock install
// config.DataDir is "./data", so the exports directory was relative, and
// clipper.Request.Validate refused every plan with "output path ... is not
// absolute". The clip editor's whole right-hand panel was an error message.
func TestClipExportDirIsAlwaysAbsoluteBecauseTheDataDirIsRelativeByDefault(t *testing.T) {
	tests := []struct {
		name          string
		recordingsDir string
	}{
		{"the stock relative data directory", filepath.Join(".", "data", "recordings")},
		{"a bare relative directory", "recordings"},
		{"an already-absolute directory", filepath.Join(string(filepath.Separator), "var", "lib", "polyemesis", "recordings")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := clipExportDirIn(tc.recordingsDir)
			if !filepath.IsAbs(got) {
				t.Errorf("clipExportDirIn(%q) = %q, which clipper.Request.Validate refuses", tc.recordingsDir, got)
			}
			if filepath.Base(got) != clipExportSubdir {
				t.Errorf("clipExportDirIn(%q) = %q, expected it to end in %q", tc.recordingsDir, got, clipExportSubdir)
			}
		})
	}
}
