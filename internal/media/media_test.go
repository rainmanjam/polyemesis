package media

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ---------------------------------------------------------------- arg helpers

// argAfter returns the value following flag, and whether flag was present at
// all. Every argument assertion in this package goes through it rather than
// matching a joined string, so a test cannot pass because the token it wanted
// happened to appear inside a filter graph.
func argAfter(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func argIndex(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
}

// countArg is how the "-map appears once per output" assertions are written.
func countArg(args []string, want string) int {
	n := 0
	for _, a := range args {
		if a == want {
			n++
		}
	}
	return n
}

func mustArg(t *testing.T, args []string, flag, want string) {
	t.Helper()
	got, ok := argAfter(args, flag)
	if !ok {
		t.Fatalf("%s is missing from %v", flag, args)
	}
	if got != want {
		t.Fatalf("%s = %q, want %q", flag, got, want)
	}
}

// ---------------------------------------------------------------------- layout

func TestLayoutForPutsDerivedFilesInTheirOwnPerRecordingDirectory(t *testing.T) {
	l := LayoutFor("/rec", "rec-20240115-143000.mkv")

	if want := filepath.Join("/rec", Subdir, "rec-20240115-143000"); l.Dir != want {
		t.Fatalf("Dir = %q, want %q", l.Dir, want)
	}
	if got := filepath.Base(l.Proxy); got != ProxyName {
		t.Fatalf("proxy name = %q", got)
	}
	// The single most important property here: nothing lands beside the master,
	// where the recordings scanner would index a .mp4 proxy as a recording.
	for _, p := range []string{l.Proxy, l.Poster, l.ContactSheet, l.SpritePattern, l.SpriteVTT, l.Archive} {
		if filepath.Dir(p) != l.Dir {
			t.Fatalf("%q is not inside %q", p, l.Dir)
		}
	}
}

func TestLayoutForGivesTheArchiveTheMastersOwnExtension(t *testing.T) {
	tests := []struct {
		name      string
		recording string
		want      string
	}{
		{"matroska master", "rec-1.mkv", "archive.mkv"},
		{"transport stream master", "rec-1.ts", "archive.ts"},
		{"mp4 master", "rec-1.mp4", "archive.mp4"},
		{"no extension at all", "rec-1", "archive"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := filepath.Base(LayoutFor("/rec", tc.recording).Archive); got != tc.want {
				t.Fatalf("archive = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestValidRecordingNameRejectsAnythingThatCouldEscapeTheDirectory(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"a plain segment name", "rec-20240115-143000.mkv", true},
		{"empty", "", false},
		{"dot", ".", false},
		{"dot dot", "..", false},
		{"a slash", "../etc/passwd", false},
		{"a separator anywhere", "sub/rec.mkv", false},
		{"a newline", "rec\n.mkv", false},
		{"a null byte", "rec\x00.mkv", false},
		{"absurdly long", strings.Repeat("a", 256), false},
		// Backslash cases pin the invariant on the platform this suite runs on.
		//
		// This check is already correct -- it tests '/' AND os.PathSeparator --
		// but so was the copy in internal/recording until it drifted to the
		// separator alone, which meant nothing on Linux and let a forward slash
		// through on Windows. Every forward-slash case above would still pass
		// after that same narrowing; these would not.
		{"a windows separator", `sub\rec.mkv`, false},
		{"a windows climb out", `..\..\etc\passwd`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidRecordingName(tc.in); got != tc.want {
				t.Fatalf("ValidRecordingName(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestResolveRefusesToLeaveTheRecordingsDerivedDirectory(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name      string
		recording string
		file      string
		wantErr   bool
	}{
		{"a derived file", "rec-1.mkv", ProxyName, false},
		{"traversal in the file", "rec-1.mkv", "../../secret", true},
		{"traversal in the recording", "../../etc", ProxyName, true},
		{"empty file name", "rec-1.mkv", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(root, tc.recording, tc.file)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Resolve returned %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if !strings.HasPrefix(got, root) {
				t.Fatalf("Resolve returned %q, outside %q", got, root)
			}
		})
	}
}

// ----------------------------------------------------------------- retention

func TestRemoveDeletesEveryDerivedFileForOneRecording(t *testing.T) {
	root := t.TempDir()
	l := LayoutFor(root, "rec-1.mkv")
	if err := os.MkdirAll(l.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.Proxy, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Remove(root, "rec-1.mkv"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(l.Dir); !os.IsNotExist(err) {
		t.Fatalf("derived directory survived Remove: %v", err)
	}
	// Removing what is already gone is how a retention sweep behaves on its
	// second pass, and must not be an error.
	if err := Remove(root, "rec-1.mkv"); err != nil {
		t.Fatalf("second Remove: %v", err)
	}
}

func TestSweepDeletesDerivedMediaWhoseMasterIsGone(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"rec-1.mkv", "rec-2.mkv", "rec-3.mkv"} {
		if err := os.MkdirAll(LayoutFor(root, name).Dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := Sweep(root, map[string]bool{"rec-1.mkv": true, "rec-3.mkv": true})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(removed) != 1 || removed[0] != "rec-2" {
		t.Fatalf("removed = %v, want [rec-2]", removed)
	}
	if _, err := os.Stat(LayoutFor(root, "rec-1.mkv").Dir); err != nil {
		t.Fatalf("a surviving recording's derived media was swept: %v", err)
	}
}

// An empty index means the scan has not run, not that every recording is gone.
// Getting this backwards deletes every proxy in the library on the first boot
// after a database rebuild.
func TestSweepDeletesNothingWhenTheIndexIsEmpty(t *testing.T) {
	root := t.TempDir()
	dir := LayoutFor(root, "rec-1.mkv").Dir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	removed, err := Sweep(root, nil)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("removed = %v, want nothing", removed)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("derived media was swept with an empty index: %v", err)
	}
}

func TestSweepIsQuietWhenNothingHasEverBeenDerived(t *testing.T) {
	removed, err := Sweep(t.TempDir(), map[string]bool{"rec-1.mkv": true})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if removed != nil {
		t.Fatalf("removed = %v, want nil", removed)
	}
}

func TestBytesTotalsWhatDerivedMediaOccupies(t *testing.T) {
	root := t.TempDir()
	l := LayoutFor(root, "rec-1.mkv")
	if err := os.MkdirAll(l.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.Proxy, make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.Poster, make([]byte, 512), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Bytes(root)
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	if got != 1536 {
		t.Fatalf("Bytes = %d, want 1536", got)
	}
}

func TestBytesIsZeroRatherThanAnErrorBeforeAnythingIsDerived(t *testing.T) {
	got, err := Bytes(filepath.Join(t.TempDir(), "nothing-here"))
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	if got != 0 {
		t.Fatalf("Bytes = %d, want 0", got)
	}
}

// ------------------------------------------------------------- filter helpers

func TestFilterColorFallsBackToBlackForAnythingThatCouldRecutTheFilterGraph(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"a named colour", "white", "white"},
		{"a hex colour", "#101014", "#101014"},
		{"empty", "", "black"},
		{"a comma, which would start a new filter", "black,scale=1:1", "black"},
		{"a colon, which would start a new option", "black:0.5", "black"},
		{"an alpha suffix", "black@0.5", "black"},
		{"absurdly long", strings.Repeat("a", 40), "black"},
		{"a hash that is not leading", "bl#ack", "black"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := filterColor(tc.in); got != tc.want {
				t.Fatalf("filterColor(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestScaleFilterDerivesTheMissingSideToAnEvenNumber(t *testing.T) {
	tests := []struct {
		name string
		w, h int
		want string
	}{
		{"both sides", 640, 360, "scale=640:360"},
		{"width only", 640, 0, "scale=640:-2"},
		{"height only", 0, 360, "scale=-2:360"},
		{"neither", 0, 0, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := scaleFilter(tc.w, tc.h); got != tc.want {
				t.Fatalf("scaleFilter(%d,%d) = %q, want %q", tc.w, tc.h, got, tc.want)
			}
		})
	}
}

func TestFitTileFilterProducesAnExactTileWhateverTheSourceShape(t *testing.T) {
	got := fitTileFilter(160, 90, "black")
	for _, want := range []string{
		"scale=160:90:force_original_aspect_ratio=decrease:force_divisible_by=2",
		"pad=160:90:",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("fitTileFilter = %q, missing %q", got, want)
		}
	}
	// The pad offsets must stay on the chroma grid, or a composited tile lands
	// half a pixel off and the whole sheet softens.
	if !strings.Contains(got, "2*floor((ow-iw)/2/2)") {
		t.Fatalf("fitTileFilter = %q, pad offset is not rounded to an even pixel", got)
	}
}

func TestFormatSecondsNeverProducesAnExponent(t *testing.T) {
	tests := []struct {
		name string
		in   float64
		want string
	}{
		{"whole", 5, "5"},
		{"fractional", 2.5, "2.5"},
		{"very small, which would otherwise print as 1e-06", 0.000001, "0.000001"},
		{"long", 3600.125, "3600.125"},
		{"zero", 0, "0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatSeconds(tc.in); got != tc.want {
				t.Fatalf("formatSeconds(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
