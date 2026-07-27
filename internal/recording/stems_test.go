package recording

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// src builds a Source with the given tracks and annotations, matching what the
// engine hands PlanStems: probe results with saved annotations hung off them.
func src(tracks []routing.Track, ann ...routing.TrackAnnotation) routing.Source {
	return routing.Source{Tracks: tracks}.WithAnnotations(ann)
}

func stereo(indices ...int) []routing.Track {
	out := make([]routing.Track, 0, len(indices))
	for _, i := range indices {
		out = append(out, routing.Track{Index: i, Channels: 2, Codec: "aac"})
	}
	return out
}

func stemNames(stems []Stem) []string {
	out := make([]string, 0, len(stems))
	for _, s := range stems {
		out = append(out, s.Name)
	}
	return out
}

func TestPlanStemsNames(t *testing.T) {
	tests := []struct {
		name   string
		source routing.Source
		want   []string
	}{
		{
			name:   "an unannotated track falls back to its 1-based track number",
			source: src(stereo(0, 1)),
			want:   []string{"track1", "track2"},
		},
		{
			name: "roles name the file",
			source: src(stereo(0, 1, 2),
				routing.TrackAnnotation{Track: 0, Role: routing.RoleMic},
				routing.TrackAnnotation{Track: 1, Role: routing.RoleMusic},
				routing.TrackAnnotation{Track: 2, Role: routing.RoleGame}),
			want: []string{"mic", "music", "game"},
		},
		{
			name: "a role beats a label, because the vocabulary is closed",
			source: src(stereo(0),
				routing.TrackAnnotation{Track: 0, Role: routing.RoleMic, Label: "Guest mic (Zoom)"}),
			want: []string{"mic"},
		},
		{
			name: "a label is used when there is no role",
			source: src(stereo(0),
				routing.TrackAnnotation{Track: 0, Label: "Guest mic (Zoom)"}),
			want: []string{"guest-mic-zoom"},
		},
		{
			name:   "the container title is used when the operator said nothing",
			source: src([]routing.Track{{Index: 0, Channels: 2, Title: "Commentary EN"}}),
			want:   []string{"commentary-en"},
		},
		{
			name: "duplicate roles suffix BOTH files with their track number",
			source: src(stereo(0, 1),
				routing.TrackAnnotation{Track: 0, Role: routing.RoleMic},
				routing.TrackAnnotation{Track: 1, Role: routing.RoleMic}),
			want: []string{"mic-1", "mic-2"},
		},
		{
			name: "a duplicate among distinct names leaves the others alone",
			source: src(stereo(0, 1, 2),
				routing.TrackAnnotation{Track: 0, Role: routing.RoleMic},
				routing.TrackAnnotation{Track: 1, Role: routing.RoleMic},
				routing.TrackAnnotation{Track: 2, Role: routing.RoleMusic}),
			want: []string{"mic-1", "mic-2", "music"},
		},
		{
			name: "a label that collides with a suffixed name still gets a unique file",
			source: src(stereo(0, 1, 2),
				routing.TrackAnnotation{Track: 0, Role: routing.RoleMic},
				routing.TrackAnnotation{Track: 1, Role: routing.RoleMic},
				routing.TrackAnnotation{Track: 2, Label: "mic-2"}),
			want: []string{"mic-1", "mic-2", "mic-2-3-2"},
		},
		{
			name: "a label of pure punctuation falls through to the track number",
			source: src(stereo(0),
				routing.TrackAnnotation{Track: 0, Label: "!!! ??? ///"}),
			want: []string{"track1"},
		},
		{
			name: "a purely numeric label is not a name",
			source: src(stereo(4),
				routing.TrackAnnotation{Track: 4, Label: "2024"}),
			want: []string{"track5"},
		},
		{
			name:   "tracks are planned in index order regardless of probe order",
			source: src(stereo(2, 0, 1)),
			want:   []string{"track1", "track2", "track3"},
		},
		{
			name:   "no tracks plans no stems",
			source: src(nil),
			want:   []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stemNames(PlanStems(tt.source, ffmpeg.StemFLAC))
			if !slices.Equal(got, tt.want) {
				t.Fatalf("names = %v, want %v", got, tt.want)
			}
		})
	}
}

// The name reaches a filesystem path. Anything that could escape the directory,
// hide the file, or break a shell has to be gone before it gets there.
func TestPlanStemsNameIsAlwaysASafeFilename(t *testing.T) {
	tests := []struct {
		name  string
		label string
		want  string
	}{
		{"path separators collapse", "../../etc/passwd", "etc-passwd"},
		{"a leading dot cannot hide the file", ".hidden", "hidden"},
		{"windows separators collapse too", `C:\Users\mic`, "c-users-mic"},
		{"shell metacharacters collapse", "mic; rm -rf /", "mic-rm-rf"},
		{"a null byte collapses", "mi\x00c", "mi-c"},
		{"non-ascii collapses rather than reaching the filesystem", "микрофон", ""},
		{"emoji collapse", "mic 🎤", "mic"},
		{"newlines collapse", "mic\nmusic", "mic-music"},
		{"runs of separators become one", "a   ---   b", "a-b"},
		{"a very long label is truncated at a separator boundary", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbb", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeStemName(tt.label); got != tt.want {
				t.Fatalf("sanitizeStemName(%q) = %q, want %q", tt.label, got, tt.want)
			}
			// Whatever survives must still be a single, non-hidden path element.
			s := PlanStems(src(stereo(0), routing.TrackAnnotation{Track: 0, Label: tt.label}), ffmpeg.StemFLAC)
			path := StemPattern("/rec", "/rec/rec-%Y%m%d-%H%M%S.mkv", s[0])
			if filepath.Dir(path) != filepath.Join("/rec", StemsSubdir) {
				t.Fatalf("stem escaped the stems directory: %q", path)
			}
		})
	}
}

func TestPlanStemsCodecSelection(t *testing.T) {
	tests := []struct {
		name     string
		codec    ffmpeg.StemCodec
		channels int
		want     ffmpeg.StemCodec
	}{
		{"flac is the default", "", 2, ffmpeg.StemFLAC},
		{"flac is honoured", ffmpeg.StemFLAC, 2, ffmpeg.StemFLAC},
		{"wav is honoured", ffmpeg.StemWAV, 2, ffmpeg.StemWAV},
		{"7.1 still fits in flac", ffmpeg.StemFLAC, 8, ffmpeg.StemFLAC},
		{"a track wider than flac allows falls back to wav", ffmpeg.StemFLAC, 12, ffmpeg.StemWAV},
		{"an unmeasured width is not a reason to change codec", ffmpeg.StemFLAC, 0, ffmpeg.StemFLAC},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := PlanStems(src([]routing.Track{{Index: 0, Channels: tt.channels}}), tt.codec)
			if s[0].Codec != tt.want {
				t.Fatalf("codec = %q, want %q", s[0].Codec, tt.want)
			}
			// The extension has to follow the codec, or a .flac file holds WAV.
			path := StemPattern("/rec", "/rec/rec-%Y%m%d-%H%M%S.mkv", s[0])
			if filepath.Ext(path) != tt.want.Ext() {
				t.Fatalf("path %q does not carry the %q extension", path, tt.want)
			}
		})
	}
}

func TestStemPattern(t *testing.T) {
	tests := []struct {
		name    string
		dir     string
		master  string
		stem    Stem
		want    string
		wantExt string
	}{
		{
			name:   "keeps the master's strftime prefix",
			dir:    "/data/recordings",
			master: "/data/recordings/rec-%Y%m%d-%H%M%S.mkv",
			stem:   Stem{Name: "mic", Codec: ffmpeg.StemFLAC},
			want:   "/data/recordings/stems/rec-%Y%m%d-%H%M%S-mic.flac",
		},
		{
			name:   "wav stems carry the wav extension",
			dir:    "/data/recordings",
			master: "/data/recordings/rec-%Y%m%d-%H%M%S.mkv",
			stem:   Stem{Name: "music", Codec: ffmpeg.StemWAV},
			want:   "/data/recordings/stems/rec-%Y%m%d-%H%M%S-music.wav",
		},
		{
			name:   "an unset codec still produces a usable path",
			dir:    "/rec",
			master: "/rec/rec-%Y%m%d-%H%M%S.mkv",
			stem:   Stem{Name: "game"},
			want:   "/rec/stems/rec-%Y%m%d-%H%M%S-game.flac",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StemPattern(tt.dir, tt.master, tt.stem); got != filepath.FromSlash(tt.want) {
				t.Fatalf("StemPattern = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStemSpecsRoundTripsThePlan(t *testing.T) {
	s := src(stereo(0, 1),
		routing.TrackAnnotation{Track: 0, Role: routing.RoleMic},
		routing.TrackAnnotation{Track: 1, Role: routing.RoleMusic})
	specs := StemSpecs("/rec", "/rec/rec-%Y%m%d-%H%M%S.mkv", PlanStems(s, ffmpeg.StemFLAC))

	args := ffmpeg.StemRecorderArgs(ffmpeg.StemRecorderSpec{
		RecorderSpec: ffmpeg.RecorderSpec{RelayURL: "srt://x", OutputPattern: "/rec/rec-%Y%m%d-%H%M%S.mkv"},
		Codec:        ffmpeg.StemFLAC,
		Stems:        specs,
	})
	for _, want := range []string{
		filepath.FromSlash("/rec/stems/rec-%Y%m%d-%H%M%S-mic.flac"),
		filepath.FromSlash("/rec/stems/rec-%Y%m%d-%H%M%S-music.flac"),
	} {
		if !slices.Contains(args, want) {
			t.Fatalf("recorder args missing %q: %v", want, args)
		}
	}
}

func TestParseStemFilename(t *testing.T) {
	at := time.Date(2024, 1, 15, 14, 30, 0, 0, time.Local)
	tests := []struct {
		name     string
		file     string
		wantOK   bool
		wantStem string
		wantAt   time.Time
	}{
		{"flac stem", "rec-20240115-143000-mic.flac", true, "mic", at},
		{"wav stem", "rec-20240115-143000-music.wav", true, "music", at},
		{"a name containing a dash survives", "rec-20240115-143000-guest-mic-zoom.flac", true, "guest-mic-zoom", at},
		{"a disambiguating suffix survives", "rec-20240115-143000-mic-2.flac", true, "mic-2", at},
		{"the master itself is not a stem", "rec-20240115-143000.mkv", false, "", time.Time{}},
		{"an unrelated file is left alone", "notes.txt", false, "", time.Time{}},
		{"a stem extension with no timestamp is left alone", "mic.flac", false, "", time.Time{}},
		{"an impossible timestamp is left alone", "rec-20241315-143000-mic.flac", false, "", time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAt, gotStem, ok := ParseStemFilename(tt.file)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if gotStem != tt.wantStem {
				t.Fatalf("stem = %q, want %q", gotStem, tt.wantStem)
			}
			if !gotAt.Equal(tt.wantAt) {
				t.Fatalf("start = %v, want %v", gotAt, tt.wantAt)
			}
		})
	}
}

// writeStem lays down one stem file at a given age.
func writeStem(t *testing.T, dir string, at time.Time, name string, size int) string {
	t.Helper()
	if err := EnsureStemsDir(dir); err != nil {
		t.Fatalf("mkdir stems: %v", err)
	}
	file := "rec-" + at.Format("20060102-150405") + "-" + name + ".flac"
	writeFile(t, StemsDir(dir), file, size)
	return file
}

func stemsOnDisk(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(StemsDir(dir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read stems dir: %v", err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	slices.Sort(out)
	return out
}

func TestSweepStemsFollowsTheMasterIndex(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	tests := []struct {
		name string
		// masterAges are indexed master segments; stemAges are files on disk.
		masterAges []time.Duration
		stemAges   []time.Duration
		wantKept   []time.Duration
	}{
		{
			name:       "a stem whose master expired is deleted",
			masterAges: []time.Duration{time.Hour},
			stemAges:   []time.Duration{time.Hour, 10 * time.Hour},
			wantKept:   []time.Duration{time.Hour},
		},
		{
			name:       "every stem is kept while its master survives",
			masterAges: []time.Duration{time.Hour, 2 * time.Hour},
			stemAges:   []time.Duration{time.Hour, 2 * time.Hour},
			wantKept:   []time.Duration{time.Hour, 2 * time.Hour},
		},
		{
			// A stem cuts on the boundary and the master waits for a keyframe,
			// so the stem's own timestamp is legitimately a little earlier.
			name:       "a stem slightly older than its master is not an orphan",
			masterAges: []time.Duration{time.Hour},
			stemAges:   []time.Duration{time.Hour + 2*time.Second},
			wantKept:   []time.Duration{time.Hour + 2*time.Second},
		},
		{
			name:       "an empty index deletes nothing, because it cannot tell",
			masterAges: nil,
			stemAges:   []time.Duration{100 * time.Hour},
			wantKept:   []time.Duration{100 * time.Hour},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, dir, store := newManager(t)
			for _, age := range tt.masterAges {
				at := now.Add(-age)
				if err := store.UpsertRecording(&db.Recording{
					Filename: segmentName(at), StartedAt: at, Bytes: 1,
				}); err != nil {
					t.Fatalf("index master: %v", err)
				}
			}
			for _, age := range tt.stemAges {
				writeStem(t, dir, now.Add(-age), "mic", 1)
			}

			if _, err := m.SweepStems(); err != nil {
				t.Fatalf("SweepStems: %v", err)
			}

			want := make([]string, 0, len(tt.wantKept))
			for _, age := range tt.wantKept {
				want = append(want, "rec-"+now.Add(-age).Format("20060102-150405")+"-mic.flac")
			}
			slices.Sort(want)
			if got := stemsOnDisk(t, dir); !slices.Equal(got, want) {
				t.Fatalf("stems on disk = %v, want %v", got, want)
			}
		})
	}
}

// Deleting a file we cannot explain is not retention, it is data loss.
func TestSweepStemsLeavesForeignFilesAlone(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	m, dir, store := newManager(t)
	at := now.Add(-time.Hour)
	if err := store.UpsertRecording(&db.Recording{Filename: segmentName(at), StartedAt: at, Bytes: 1}); err != nil {
		t.Fatalf("index master: %v", err)
	}
	if err := EnsureStemsDir(dir); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, StemsDir(dir), "notes.txt", 1)
	writeFile(t, StemsDir(dir), "mixdown.flac", 1)
	writeStem(t, dir, now.Add(-100*time.Hour), "mic", 1)

	if _, err := m.SweepStems(); err != nil {
		t.Fatalf("SweepStems: %v", err)
	}
	want := []string{"mixdown.flac", "notes.txt"}
	if got := stemsOnDisk(t, dir); !slices.Equal(got, want) {
		t.Fatalf("stems on disk = %v, want %v", got, want)
	}
}

func TestSweepStemsWithNoStemsDirectory(t *testing.T) {
	m, _, _ := newManager(t)
	changed, err := m.SweepStems()
	if err != nil {
		t.Fatalf("SweepStems: %v", err)
	}
	if changed {
		t.Fatal("expected no change when the stems directory does not exist")
	}
}

func TestStemBytesCountsOnlyStems(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	m, dir, _ := newManager(t)
	writeStem(t, dir, now, "mic", 100)
	writeStem(t, dir, now, "music", 250)
	writeFile(t, StemsDir(dir), "notes.txt", 999)
	// A master in the recordings directory must not be counted twice.
	writeFile(t, dir, segmentName(now), 5000)

	got, err := m.StemBytes()
	if err != nil {
		t.Fatalf("StemBytes: %v", err)
	}
	if got != 350 {
		t.Fatalf("StemBytes = %d, want 350", got)
	}
}

func TestStemBytesWithNoStemsDirectory(t *testing.T) {
	m, _, _ := newManager(t)
	got, err := m.StemBytes()
	if err != nil {
		t.Fatalf("StemBytes: %v", err)
	}
	if got != 0 {
		t.Fatalf("StemBytes = %d, want 0", got)
	}
}
