package segname

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The bytes on disk are a contract with every file an install already holds,
// so they are pinned as literals rather than derived from the constants they
// are built from: a formatter and a parser changed together still fail here.
func TestPatternIsByteIdenticalToTheNameOnDisk(t *testing.T) {
	for _, tc := range []struct {
		id   int64
		want string
	}{
		{3, "rec-s3-%Y%m%d-%H%M%S.mkv"},
		{1234, "rec-s1234-%Y%m%d-%H%M%S.mkv"},
	} {
		if got := Pattern(tc.id); got != tc.want {
			t.Errorf("Pattern(%d) = %q, want %q", tc.id, got, tc.want)
		}
	}
	at := time.Date(2024, 1, 15, 14, 30, 5, 0, time.Local)
	if got := FormatStamp(at); got != "20240115-143005" {
		t.Errorf("FormatStamp = %q, want 20240115-143005", got)
	}
}

// ffmpeg expands the strftime half and Go code renders the layout half; both
// name the same files, so they must agree for every field.
func TestStrftimeAndGoLayoutRenderTheSameStamp(t *testing.T) {
	at := time.Date(2009, 11, 7, 3, 4, 9, 0, time.Local)
	strftime := strings.NewReplacer(
		"%Y", "2009", "%m", "11", "%d", "07", "%H", "03", "%M", "04", "%S", "09",
	).Replace(stampStrftime)
	if got := FormatStamp(at); got != strftime {
		t.Fatalf("FormatStamp = %q, ffmpeg would write %q", got, strftime)
	}
	if !isStamp(strftime) {
		t.Fatalf("isStamp(%q) = false for a stamp ffmpeg writes", strftime)
	}
}

func TestParseStamp(t *testing.T) {
	got, ok := ParseStamp("20240115-143005")
	if want := time.Date(2024, 1, 15, 14, 30, 5, 0, time.Local); !ok || !got.Equal(want) {
		t.Fatalf("ParseStamp = %v, %v; want %v", got, ok, want)
	}
	for _, bad := range []string{"", "20240115", "20241315-143005", "2024-01-15"} {
		if _, ok := ParseStamp(bad); ok {
			t.Errorf("ParseStamp(%q) accepted a malformed stamp", bad)
		}
	}
}

func TestSource(t *testing.T) {
	for _, tc := range []struct {
		name   string
		want   int64
		wantOK bool
	}{
		{"rec-s42-20240115-143000.mkv", 42, true},
		{"/data/recordings/rec-s7-20240115-143000.mkv", 7, true},
		{"rec-20240115-143000.mkv", 0, false},
		{"rec-s0-20240115-143000.mkv", 0, false},
		{"rec-sx-20240115-143000.mkv", 0, false},
		{"rec-s-20240115-143000.mkv", 0, false},
		{"rec-s42.mkv", 0, false},
		{"rec-s99999999999999999999-20240115-143000.mkv", 0, false},
		{"clip-s4-20240115-143000.mkv", 0, false},
	} {
		got, ok := Source(tc.name)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("Source(%q) = %d, %v; want %d, %v", tc.name, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestIsRecorderSegment(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"rec-s3-20240115-143000.mkv", true},
		{"rec-s1234-20240115-143000.mkv", true},
		{"rec-20240115-143000.mkv", true}, // an earlier release's name
		{"rec-s0-20240115-143000.mkv", false},
		{"rec-sx-20240115-143000.mkv", false},
		{"rec-s3.mkv", false},
		{"rec-s3-20240115-143000.mp4", false},
		{"rec-s3-20240115-143000-1.mkv", false},
		{"rec-s3-2024011-1430000.mkv", false},
		{"rec-s3-20240115_143000.mkv", false},
		{"rec-s3-2024a115-143000.mkv", false},
		{"rec-2024011-143000.mkv", false},
		{"booth-20240115-143000.mkv", false},
		{"dir/rec-s3-20240115-143000.mkv", false},
		{"rec.mkv", false},
	} {
		if got := IsRecorderSegment(tc.name); got != tc.want {
			t.Errorf("IsRecorderSegment(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
	at := time.Date(2024, 1, 15, 14, 30, 0, 0, time.Local)
	written := strings.Replace(Pattern(9), stampStrftime, FormatStamp(at), 1)
	if !IsRecorderSegment(written) {
		t.Errorf("IsRecorderSegment(%q) = false for the name Pattern produces", written)
	}
}

func TestTrailingStamp(t *testing.T) {
	want := time.Date(2024, 1, 15, 14, 30, 0, 0, time.Local)
	for _, name := range []string{
		"rec-s3-20240115-143000.mkv", "rec-20240115-143000.mkv", "booth-20240115-143000.mkv",
	} {
		if got, ok := TrailingStamp(name); !ok || !got.Equal(want) {
			t.Errorf("TrailingStamp(%q) = %v, %v; want %v", name, got, ok, want)
		}
	}
	for _, name := range []string{"show.mkv", "a-b.mkv", "rec-s3-20240115-143000-1.mkv"} {
		if _, ok := TrailingStamp(name); ok {
			t.Errorf("TrailingStamp(%q) read a start out of a name with none at the end", name)
		}
	}
}

func TestSplitMaster(t *testing.T) {
	want := time.Date(2024, 1, 15, 14, 30, 0, 0, time.Local)
	for _, tc := range []struct{ name, prefix string }{
		{"rec-s3-20240115-143000.mkv", "rec-s3"},
		{filepath.Join("dir", "rec-20240115-143000.mkv"), "rec"},
		{"rec-s3-20240115-143000", "rec-s3"},
	} {
		p, at, ok := SplitMaster(tc.name)
		if !ok || p != tc.prefix || !at.Equal(want) {
			t.Errorf("SplitMaster(%q) = %q, %v, %v; want %q, %v", tc.name, p, at, ok, tc.prefix, want)
		}
	}
	for _, bad := range []string{"show.mkv", "rec-s3-20241315-143000.mkv", "-20240115-143000.mkv"} {
		if _, _, ok := SplitMaster(bad); ok {
			t.Errorf("SplitMaster(%q) accepted a name no recorder wrote", bad)
		}
	}
}

func TestSplitStem(t *testing.T) {
	sn, ok := SplitStem("rec-s3-20240115-143000-mic-2")
	if !ok {
		t.Fatal("SplitStem rejected a stem this build writes")
	}
	if sn.Prefix != "rec-s3" || sn.Stamp != "20240115-143000" || sn.Track != "mic-2" {
		t.Fatalf("SplitStem = %+v", sn)
	}
	if want := time.Date(2024, 1, 15, 14, 30, 0, 0, time.Local); !sn.Start.Equal(want) {
		t.Fatalf("SplitStem start = %v, want %v", sn.Start, want)
	}
	if got := sn.MasterBase(); got != "rec-s3-20240115-143000" {
		t.Fatalf("MasterBase = %q", got)
	}
	for _, bad := range []string{"rec-s3-20240115-143000", "rec-s3-20240115-143000-Mic", "rec-s3-20241315-143000-mic"} {
		if _, ok := SplitStem(bad); ok {
			t.Errorf("SplitStem(%q) accepted a name no recorder wrote", bad)
		}
	}
}
