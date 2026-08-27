package recording

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/db/dbtest"
)

func newManager(t *testing.T) (*Manager, string, *db.DB) {
	t.Helper()
	return newManagerIn(t, t.TempDir())
}

func newManagerIn(t *testing.T, dir string) (*Manager, string, *db.DB) {
	t.Helper()
	store := dbtest.Open(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(log, store, dir, nil), dir, store
}

// segmentName mirrors the strftime pattern the recorder writes, so the tests
// exercise the same start-time recovery path production uses.
func segmentName(at time.Time) string {
	return "rec-" + at.Format("20060102-150405") + ".mkv"
}

func writeFile(t *testing.T, dir, name string, size int) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// writeSegments lays down one file per age, newest-relative to now, and returns
// the names in the same order so a test can name expectations by age.
func writeSegments(t *testing.T, dir string, now time.Time, ages []time.Duration, sizes []int) []string {
	t.Helper()
	names := make([]string, len(ages))
	for i, age := range ages {
		names[i] = segmentName(now.Add(-age))
		size := 1
		if sizes != nil {
			size = sizes[i]
		}
		writeFile(t, dir, names[i], size)
	}
	return names
}

func namesFor(now time.Time, ages []time.Duration) []string {
	out := make([]string, len(ages))
	for i, age := range ages {
		out[i] = segmentName(now.Add(-age))
	}
	slices.Sort(out)
	return out
}

func indexedFilenames(t *testing.T, store *db.DB) []string {
	t.Helper()
	recs, err := store.ListRecordings()
	if err != nil {
		t.Fatalf("list recordings: %v", err)
	}
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.Filename)
	}
	slices.Sort(out)
	return out
}

func filesOnDisk(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	out := []string{}
	for _, e := range entries {
		if !e.IsDir() {
			out = append(out, e.Name())
		}
	}
	slices.Sort(out)
	return out
}

// maxGBFor expresses a byte budget as the GB figure the settings carry, so the
// size-cap tests can work with a handful of bytes instead of real gigabytes.
func maxGBFor(bytes int64) float64 { return float64(bytes) / (1024 * 1024 * 1024) }

// ------------------------------------------------------------------- scan

func TestScanIndexesRecordingFilesOnly(t *testing.T) {
	tests := []struct {
		name  string
		file  string
		isDir bool
		want  bool
	}{
		{name: "mkv segments are indexed", file: "rec-20240115-143000.mkv", want: true},
		{name: "mp4 segments are indexed", file: "rec-20240115-153000.mp4", want: true},
		{name: "ts segments are indexed", file: "rec-20240115-163000.ts", want: true},
		{name: "the extension test is case-insensitive", file: "rec-20240115-173000.MKV", want: true},
		{name: "unrelated files are left alone", file: "notes.txt", want: false},
		{name: "extensionless files are left alone", file: "README", want: false},
		{name: "half-written temp files are left alone", file: "rec-20240115-183000.mkv.tmp", want: false},
		{name: "directories are never indexed", file: "rec-20240115-193000.mkv", isDir: true, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, dir, store := newManager(t)
			if tc.isDir {
				if err := os.Mkdir(filepath.Join(dir, tc.file), 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
			} else {
				writeFile(t, dir, tc.file, 1)
			}

			if _, err := m.Scan(); err != nil {
				t.Fatalf("scan: %v", err)
			}

			got := indexedFilenames(t, store)
			if slices.Contains(got, tc.file) != tc.want {
				t.Errorf("indexed %v, want %q present = %v", got, tc.file, tc.want)
			}
		})
	}
}

func TestScanRecordsSizeAndStartTime(t *testing.T) {
	mtime := time.Date(2031, 6, 2, 9, 15, 30, 0, time.Local)

	tests := []struct {
		name      string
		file      string
		size      int
		wantStart time.Time
	}{
		{
			name: "the start time comes from the filename, not the mtime that moves while writing",
			file: "rec-20240115-143000.mkv",
			size: 1234,
			// The recorder is still appending, so mtime is "now", not the start.
			wantStart: time.Date(2024, 1, 15, 14, 30, 0, 0, time.Local),
		},
		{
			name:      "a name without a timestamp falls back to the mtime",
			file:      "capture.mkv",
			size:      7,
			wantStart: mtime,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, dir, store := newManager(t)
			path := writeFile(t, dir, tc.file, tc.size)
			if err := os.Chtimes(path, mtime, mtime); err != nil {
				t.Fatalf("chtimes: %v", err)
			}

			if _, err := m.Scan(); err != nil {
				t.Fatalf("scan: %v", err)
			}

			recs, err := store.ListRecordings()
			if err != nil {
				t.Fatalf("list recordings: %v", err)
			}
			if len(recs) != 1 {
				t.Fatalf("indexed %d recordings, want 1", len(recs))
			}
			if recs[0].Bytes != int64(tc.size) {
				t.Errorf("bytes = %d, want %d", recs[0].Bytes, tc.size)
			}
			if !recs[0].StartedAt.Equal(tc.wantStart) {
				t.Errorf("startedAt = %s, want %s", recs[0].StartedAt, tc.wantStart)
			}
		})
	}
}

func TestScanDropsIndexRowsWhoseFileVanished(t *testing.T) {
	m, dir, store := newManager(t)
	writeFile(t, dir, "rec-20240115-143000.mkv", 10)
	gone := writeFile(t, dir, "rec-20240115-153000.mkv", 10)

	if _, err := m.Scan(); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	if got := len(indexedFilenames(t, store)); got != 2 {
		t.Fatalf("indexed %d recordings after first scan, want 2", got)
	}

	if err := os.Remove(gone); err != nil {
		t.Fatalf("remove: %v", err)
	}
	changed, err := m.Scan()
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if !changed {
		t.Error("scan reported no change after a file disappeared")
	}

	want := []string{"rec-20240115-143000.mkv"}
	if got := indexedFilenames(t, store); !slices.Equal(got, want) {
		t.Errorf("indexed %v, want %v", got, want)
	}
}

func TestScanTreatsAMissingRecordingsDirAsEmpty(t *testing.T) {
	m, _, store := newManagerIn(t, filepath.Join(t.TempDir(), "never-created"))

	changed, err := m.Scan()
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if changed {
		t.Error("scan reported a change for a directory that does not exist")
	}
	if got := indexedFilenames(t, store); len(got) != 0 {
		t.Errorf("indexed %v, want nothing", got)
	}
}

// ------------------------------------------------------------------ sweep

func TestSweepDeletesSegmentsOlderThanMaxAge(t *testing.T) {
	tests := []struct {
		name        string
		maxAgeHours int
		ages        []time.Duration
		wantKept    []time.Duration
	}{
		{
			name:        "segments past the cutoff go, newer ones stay",
			maxAgeHours: 24,
			ages:        []time.Duration{1 * time.Hour, 26 * time.Hour, 50 * time.Hour},
			wantKept:    []time.Duration{1 * time.Hour},
		},
		{
			name:        "nothing goes while every segment is inside the window",
			maxAgeHours: 72,
			ages:        []time.Duration{1 * time.Hour, 26 * time.Hour, 50 * time.Hour},
			wantKept:    []time.Duration{1 * time.Hour, 26 * time.Hour, 50 * time.Hour},
		},
		{
			// #504: a max-age shorter than a segment's own length is a
			// plausible operator mistake, and it means the live segment can
			// be past the cutoff too. It must survive its own age -- the
			// most recent segment here stands in for "the one still being
			// written" -- while the older, non-live segment is still culled.
			name:        "the live segment outlasts the cutoff even when every segment is expired",
			maxAgeHours: 24,
			ages:        []time.Duration{26 * time.Hour, 50 * time.Hour},
			wantKept:    []time.Duration{26 * time.Hour},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, dir, store := newManager(t)
			now := time.Now()
			writeSegments(t, dir, now, tc.ages, nil)
			if _, err := m.Scan(); err != nil {
				t.Fatalf("scan: %v", err)
			}

			deleted, err := m.Sweep(db.RecordingSettings{MaxAgeHours: tc.maxAgeHours})
			if err != nil {
				t.Fatalf("sweep: %v", err)
			}
			if want := len(tc.wantKept) != len(tc.ages); deleted != want {
				t.Errorf("sweep reported deleted = %v, want %v", deleted, want)
			}

			want := namesFor(now, tc.wantKept)
			if got := filesOnDisk(t, dir); !slices.Equal(got, want) {
				t.Errorf("files on disk %v, want %v", got, want)
			}
			if got := indexedFilenames(t, store); !slices.Equal(got, want) {
				t.Errorf("indexed %v, want %v", got, want)
			}
		})
	}
}

// #504: a max-age setting shorter than a segment's own length is a plausible
// operator mistake, and the sweep runs every 30 seconds -- long enough to
// find the open file and unlink footage that cannot be recreated. The live
// segment (the most recent by start time, standing in for "the one still
// being written") must survive its own age no matter how the cutoff compares
// to it; an older, non-live segment past the cutoff must still go, or
// retention stops working entirely.
func TestSweepNeverDeletesTheLiveSegmentByAge(t *testing.T) {
	tests := []struct {
		name        string
		maxAgeHours int
		ages        []time.Duration
		wantKept    []time.Duration
	}{
		{
			name:        "a lone segment survives even though it is already past the cutoff",
			maxAgeHours: 1,
			ages:        []time.Duration{2 * time.Hour},
			wantKept:    []time.Duration{2 * time.Hour},
		},
		{
			name:        "the live segment survives past the cutoff while an older, non-live one is still deleted",
			maxAgeHours: 1,
			ages:        []time.Duration{2 * time.Hour, 5 * time.Hour},
			wantKept:    []time.Duration{2 * time.Hour},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, dir, store := newManager(t)
			now := time.Now()
			writeSegments(t, dir, now, tc.ages, nil)
			if _, err := m.Scan(); err != nil {
				t.Fatalf("scan: %v", err)
			}

			if _, err := m.Sweep(db.RecordingSettings{MaxAgeHours: tc.maxAgeHours}); err != nil {
				t.Fatalf("sweep: %v", err)
			}

			want := namesFor(now, tc.wantKept)
			if got := filesOnDisk(t, dir); !slices.Equal(got, want) {
				t.Errorf("files on disk %v, want %v", got, want)
			}
			if got := indexedFilenames(t, store); !slices.Equal(got, want) {
				t.Errorf("indexed %v, want %v", got, want)
			}
		})
	}
}

// TestSweepProtectsEverySegmentARecorderCouldStillBeWritingNotJustTheNewest is
// the count #504 got wrong.
//
// config.RecordingsDir() is install-wide and every programme runs its own
// recorder into it, so an install with two programmes was measured holding
// EIGHT .mkv files open at once. The protection named one -- the latest start
// time -- and the size branch was then free to unlink the other seven while
// ffmpeg was writing them, which costs the tail of a live archive and leaves
// the bytes charged to a volume that Usage() no longer counts them against.
//
// Each case carries its own control: a closed segment that must still be swept.
// A guard that protected everything would pass the first half of both cases and
// fail the second, and a retention policy that deletes nothing is how the disk
// fills -- the failure Sweep exists to prevent.
func TestSweepProtectsEverySegmentARecorderCouldStillBeWritingNotJustTheNewest(t *testing.T) {
	tests := []struct {
		name     string
		settings db.RecordingSettings
		ages     []time.Duration
		sizes    []int
		wantKept []time.Duration
	}{
		{
			// Three recorders rolling over every minute, their open segments
			// scattered across the last hundred seconds, and two segments from
			// hours ago that nothing holds.
			name:     "the size cap spares every open segment and still takes the closed ones",
			settings: db.RecordingSettings{SegmentSeconds: 60, MaxGB: maxGBFor(1)},
			ages:     []time.Duration{0, 30 * time.Second, 100 * time.Second, 4 * time.Hour, 9 * time.Hour},
			sizes:    []int{500, 500, 500, 500, 500},
			wantKept: []time.Duration{0, 30 * time.Second, 100 * time.Second},
		},
		{
			// A max-age shorter than a segment's own length, which is the
			// operator mistake #504 was written for -- except that here it is
			// two open segments past the cutoff, not one.
			name:     "the age cutoff spares every open segment and still takes the closed ones",
			settings: db.RecordingSettings{SegmentSeconds: 7200, MaxAgeHours: 1},
			ages:     []time.Duration{0, 90 * time.Minute, 2 * time.Hour, 9 * time.Hour},
			wantKept: []time.Duration{0, 90 * time.Minute, 2 * time.Hour},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, dir, store := newManager(t)
			now := time.Now()
			writeSegments(t, dir, now, tc.ages, tc.sizes)
			if _, err := m.Scan(); err != nil {
				t.Fatalf("scan: %v", err)
			}

			if _, err := m.Sweep(tc.settings); err != nil {
				t.Fatalf("sweep: %v", err)
			}

			want := namesFor(now, tc.wantKept)
			if got := filesOnDisk(t, dir); !slices.Equal(got, want) {
				t.Errorf("files on disk %v, want %v: the sweep either unlinked a segment a recorder "+
					"is still writing into, or stopped deleting segments nothing holds", got, want)
			}
			if got := indexedFilenames(t, store); !slices.Equal(got, want) {
				t.Errorf("indexed %v, want %v", got, want)
			}
		})
	}
}

// TestAnUnsetSegmentLengthLeavesTheLiveWindowAWholeSegmentWide covers the one
// input to the live-segment guard that no deletion test can reach: a caller
// that hands Sweep a db.RecordingSettings it built itself, with the segment
// length left at zero.
//
// Nothing in production does that today, and the whole guard collapses to the
// two-minute slack if one ever starts -- which is a sweep that unlinks open
// segments again, silently, on an install nobody changed. Zero means "this did
// not come through the store" (Validate refuses anything outside 10s-24h), and
// the safe reading of that is the length the install ships with.
//
// The second assertion is the control: a guard that answered the default to
// every question would satisfy the first and make the setting decorative.
func TestAnUnsetSegmentLengthLeavesTheLiveWindowAWholeSegmentWide(t *testing.T) {
	shipped := time.Duration(db.DefaultSettings().Recording.SegmentSeconds) * time.Second
	if got := liveWindow(0); got < shipped {
		t.Errorf("an unset segment length gives a live window of %s, shorter than the %s segment "+
			"this install ships with: a sweep would delete files a recorder is still writing", got, shipped)
	}
	if got, want := liveWindow(60), 60*time.Second+liveSegmentSlack; got != want {
		t.Errorf("a configured 60s segment gives a live window of %s, want %s: the operator's "+
			"segment length is not reaching the guard", got, want)
	}
}

func TestSweepDeletesOldestFirstUntilUnderMaxGB(t *testing.T) {
	ages := []time.Duration{2 * time.Hour, 4 * time.Hour, 6 * time.Hour}
	sizes := []int{100, 100, 100}

	tests := []struct {
		name     string
		capBytes int64
		wantKept []time.Duration
	}{
		{
			name:     "the oldest segment is sacrificed first",
			capBytes: 250,
			wantKept: []time.Duration{2 * time.Hour, 4 * time.Hour},
		},
		{
			name:     "deletion stops as soon as the total fits",
			capBytes: 150,
			wantKept: []time.Duration{2 * time.Hour},
		},
		{
			name:     "nothing goes while the total is under the cap",
			capBytes: 1000,
			wantKept: ages,
		},
		{
			name:     "a total exactly on the cap is not over it",
			capBytes: 300,
			wantKept: ages,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, dir, store := newManager(t)
			now := time.Now()
			writeSegments(t, dir, now, ages, sizes)
			if _, err := m.Scan(); err != nil {
				t.Fatalf("scan: %v", err)
			}

			deleted, err := m.Sweep(db.RecordingSettings{MaxGB: maxGBFor(tc.capBytes)})
			if err != nil {
				t.Fatalf("sweep: %v", err)
			}
			if want := len(tc.wantKept) != len(ages); deleted != want {
				t.Errorf("sweep reported deleted = %v, want %v", deleted, want)
			}

			want := namesFor(now, tc.wantKept)
			if got := filesOnDisk(t, dir); !slices.Equal(got, want) {
				t.Errorf("files on disk %v, want %v", got, want)
			}
			if got := indexedFilenames(t, store); !slices.Equal(got, want) {
				t.Errorf("indexed %v, want %v", got, want)
			}
		})
	}
}

// The newest segment is the one the recorder holds open. Deleting it would
// fight the recorder rather than free space, so the size cap must yield.
func TestSweepNeverDeletesTheLastRemainingSegment(t *testing.T) {
	tests := []struct {
		name     string
		ages     []time.Duration
		sizes    []int
		capBytes int64
	}{
		{
			name:     "a lone segment survives a cap it blows on its own",
			ages:     []time.Duration{1 * time.Hour},
			sizes:    []int{500},
			capBytes: 1,
		},
		{
			name:     "the sweep stops at one segment even while still over the cap",
			ages:     []time.Duration{2 * time.Hour, 4 * time.Hour, 6 * time.Hour},
			sizes:    []int{500, 500, 500},
			capBytes: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, dir, store := newManager(t)
			now := time.Now()
			writeSegments(t, dir, now, tc.ages, tc.sizes)
			if _, err := m.Scan(); err != nil {
				t.Fatalf("scan: %v", err)
			}

			if _, err := m.Sweep(db.RecordingSettings{MaxGB: maxGBFor(tc.capBytes)}); err != nil {
				t.Fatalf("sweep: %v", err)
			}

			want := namesFor(now, tc.ages[:1])
			if got := filesOnDisk(t, dir); !slices.Equal(got, want) {
				t.Errorf("files on disk %v, want %v", got, want)
			}
			if got := indexedFilenames(t, store); !slices.Equal(got, want) {
				t.Errorf("indexed %v, want %v", got, want)
			}
		})
	}
}

func TestSweepTreatsZeroLimitsAsNoLimit(t *testing.T) {
	ages := []time.Duration{2 * time.Hour, 100 * time.Hour}
	sizes := []int{1000, 1000}

	tests := []struct {
		name     string
		settings db.RecordingSettings
	}{
		{
			name:     "both limits off deletes nothing at all",
			settings: db.RecordingSettings{MaxGB: 0, MaxAgeHours: 0},
		},
		{
			name:     "MaxGB of zero means no size cap",
			settings: db.RecordingSettings{MaxGB: 0, MaxAgeHours: 10000},
		},
		{
			name:     "MaxAgeHours of zero means segments never expire",
			settings: db.RecordingSettings{MaxGB: maxGBFor(1 << 20), MaxAgeHours: 0},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, dir, store := newManager(t)
			now := time.Now()
			writeSegments(t, dir, now, ages, sizes)
			if _, err := m.Scan(); err != nil {
				t.Fatalf("scan: %v", err)
			}

			deleted, err := m.Sweep(tc.settings)
			if err != nil {
				t.Fatalf("sweep: %v", err)
			}
			if deleted {
				t.Error("sweep deleted something with the limit switched off")
			}

			want := namesFor(now, ages)
			if got := filesOnDisk(t, dir); !slices.Equal(got, want) {
				t.Errorf("files on disk %v, want %v", got, want)
			}
			if got := indexedFilenames(t, store); !slices.Equal(got, want) {
				t.Errorf("indexed %v, want %v", got, want)
			}
		})
	}
}

func TestSweepOnAnEmptyIndexDeletesNothing(t *testing.T) {
	m, _, _ := newManager(t)

	deleted, err := m.Sweep(db.RecordingSettings{MaxGB: maxGBFor(1), MaxAgeHours: 1})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if deleted {
		t.Error("sweep reported a deletion with nothing indexed")
	}
}

func TestSweepDeIndexesAnExpiredSegmentAlreadyGoneFromDisk(t *testing.T) {
	m, dir, store := newManager(t)
	now := time.Now()
	names := writeSegments(t, dir, now, []time.Duration{1 * time.Hour, 50 * time.Hour}, nil)
	if _, err := m.Scan(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, names[1])); err != nil {
		t.Fatalf("remove: %v", err)
	}

	deleted, err := m.Sweep(db.RecordingSettings{MaxAgeHours: 24})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if !deleted {
		t.Error("sweep reported no deletion for an expired segment")
	}
	want := []string{names[0]}
	if got := indexedFilenames(t, store); !slices.Equal(got, want) {
		t.Errorf("indexed %v, want %v", got, want)
	}
}

// A filename reaches the sweeper from a database row, so a poisoned row must
// not turn retention into an arbitrary-file delete.
func TestSweepRefusesToDeleteThroughAnEscapingFilename(t *testing.T) {
	m, dir, store := newManager(t)
	outside := writeFile(t, filepath.Dir(dir), "outside.mkv", 10)
	if err := store.UpsertRecording(&db.Recording{
		Filename:  "../outside.mkv",
		StartedAt: time.Now().Add(-100 * time.Hour),
		Bytes:     10,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	deleted, err := m.Sweep(db.RecordingSettings{MaxAgeHours: 1})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if deleted {
		t.Error("sweep reported deleting a recording it must have refused")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("file outside the recordings dir was deleted: %v", err)
	}
}

func TestScanAndSweepNotifiesOnChangeAfterRetentionDeletes(t *testing.T) {
	dir := t.TempDir()
	store := dbtest.Open(t)
	notified := 0
	m := New(slog.New(slog.NewTextHandler(io.Discard, nil)), store, dir, func() { notified++ })

	now := time.Now()
	writeSegments(t, dir, now, []time.Duration{1 * time.Hour, 50 * time.Hour}, nil)
	m.ScanAndSweep(db.RecordingSettings{MaxAgeHours: 24})

	if notified == 0 {
		t.Error("onChange was never called after retention deleted a segment")
	}
	want := namesFor(now, []time.Duration{1 * time.Hour})
	if got := filesOnDisk(t, dir); !slices.Equal(got, want) {
		t.Errorf("files on disk %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------- resolve

func TestResolveRejectsNamesThatEscapeTheRecordingsDir(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{name: "empty name", in: ""},
		{name: "the directory itself", in: "."},
		{name: "the parent directory", in: ".."},
		{name: "a relative climb out", in: "../x"},
		{name: "a deeper relative climb out", in: "../../etc/passwd"},
		{name: "an absolute path", in: "/etc/passwd"},
		{name: "a nested path", in: "a/b"},
		{name: "a leading slash on an otherwise valid name", in: "/rec-20240115-143000.mkv"},
		{name: "a climb that rejoins the dir", in: "sub/../rec-20240115-143000.mkv"},
		{name: "a trailing separator", in: "rec-20240115-143000.mkv/"},
		// The BACKSLASH cases are what make this invariant testable here.
		//
		// Every case above uses a forward slash, so on Linux they were all
		// caught by os.PathSeparator and the suite was green -- while on
		// Windows, where that constant is '\', the same names sailed through
		// and Join turned them into paths into subdirectories. A Linux run
		// could not see it.
		//
		// A backslash is a legal character in a POSIX filename, so these
		// exercise the same "a name is a bare filename, not a path" rule on
		// the platform CI mostly runs on, and they fail on Linux if the check
		// ever narrows back to the local separator.
		{name: "a windows nested path", in: `a\b`},
		{name: "a windows climb out", in: `..\..\etc\passwd`},
		{name: "a windows trailing separator", in: `rec-20240115-143000.mkv\`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, _, _ := newManager(t)
			got, err := m.Resolve(tc.in)
			if err == nil {
				t.Fatalf("Resolve(%q) = %q, want an error", tc.in, got)
			}
		})
	}
}

func TestResolveReturnsAPathInsideTheRecordingsDir(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{name: "a recorder segment name", in: "rec-20240115-143000.mkv"},
		{name: "a name that merely starts with dots", in: "...mkv"},
		{name: "a name containing a dot-dot run", in: "rec..old.mkv"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, dir, _ := newManager(t)
			got, err := m.Resolve(tc.in)
			if err != nil {
				t.Fatalf("Resolve(%q): %v", tc.in, err)
			}
			base, err := filepath.Abs(dir)
			if err != nil {
				t.Fatalf("abs: %v", err)
			}
			if want := filepath.Join(base, tc.in); got != want {
				t.Errorf("Resolve(%q) = %q, want %q", tc.in, got, want)
			}
			if !filepath.IsAbs(got) {
				t.Errorf("Resolve(%q) = %q, want an absolute path", tc.in, got)
			}
		})
	}
}

func TestDeleteRefusesAnEscapingFilename(t *testing.T) {
	m, dir, store := newManager(t)
	outside := writeFile(t, filepath.Dir(dir), "outside.mkv", 10)
	if err := store.UpsertRecording(&db.Recording{Filename: "../outside.mkv", StartedAt: time.Now()}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	recs, err := store.ListRecordings()
	if err != nil {
		t.Fatalf("list recordings: %v", err)
	}

	if err := m.Delete(recs[0].ID); err == nil {
		t.Error("Delete accepted a filename that escapes the recordings dir")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("file outside the recordings dir was deleted: %v", err)
	}
}
