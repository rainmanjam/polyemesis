package ffmpeg

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// hostileNames are directory names, and overlay text, carrying every character
// that means something to a filtergraph at either level, plus the ones that do
// not but are the next thing somebody would suspect: '%' (drawtext's expansion
// character), a space, a double quote and non-ASCII. The last one is all of
// them at once, so an escaper that is right for each alone but wrong in
// combination still fails.
var hostileNames = []string{
	"plain",
	"a'b",
	"a,b",
	"a[b",
	"a]b",
	"a;b",
	"a:b",
	`a\b`,
	"a%b",
	"a b",
	`a"b`,
	"ünï日本",
	`all ' , [ ] ; : \ % " ü`,
}

// hostileNamesForThisOS drops the names this platform cannot create: Windows
// refuses \ : and " in a path component.
func hostileNamesForThisOS() []string {
	if runtime.GOOS != "windows" {
		return hostileNames
	}
	var out []string
	for _, n := range hostileNames {
		if !strings.ContainsAny(n, `\:"`) {
			out = append(out, n)
		}
	}
	return out
}

// Each metacharacter, escaped for exactly the levels it belongs to.
//
// The table is the documented contract, not a restatement of the replacer:
// FFmpeg's "Notes on filtergraph escaping" put \ ' : at the option level and
// \ ' [ ] , ; at the graph level, applied innermost first. The real-parser
// proof is TestFilterPathOpensADirectoryNamedWithEveryCharacter and
// TestFilterArgumentsSurviveTheRealParser; this one names the character when
// the rule breaks.
func TestEscapeLavfiArgEscapesEachCharacterForItsOwnLevels(t *testing.T) {
	cases := []struct {
		in, want, why string
	}{
		{`\`, `\\\\`, "both levels: escaped for the option pass, then both of those for the graph pass"},
		{`'`, `\\\'`, "both levels: the option level's \\' has BOTH characters escaped for the graph"},
		{`:`, `\\:`, "option level only: the graph pass must hand the option pass a \\:"},
		{`,`, `\,`, "graph level only: a doubled escape leaves the graph pass a bare comma"},
		{`[`, `\[`, "graph level only"},
		{`]`, `\]`, "graph level only"},
		{`;`, `\;`, "graph level only"},
		{`%`, `%`, "not a filtergraph character; drawtext's expansion=none is what disarms it"},
		{`a b`, `a b`, "interior whitespace is literal at both levels"},
		{`"`, `"`, "FFmpeg quotes with ' only"},
		{`ü日本`, `ü日本`, "non-ASCII passes through as bytes"},
		{`=`, `=`, "only the FIRST = separates key from value, so a later one is literal"},
		{` a `, `\\\ a\\\ `, "BOUNDARY whitespace is trimmed at both levels unless escaped at both"},
		{"\tTab\t", "\\\\\\\tTab\\\\\\\t", "a tab is whitespace to FFmpeg's tokenizer too"},
		{`  `, `\\\ \\\ `, "a value that is all whitespace is all boundary"},
		{`a  b`, `a  b`, "interior whitespace stays as it was, so ordinary text is unchanged"},
	}
	for _, c := range cases {
		if got := escapeLavfiArg(c.in); got != c.want {
			t.Errorf("escapeLavfiArg(%q) = %q, want %q -- %s", c.in, got, c.want, c.why)
		}
	}
}

// An ordinary value comes out exactly as the old escaper rendered it: only \
// and : appear in an ordinary install's paths, and those are the two characters
// on which the old and the new escaping agree. TestLiveArgsForOrdinaryPathsAreUnchanged
// pins whole argument lists; this pins the rule.
func TestEscapeLavfiArgOnlyChangesValuesWithGraphOnlyMetacharacters(t *testing.T) {
	old := strings.NewReplacer(`\`, `\\\\`, `:`, `\\:`, `,`, `\\,`, `'`, `\\'`, `[`, `\\[`, `]`, `\\]`, `;`, `\\;`)
	for _, v := range []string{
		"/var/lib/polyemesis/fonts/Inter-Regular.ttf",
		"C:/ProgramData/Poly Emesis/fonts/Inter-Regular.ttf",
		`LIVE 12:30 - 100% \o/`,
		"white@0.9",
		"0x101010@0.6",
	} {
		if got, was := escapeLavfiArg(v), old.Replace(v); got != was {
			t.Errorf("escapeLavfiArg(%q) = %q, and it used to be %q; an ordinary value must not change", v, got, was)
		}
	}
}

// Every value, round-tripped through FFmpeg's own filtergraph parser and read
// back out of it. The metadata filter is in every build: one instance stores
// the escaped value under a key, a second prints what it received, so the test
// compares the string FFmpeg PARSED with the string that went in -- not a
// rendering of it. The value is tried both as the LAST option before the ','
// (where the graph level trims trailing whitespace) and in the MIDDLE of the
// options (where only the option level does).
//
// MUTATION: escape boundary whitespace at only one level, or not at all, and
// " LIVE " comes back as "LIVE" or " LIVE\".
func TestEscapeLavfiArgRoundTripsThroughTheRealParser(t *testing.T) {
	bin := testenv.FFmpegBinary(t, "ffmpeg", "without ffmpeg the filtergraph parser cannot be measured")
	values := append([]string{" LIVE ", "  two  ", "\tTab\t", " ", "a  b", "Tom's Show, [LIVE]; 12:30 \\o/ 100%"}, hostileNames...)
	for _, v := range values {
		for _, pos := range []struct{ name, filter string }{
			{"last", "metadata=mode=add:key=k:value=" + escapeLavfiArg(v)},
			{"middle", "metadata=key=k:value=" + escapeLavfiArg(v) + ":mode=add"},
		} {
			graph := "color=s=16x16:d=0.04," + pos.filter + ",metadata=mode=print"
			out, err := exec.Command(bin, "-hide_banner", "-loglevel", "info", "-f", "lavfi", "-i", graph,
				"-frames:v", "1", "-f", "null", "-").CombinedOutput()
			if err != nil {
				t.Errorf("%q (%s): FFmpeg rejected the graph: %v\n%s\ngraph: %s", v, pos.name, err, out, graph)
				continue
			}
			got, found := "", false
			for _, line := range strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n") {
				if i := strings.Index(line, "] k="); i >= 0 {
					got, found = line[i+len("] k="):], true
					break
				}
			}
			if !found {
				t.Errorf("%q (%s): the metadata filter printed no value\n%s", v, pos.name, out)
			} else if got != v {
				t.Errorf("%q (%s): FFmpeg parsed it as %q\ngraph: %s", v, pos.name, got, graph)
			}
		}
	}
}

// GraphArgPath is the clipper's name for the same escaping; they must not drift.
func TestGraphArgPathIsFilterPath(t *testing.T) {
	for _, n := range hostileNames {
		p := "/data/" + n + "/f.ttf"
		if GraphArgPath(p) != filterPath(p) {
			t.Errorf("GraphArgPath and filterPath disagree on %q", p)
		}
	}
}

// The path escaping against the real parser, through the movie= source, which
// every FFmpeg build has -- so this runs on a machine whose FFmpeg has no
// drawtext (Homebrew's), where TestFilterArgumentsSurviveTheRealParser cannot.
// It goes through filterPath, which is what drawtext's fontfile and textfile
// use on the live path.
//
// MUTATION: drop any one character from either level of escapeLavfiArg and the
// subtest named for it fails with FFmpeg's own parse error.
func TestFilterPathOpensADirectoryNamedWithEveryCharacter(t *testing.T) {
	bin := testenv.FFmpegBinary(t, "ffmpeg", "without ffmpeg the filtergraph parser cannot be measured")
	for _, name := range hostileNamesForThisOS() {
		t.Run(name, func(t *testing.T) {
			src := filepath.Join(t.TempDir(), name, "src.mkv")
			if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command(bin, "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi",
				"-i", "color=c=red:s=32x32:d=1", "-c:v", "ffv1", src).CombinedOutput(); err != nil {
				t.Fatalf("fixture: %v %s", err, out)
			}
			graph := "movie=filename=" + filterPath(src) + ",scale=w=16:h=16[v]"
			out, err := exec.Command(bin, "-hide_banner", "-loglevel", "error", "-filter_complex", graph,
				"-map", "[v]", "-frames:v", "1", "-f", "null", "-").CombinedOutput()
			if err != nil {
				t.Errorf("a directory named %q does not survive the graph: %v\n%s\ngraph: %s", name, err, out, graph)
			}
		})
	}
}

// renderDrawtext draws one frame of spec over black and returns the PNG path.
func renderDrawtext(t *testing.T, bin string, spec *TextSpec, out frameSize) string {
	t.Helper()
	png := filepath.Join(t.TempDir(), "f.png")
	graph := "color=c=black:s=" + strconv.Itoa(out.W) + "x" + strconv.Itoa(out.H) + ":d=1," + drawtextFilter(spec, out)
	b, err := exec.Command(bin, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", graph, "-frames:v", "1", "-y", png).CombinedOutput()
	if err != nil {
		t.Fatalf("FFmpeg rejected the graph: %v\ngraph: %s\n%s", err, graph, b)
	}
	return png
}

// grayFrame reads a PNG back as one byte per pixel.
func grayFrame(t *testing.T, bin, png string) []byte {
	t.Helper()
	out, err := exec.Command(bin, "-hide_banner", "-loglevel", "error",
		"-i", png, "-pix_fmt", "gray", "-f", "rawvideo", "-").Output()
	if err != nil {
		t.Fatalf("reading %s back: %v", png, err)
	}
	return out
}

func writeFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// runLiveRendition runs the argv RenditionArgs builds for the live encode --
// an image overlay, boxed overlay text and the live-caption burn-in, every
// file of them under dataDir -- against a one-second file standing in for the
// ingest relay, and the standby slate with its image under the same directory.
// Only the relay URLs are swapped for files; every filter argument is the one
// the engine would run.
func runLiveRendition(t *testing.T, bin, dataDir, font string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ff := func(t *testing.T, args ...string) {
		t.Helper()
		if b, err := exec.CommandContext(ctx, bin, args...).CombinedOutput(); err != nil {
			t.Fatalf("%v\nargv: %q\n%s", err, args, b)
		}
	}

	// The slate first: its image is a plain -i, so only its colour reaches a
	// filter argument, and it must start from the same data directory.
	t.Run("slate", func(t *testing.T) {
		img := filepath.Join(dataDir, "slates", "brb.png")
		if err := os.MkdirAll(filepath.Dir(img), 0o755); err != nil {
			t.Fatal(err)
		}
		ff(t, "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "color=c=white:s=320x180", "-frames:v", "1", img)
		sl := SlateSpec{OutRelayURL: "udp://127.0.0.1:20020", ImagePath: img, Color: "navy", Width: 640, Height: 360}
		slOut := filepath.Join(t.TempDir(), "slate.ts")
		args := SlateArgs(sl)
		args = swapRelays(t, args, "", "", RelayOutputURL(sl.OutRelayURL), slOut)
		// The slate is an endless source; stop it after a few frames.
		args = append(args[:len(args)-1:len(args)-1], "-frames:v", "5", slOut)
		ff(t, args...)
		if st, err := os.Stat(slOut); err != nil || st.Size() == 0 {
			t.Errorf("the slate wrote nothing: %v", err)
		}
	})

	ingest := filepath.Join(t.TempDir(), "ingest.ts")
	ff(t, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "color=c=black:s=640x360:r=30:d=1",
		"-f", "lavfi", "-i", "anullsrc=channel_layout=stereo:sample_rate=48000",
		"-t", "1", "-c:v", "mpeg2video", "-c:a", "mp2", "-f", "mpegts", ingest)
	logo := filepath.Join(dataDir, "overlays", "logo.png")
	if err := os.MkdirAll(filepath.Dir(logo), 0o755); err != nil {
		t.Fatal(err)
	}
	ff(t, "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "color=c=white:s=64x64", "-frames:v", "1", logo)
	burn := writeFile(t, filepath.Join(dataDir, "captions", "burn-source-1.txt"), "caption, with 'quotes'")

	// lit runs the rendition argv and counts the lit pixels of its first frame.
	lit := func(s RenditionSpec) int {
		t.Helper()
		out := filepath.Join(t.TempDir(), "out.ts")
		ff(t, swapRelays(t, RenditionArgs(s), RelayInputURL(s.InRelayURL), ingest, RelayOutputURL(s.OutRelayURL), out)...)
		frame := filepath.Join(t.TempDir(), "frame.png")
		ff(t, "-hide_banner", "-loglevel", "error", "-y", "-i", out, "-frames:v", "1", frame)
		return nonBlackPixels(t, bin, frame)
	}
	s := RenditionSpec{
		InRelayURL: "udp://127.0.0.1:20000", OutRelayURL: "udp://127.0.0.1:20010",
		Width: 640, Height: 360, FPS: 30, VideoKbps: 800,
		Overlay: &OverlaySpec{ImagePath: logo, Anchor: AnchorTopRight, WidthPct: 0.1},
	}
	logoOnly := lit(s)
	s.Text = &TextSpec{Text: "Tom's Show, [LIVE]; 12:30 \\o/ 100%", FontFile: font, SizePct: 0.06, Color: "white"}
	withText := lit(s)
	s.Captions = &TextSpec{TextFile: burn, FontFile: font, Anchor: AnchorBottomCenter, SizePct: 0.06, Color: "yellow"}
	withCaptions := lit(s)
	// Each layer has to add pixels of its own: a graph that parses but drops
	// a layer (a value swallowed by a stray quote) shows up here.
	if logoOnly < 1000 || withText <= logoOnly+300 || withCaptions <= withText+300 {
		t.Errorf("lit pixels: logo %d, +text %d, +captions %d; every layer must draw", logoOnly, withText, withCaptions)
	}
	t.Logf("lit pixels: logo %d, +text %d, +captions %d", logoOnly, withText, withCaptions)
}

// swapRelays replaces the relay URLs in a live argv with files. An empty from
// is left alone.
func swapRelays(t *testing.T, args []string, inFrom, inTo, outFrom, outTo string) []string {
	t.Helper()
	out := append([]string(nil), args...)
	var sawIn, sawOut bool
	for i, a := range out {
		if inFrom != "" && a == inFrom {
			out[i], sawIn = inTo, true
		}
		if a == outFrom {
			out[i], sawOut = outTo, true
		}
	}
	if (inFrom != "" && !sawIn) || !sawOut {
		t.Fatalf("relay URLs not found in argv (in %v, out %v): %q", sawIn, sawOut, args)
	}
	return out
}
