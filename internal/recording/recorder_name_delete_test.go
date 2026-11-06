package recording

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// recorderSegmentName is the file a recorder for sourceID opens at at: the
// pattern SegmentPattern hands ffmpeg, expanded the way ffmpeg's strftime
// expands it. It is built from the producer rather than spelled out so a
// change to the name the recorder writes reaches these tests too.
func recorderSegmentName(sourceID int64, at time.Time) string {
	return strings.NewReplacer(
		"%Y", at.Format("2006"), "%m", at.Format("01"), "%d", at.Format("02"),
		"%H", at.Format("15"), "%M", at.Format("04"), "%S", at.Format("05"),
	).Replace(filepath.Base(SegmentPattern("", sourceID)))
}

// THE EXEMPTION MUST RECOGNISE THE NAME THE RECORDER WRITES. With the recorder
// stopped, Delete lifts the live-segment guard on the recorder's own last
// segment. That check matched only rec-YYYYMMDD-HHMMSS.mkv, the name before
// segments carried their programme, so for every segment this build wrote
// (rec-s<id>-YYYYMMDD-HHMMSS.mkv) the exemption never fired and the operator
// was told to wait for a recorder that no longer existed. The older name is
// still on disk from earlier releases and stays deletable too.
//
// Mutation: match only the legacy name in IsRecorderSegment. Observed to fail
// the "source-tagged" rows with ErrSegmentLive.
func TestDeleteLiftsTheLiveGuardOnTheNameTheRecorderWrites(t *testing.T) {
	started := time.Now().Add(-5 * time.Minute)
	for _, tc := range []struct {
		name       string
		file       string
		running    bool
		destURL    string
		wantRefuse bool
	}{
		{"source-tagged segment this build writes", recorderSegmentName(3, started), false, "", false},
		{"source-tagged segment of a multi-digit programme", recorderSegmentName(1234, started), false, "", false},
		{"legacy segment an earlier release wrote", segmentName(started), false, "", false},
		{"source-tagged segment while a recorder is running", recorderSegmentName(3, started), true, "", true},
		// ResolveForWrite rolls rec-s3.mkv over to rec-s3-YYYYMMDD-HHMMSS.mkv,
		// exactly the recorder's name, so the destination check must win.
		{"a file destination's rollover that spells the recorder's name", recorderSegmentName(3, started), false, "rec-s3.mkv", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, dir, store := newManager(t)
			WithRecorderProbe(func() bool { return tc.running })(m)
			setRecording(t, store, true)
			if tc.destURL != "" {
				if _, err := store.CreateDestination(&db.Destination{
					Name: "booth", Kind: db.DestFile, URL: tc.destURL,
				}); err != nil {
					t.Fatalf("create file destination: %v", err)
				}
			}

			writeFile(t, dir, tc.file, 16)
			if err := store.UpsertRecording(&db.Recording{Filename: tc.file, StartedAt: started}); err != nil {
				t.Fatalf("index %s: %v", tc.file, err)
			}
			recs, err := store.ListRecordings()
			if err != nil || len(recs) != 1 {
				t.Fatalf("ListRecordings = %d rows (err %v), want 1", len(recs), err)
			}

			err = m.Delete(recs[0].ID)
			if tc.wantRefuse {
				if !errors.Is(err, ErrSegmentLive) {
					t.Fatalf("Delete(%s) = %v, want ErrSegmentLive: a file a live process "+
						"may be writing was deleted", tc.file, err)
				}
				return
			}
			if err != nil {
				if errors.Is(err, ErrSegmentLive) {
					t.Fatalf("Delete(%s) = %v with no recorder running: the name the "+
						"recorder writes is not recognised as the recorder's own", tc.file, err)
				}
				t.Fatalf("Delete(%s) = %v", tc.file, err)
			}
			if _, statErr := os.Stat(filepath.Join(dir, tc.file)); !os.IsNotExist(statErr) {
				t.Errorf("Delete(%s) succeeded and left the file on disk (%v)", tc.file, statErr)
			}
		})
	}
}

// A file destination whose path is taken rolls over to <stem>-YYYYMMDD-HHMMSS,
// the recorder's stamp, which is what lets Scan read its start from the name.
func TestResolveForWriteRollsOverToTheRecorderStamp(t *testing.T) {
	m, dir, _ := newManagerIn(t, t.TempDir())
	writeFile(t, dir, "show.mkv", 1)
	before := time.Now().Truncate(time.Second)
	got, err := m.ResolveForWrite("show.mkv")
	if err != nil {
		t.Fatalf("ResolveForWrite: %v", err)
	}
	base := filepath.Base(got)
	stamp, ok := strings.CutPrefix(strings.TrimSuffix(base, ".mkv"), "show-")
	if !ok || len(stamp) != len("20060102-150405") || stamp[8] != '-' {
		t.Fatalf("rollover name = %q, want show-YYYYMMDD-HHMMSS.mkv", base)
	}
	at := startTimeFromName(base, time.Time{})
	if at.Before(before) || at.After(time.Now()) {
		t.Fatalf("rollover %q reads back as %v, not the moment it was taken", base, at)
	}
}
