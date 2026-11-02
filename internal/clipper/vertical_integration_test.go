package clipper

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// The vertical export against a real FFmpeg, one render per layout.
//
// The fixture is built to make the geometry MEASURABLE rather than merely
// plausible: a flat blue 1920x1080 frame with one solid red box exactly where
// the face-cam box is placed. A face panel that shows red came from that box; a
// centre crop panned right shows red and panned left shows none; the blur
// layout's centred picture puts the box at a computable spot. Flat colours also
// make the caption measurement an absolute one -- there is nothing white in the
// fixture, so white pixels in the caption band are the caption.
//
// Every render is checked with ffprobe for what a short platform requires:
// 1080x1920, H.264 in yuv420p, an AAC track, and the duration asked for.
//
// THE CAPTION CHECK HAS TWO BRANCHES AND NEITHER IS A SKIP. With libass (the
// shipping Alpine image, CI's FFmpeg) the caption band is lit inside the cue's
// window and dark outside it. Without it (a Homebrew FFmpeg has no libass) the
// export must still succeed, carry the warning, and draw nothing.
//
// Set POLYEMESIS_VERTICAL_EVIDENCE to a directory to keep one frame per layout
// and the ffprobe report there.

// faceBox is where the red box is drawn and where the face-cam box is placed.
var faceBox = Rect{X: 0.7, Y: 0.05, W: 0.25, H: 0.3}

func verticalFixture(t *testing.T, bin, path string) {
	t.Helper()
	box := "drawbox=x=1344:y=54:w=480:h=324:color=red:t=fill"
	cmd := exec.Command(bin, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "color=c=blue:s=1920x1080:r=30,"+box,
		"-f", "lavfi", "-i", "sine=f=440:r=48000",
		"-t", "12", "-map", "0:v", "-map", "1:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-g", "30",
		"-c:a", "aac", "-f", "matroska", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v\n%s", err, out)
	}
}

// hasLibass reports whether this FFmpeg has the subtitles filter, which is
// what burns captions.
func hasLibass(t *testing.T, bin string) bool {
	t.Helper()
	out, err := exec.Command(bin, "-hide_banner", "-filters").Output()
	if err != nil {
		t.Fatalf("ffmpeg -filters: %v", err)
	}
	return strings.Contains(string(out), " subtitles ")
}

// frameRGB decodes one frame of a render at an output time as raw RGB.
func frameRGB(t *testing.T, bin, path string, at time.Duration) []byte {
	t.Helper()
	out, err := exec.Command(bin, "-hide_banner", "-loglevel", "error",
		"-ss", secs(at), "-i", path, "-frames:v", "1",
		"-f", "rawvideo", "-pix_fmt", "rgb24", "-").Output()
	if err != nil {
		t.Fatalf("decode a frame at %s: %v", at, err)
	}
	if len(out) != VerticalWidth*VerticalHeight*3 {
		t.Fatalf("frame is %d bytes, not 1080x1920 RGB", len(out))
	}
	return out
}

func pixel(f []byte, x, y int) (r, g, b int) {
	i := (y*VerticalWidth + x) * 3
	return int(f[i]), int(f[i+1]), int(f[i+2])
}

func isRed(r, g, b int) bool  { return r > 170 && g < 90 && b < 90 }
func isBlue(r, g, b int) bool { return b > 170 && r < 90 && g < 90 }

// whiteIn counts near-white pixels in a horizontal band: the caption.
func whiteIn(f []byte, y0, y1 int) int {
	n := 0
	for y := y0; y < y1; y++ {
		for x := 0; x < VerticalWidth; x++ {
			if r, g, b := pixel(f, x, y); r > 215 && g > 215 && b > 215 {
				n++
			}
		}
	}
	return n
}

type probeReport struct {
	Streams []struct {
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
		PixFmt    string `json:"pix_fmt"`
		Channels  int    `json:"channels"`
	} `json:"streams"`
	Format struct {
		Duration   string `json:"duration"`
		FormatName string `json:"format_name"`
	} `json:"format"`
}

func probeShort(t *testing.T, ffprobe, path string) (probeReport, []byte) {
	t.Helper()
	raw, err := exec.Command(ffprobe, "-v", "error", "-show_entries",
		"stream=codec_type,codec_name,width,height,pix_fmt,channels:format=duration,format_name",
		"-of", "json", path).Output()
	if err != nil {
		t.Fatalf("ffprobe rejected the short: %v", err)
	}
	var rep probeReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatalf("ffprobe JSON: %v", err)
	}
	return rep, raw
}

func assertShort(t *testing.T, rep probeReport, want time.Duration) {
	t.Helper()
	var video, audio bool
	for _, s := range rep.Streams {
		switch s.CodecType {
		case "video":
			video = true
			if s.Width != VerticalWidth || s.Height != VerticalHeight || s.CodecName != "h264" || s.PixFmt != "yuv420p" {
				t.Errorf("video is %s %dx%d %s, want h264 1080x1920 yuv420p", s.CodecName, s.Width, s.Height, s.PixFmt)
			}
		case "audio":
			audio = true
			if s.CodecName != "aac" || s.Channels != 2 {
				t.Errorf("audio is %s x%d, want stereo aac", s.CodecName, s.Channels)
			}
		}
	}
	if !video || !audio {
		t.Errorf("video=%v audio=%v, a short needs both", video, audio)
	}
	if !strings.Contains(rep.Format.FormatName, "mp4") {
		t.Errorf("container %q, want mp4", rep.Format.FormatName)
	}
	d, err := strconv.ParseFloat(rep.Format.Duration, 64)
	if err != nil {
		t.Fatalf("duration %q: %v", rep.Format.Duration, err)
	}
	if got := time.Duration(d * float64(time.Second)); got < want-120*time.Millisecond || got > want+120*time.Millisecond {
		t.Errorf("the short runs %s, want %s", got, want)
	}
}

func TestARealVerticalExportOfEachLayout(t *testing.T) {
	bin, ffprobe := integrationTools(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")
	verticalFixture(t, bin, src)
	drawtext := hasLibass(t, bin)

	fonts := filepath.Join(dir, ffmpeg.FontsDirName)
	if err := ffmpeg.EnsureFonts(fonts); err != nil {
		t.Fatal(err)
	}
	font, err := ffmpeg.FontPath(fonts, DefaultCaptionFont)
	if err != nil {
		t.Fatal(err)
	}
	evidence := os.Getenv("POLYEMESIS_VERTICAL_EVIDENCE")

	tl, err := NewTimeline([]Segment{{Path: src, Duration: 12 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	const in, out = 2 * time.Second, 8 * time.Second
	// Spoken 3.0-5.0s into the recording: 1.0-3.0s into the short.
	caption := func() *Captions {
		return &Captions{
			Cues:     []Cue{{StartMS: 3000, EndMS: 5000, Text: "HELLO SHORTS"}},
			Style:    CaptionStyle{Box: false},
			FontFile: font,
			Burn:     drawtext,
		}
	}
	type probe struct {
		x, y int
		red  bool
	}
	layouts := []struct {
		name   string
		v      Vertical
		probes []probe
	}{
		{"center-right", Vertical{Layout: LayoutCenter, Focus: 1}, []probe{{540, 300, true}, {540, 1700, false}}},
		{"center-left", Vertical{Layout: LayoutCenter, Focus: 0}, []probe{{540, 300, false}, {100, 100, false}}},
		// The face panel is the top 768 rows, cut from the red box; the game
		// panel below is the whole frame, mostly blue at its centre.
		{"stacked", Vertical{Layout: LayoutStacked, Face: faceBox}, []probe{{540, 384, true}, {100, 100, true}, {540, 1100, false}}},
		// The whole 1920x1080 frame fitted to 1080x608, centred at y=656..1264:
		// the red box lands around (891, 776).
		{"blur", Vertical{Layout: LayoutBlur}, []probe{{891, 776, true}, {300, 900, false}}},
	}
	c := New(testLog(), bin, ffprobe)
	for _, l := range layouts {
		t.Run(l.name, func(t *testing.T) {
			v := l.v
			v.Captions = caption()
			path := filepath.Join(dir, l.name+".mp4")
			res, err := c.Cut(context.Background(), tl, Request{
				In: in, Out: out, OutPath: path, Mode: ModePrecise, Vertical: &v,
			}, nil)
			if err != nil {
				t.Fatalf("Cut: %v", err)
			}
			rep, raw := probeShort(t, ffprobe, path)
			assertShort(t, rep, out-in)

			mid := frameRGB(t, bin, path, 2*time.Second)
			for _, p := range l.probes {
				r, g, b := pixel(mid, p.x, p.y)
				if p.red && !isRed(r, g, b) {
					t.Errorf("(%d,%d) is rgb(%d,%d,%d), want the red face box there", p.x, p.y, r, g, b)
				}
				if !p.red && isRed(r, g, b) {
					t.Errorf("(%d,%d) is red; the box should not be there", p.x, p.y)
				}
			}
			// The caption band: centred at 72% of 1920.
			band0, band1 := 1300, 1470
			during := whiteIn(mid, band0, band1)
			after := whiteIn(frameRGB(t, bin, path, 4500*time.Millisecond), band0, band1)
			if drawtext {
				if during < 800 || after > 50 {
					t.Errorf("caption band lit %d px during the cue and %d after; want a caption only during it", during, after)
				}
			} else {
				if during != 0 {
					t.Errorf("no drawtext, yet %d white pixels in the caption band", during)
				}
				warned := false
				for _, w := range res.Plan.Warnings {
					warned = warned || strings.Contains(w, "captions were not burned")
				}
				if !warned {
					t.Errorf("captions were dropped without a warning: %v", res.Plan.Warnings)
				}
			}
			t.Logf("%s: drawtext=%v caption px during=%d after=%d", l.name, drawtext, during, after)
			if evidence != "" {
				keepEvidence(t, bin, path, raw, filepath.Join(evidence, l.name))
			}
		})
	}
}

func keepEvidence(t *testing.T, bin, path string, probe []byte, prefix string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(prefix), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(bin, "-hide_banner", "-loglevel", "error", "-y", "-ss", "2",
		"-i", path, "-frames:v", "1", prefix+".png").CombinedOutput(); err != nil {
		t.Fatalf("evidence frame: %v %s", err, out)
	}
	if err := os.WriteFile(prefix+".ffprobe.json", probe, 0o644); err != nil {
		t.Fatal(err)
	}
}

// A short cut across a segment boundary: the concat demuxer, the output-side
// seek, and the caption shift that goes with it, rendered for real. The caption
// must be lit at the time it was spoken, not hours early.
func TestARealVerticalExportAcrossASegmentBoundaryKeepsCaptionsInPlace(t *testing.T) {
	bin, ffprobe := integrationTools(t)
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.mkv"), filepath.Join(dir, "b.mkv")
	verticalFixture(t, bin, a)
	verticalFixture(t, bin, b)
	drawtext := hasLibass(t, bin)
	fonts := filepath.Join(dir, ffmpeg.FontsDirName)
	if err := ffmpeg.EnsureFonts(fonts); err != nil {
		t.Fatal(err)
	}
	font, _ := ffmpeg.FontPath(fonts, DefaultCaptionFont)

	tl, err := NewTimeline([]Segment{
		{Path: a, Start: 0, Duration: 12 * time.Second},
		{Path: b, Start: 12 * time.Second, Duration: 12 * time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "seam.mp4")
	v := Vertical{Layout: LayoutCenter, Focus: 0.5, Captions: &Captions{
		// 10.0-11.0s on the timeline: 2.0-3.0s into a short that starts at 8s.
		Cues: []Cue{{StartMS: 10000, EndMS: 11000, Text: "ACROSS THE SEAM"}}, FontFile: font, Burn: drawtext,
	}}
	res, err := New(testLog(), bin, ffprobe).Cut(context.Background(), tl, Request{
		In: 8 * time.Second, Out: 16 * time.Second, OutPath: path, Mode: ModePrecise, Vertical: &v,
	}, nil)
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	if !res.Plan.Concat {
		t.Fatal("the short spans both files")
	}
	rep, _ := probeShort(t, ffprobe, path)
	assertShort(t, rep, 8*time.Second)
	before := whiteIn(frameRGB(t, bin, path, 1*time.Second), 1300, 1470)
	during := whiteIn(frameRGB(t, bin, path, 2500*time.Millisecond), 1300, 1470)
	if drawtext && (during < 800 || before > 50) {
		t.Errorf("caption lit %d px before its cue and %d during; the output seek shifted it", before, during)
	}
	if !drawtext && during != 0 {
		t.Errorf("no drawtext, yet %d caption pixels", during)
	}
}

// SOURCES THAT ARE NOT A CLEAN 16:9. An odd-sized master, a portrait one, an
// ultrawide one and one far too small to crop a 2% box out of, through every
// layout with the face box hard against the corner: each must still produce a
// 1080x1920 yuv420p short. A crop that rounds to zero or to an odd size is an
// FFmpeg error, not a picture.
//
// MUTATION: cropTo without the even, at-least-2 floor. Observed: FAIL on the
// 35x19 source ("Invalid too big or non positive size").
func TestARealVerticalExportOfAwkwardSources(t *testing.T) {
	bin, ffprobe := integrationTools(t)
	dir := t.TempDir()
	corner := Rect{X: 0.98, Y: 0.98, W: 0.02, H: 0.02}
	layouts := map[string]Vertical{
		"center":  {Layout: LayoutCenter, Focus: 1},
		"blur":    {Layout: LayoutBlur, Focus: 0.5},
		"stacked": {Layout: LayoutStacked, Face: corner, Game: corner, FaceShare: 0.6, FaceBottom: true},
	}
	c := New(testLog(), bin, ffprobe)
	for _, size := range []string{"1279x719", "1080x1920", "3440x1440", "35x19"} {
		src := filepath.Join(dir, size+".mkv")
		gen := exec.Command(bin, "-hide_banner", "-loglevel", "error", "-y",
			"-f", "lavfi", "-i", "testsrc2=s="+size+":r=30",
			"-f", "lavfi", "-i", "sine=f=440:r=48000",
			"-t", "3", "-map", "0:v", "-map", "1:a",
			"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv444p",
			"-c:a", "aac", "-f", "matroska", src)
		if out, err := gen.CombinedOutput(); err != nil {
			t.Fatalf("fixture %s: %v\n%s", size, err, out)
		}
		tl, err := NewTimeline([]Segment{{Path: src, Duration: 3 * time.Second}})
		if err != nil {
			t.Fatal(err)
		}
		for name, v := range layouts {
			v := v
			t.Run(size+"/"+name, func(t *testing.T) {
				out := filepath.Join(dir, size+"-"+name+".mp4")
				if _, err := c.Cut(context.Background(), tl, Request{In: 500 * time.Millisecond, Out: 2500 * time.Millisecond,
					OutPath: out, Mode: ModePrecise, Vertical: &v}, nil); err != nil {
					t.Fatalf("Cut: %v", err)
				}
				rep, _ := probeShort(t, ffprobe, out)
				assertShort(t, rep, 2*time.Second)
			})
		}
	}
}

// The same gap, rendered: the short is 8 s, and the caption spoken 1 s into
// the second file is lit 3 s into the short (2 s of the first file, then 1 s).
func TestARealVerticalExportAcrossAGapKeepsCaptionsOnTheirPicture(t *testing.T) {
	bin, ffprobe := integrationTools(t)
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.mkv"), filepath.Join(dir, "b.mkv")
	verticalFixture(t, bin, a)
	verticalFixture(t, bin, b)
	drawtext := hasLibass(t, bin)
	fonts := filepath.Join(dir, ffmpeg.FontsDirName)
	if err := ffmpeg.EnsureFonts(fonts); err != nil {
		t.Fatal(err)
	}
	font, _ := ffmpeg.FontPath(fonts, DefaultCaptionFont)
	tl, err := NewTimeline([]Segment{{Path: a, Start: 0, Duration: 12 * time.Second}, {Path: b, Start: 20 * time.Second, Duration: 12 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	v := Vertical{Layout: LayoutCenter, Focus: 0.5, Captions: &Captions{
		Cues: []Cue{{StartMS: 21_000, EndMS: 22_000, Text: "AFTER THE GAP"}}, FontFile: font, Burn: drawtext}}
	out := filepath.Join(dir, "gap.mp4")
	if _, err := New(testLog(), bin, ffprobe).Cut(context.Background(), tl, Request{In: 10 * time.Second, Out: 26 * time.Second,
		OutPath: out, Mode: ModePrecise, Vertical: &v}, nil); err != nil {
		t.Fatalf("Cut: %v", err)
	}
	rep, _ := probeShort(t, ffprobe, out)
	assertShort(t, rep, 8*time.Second)
	lit := whiteIn(frameRGB(t, bin, out, 3500*time.Millisecond), 1300, 1470)
	early := whiteIn(frameRGB(t, bin, out, 1500*time.Millisecond), 1300, 1470)
	if drawtext && (lit < 800 || early > 50) {
		t.Errorf("caption lit %d px at 3.5 s and %d at 1.5 s; want it only on its own picture", lit, early)
	}
}

// A captioned short whose fonts and work directories sit under a name carrying
// every filtergraph metacharacter a POSIX or Windows directory can hold. The
// font path and every caption sidecar go through GraphArgPath; with the
// single-level escaper this exited 234 before drawing a frame.
func TestARealCaptionedShortFromADirectoryWithGraphMetacharacters(t *testing.T) {
	bin, ffprobe := integrationTools(t)
	root := filepath.Join(t.TempDir(), "it's,[all];here")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(root, "src.mkv")
	verticalFixture(t, bin, src)
	drawtext := hasLibass(t, bin)
	fonts := filepath.Join(root, ffmpeg.FontsDirName)
	if err := ffmpeg.EnsureFonts(fonts); err != nil {
		t.Fatal(err)
	}
	font, _ := ffmpeg.FontPath(fonts, DefaultCaptionFont)
	tl, err := NewTimeline([]Segment{{Path: src, Duration: 12 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	v := Vertical{Layout: LayoutCenter, Focus: 0.5, Captions: &Captions{
		Cues: []Cue{{StartMS: 2000, EndMS: 4000, Text: "it's [odd]; but, fine"}}, FontFile: font, Burn: drawtext}}
	out := filepath.Join(root, "short.mp4")
	if _, err := New(testLog(), bin, ffprobe).Cut(context.Background(), tl, Request{In: time.Second, Out: 5 * time.Second,
		OutPath: out, Mode: ModePrecise, Vertical: &v}, nil); err != nil {
		t.Fatalf("Cut: %v", err)
	}
	rep, _ := probeShort(t, ffprobe, out)
	assertShort(t, rep, 4*time.Second)
	if lit := whiteIn(frameRGB(t, bin, out, 2*time.Second), 1300, 1470); drawtext && lit < 800 {
		t.Errorf("the caption drew %d px from a hostile directory", lit)
	}
}

// The gap-start case rendered: the short is the 4 s of picture after the
// recording resumes, and the caption is lit 1 s in.
func TestARealVerticalExportStartingInAGap(t *testing.T) {
	bin, ffprobe := integrationTools(t)
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.mkv"), filepath.Join(dir, "b.mkv")
	verticalFixture(t, bin, a)
	verticalFixture(t, bin, b)
	tl, err := NewTimeline([]Segment{{Path: a, Start: 0, Duration: 12 * time.Second}, {Path: b, Start: 20 * time.Second, Duration: 12 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "gapstart.mp4")
	v := Vertical{Layout: LayoutCenter, Focus: 0.5}
	if _, err := New(testLog(), bin, ffprobe).Cut(context.Background(), tl, Request{In: 13 * time.Second, Out: 24 * time.Second,
		OutPath: out, Mode: ModePrecise, Vertical: &v}, nil); err != nil {
		t.Fatalf("Cut: %v", err)
	}
	rep, _ := probeShort(t, ffprobe, out)
	assertShort(t, rep, 4*time.Second)
}

// litRows is, for each row of a frame, whether it holds a near-white pixel.
func litRows(f []byte) []bool {
	rows := make([]bool, VerticalHeight)
	for y := 0; y < VerticalHeight; y++ {
		for x := 0; x < VerticalWidth; x++ {
			if r, g, b := pixel(f, x, y); r > 215 && g > 215 && b > 215 {
				rows[y] = true
				break
			}
		}
	}
	return rows
}

// bands are the runs of lit rows, merging runs closer than gap rows: a caption
// line is one band, two stacked captions are two.
func bands(rows []bool, gap int) [][2]int {
	var out [][2]int
	for y := 0; y < len(rows); y++ {
		if !rows[y] {
			continue
		}
		start := y
		for y < len(rows) && rows[y] {
			y++
		}
		if k := len(out) - 1; k >= 0 && start-out[k][1] <= gap {
			out[k][1] = y
			continue
		}
		out = append(out, [2]int{start, y})
	}
	return out
}

// captionedShort renders a 4 s centre-crop short of the blue/red fixture with
// cues and a style, returning the path and whether libass was there to burn.
func captionedShort(t *testing.T, name string, cues []Cue, style CaptionStyle) (string, bool) {
	t.Helper()
	bin, ffprobe := integrationTools(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")
	verticalFixture(t, bin, src)
	burn := hasLibass(t, bin)
	fonts := filepath.Join(dir, ffmpeg.FontsDirName)
	if err := ffmpeg.EnsureFonts(fonts); err != nil {
		t.Fatal(err)
	}
	font, _ := ffmpeg.FontPath(fonts, DefaultCaptionFont)
	tl, _ := NewTimeline([]Segment{{Path: src, Duration: 12 * time.Second}})
	v := Vertical{Layout: LayoutCenter, Focus: 0.5, Captions: &Captions{Cues: cues, Style: style, FontFile: font, Burn: burn}}
	out := filepath.Join(dir, name+".mp4")
	if _, err := New(testLog(), bin, ffprobe).Cut(context.Background(), tl, Request{In: 0, Out: 4 * time.Second,
		OutPath: out, Mode: ModePrecise, Vertical: &v}, nil); err != nil {
		t.Fatalf("Cut: %v", err)
	}
	rep, raw := probeShort(t, ffprobe, out)
	assertShort(t, rep, 4*time.Second)
	if ev := os.Getenv("POLYEMESIS_VERTICAL_EVIDENCE"); ev != "" {
		keepEvidence(t, bin, out, raw, filepath.Join(ev, "caption-"+name))
	}
	return out, burn
}

// CONCURRENT CAPTIONS STACK. Two cues on screen at once are two separate
// bands of text, not one drawn over the other -- libass's collision handling,
// measured in the rendered pixels.
//
// MUTATION: every event pinned to one spot (\pos). Observed: FAIL, one band.
func TestARealVerticalExportStacksConcurrentCaptions(t *testing.T) {
	out, burn := captionedShort(t, "stacked-cues", []Cue{
		{StartMS: 500, EndMS: 3500, Text: "FIRST SPEAKER"},
		{StartMS: 700, EndMS: 3500, Text: "SECOND SPEAKER", Track: 1},
	}, CaptionStyle{})
	if !burn {
		return
	}
	bin, _ := integrationTools(t)
	got := bands(litRows(frameRGB(t, bin, out, 2*time.Second)), 8)
	if len(got) < 2 {
		t.Errorf("concurrent captions drew %d band(s) %v, want two stacked", len(got), got)
	}
	for i := 1; i < len(got); i++ {
		if got[i][0] < got[i-1][1] {
			t.Errorf("bands overlap: %v", got)
		}
	}
}

// ONE SPEAKER'S LINES DO NOT JUMP. Adjacent cues on one track that overlap by
// a few milliseconds -- as a transcriber's do -- draw the second line exactly
// where it sits alone, and a chain of them stays on one band.
//
// MUTATION: no same-track clamp. Observed: FAIL, the second line sits a line
// higher (y 1261 against 1355) for its whole span.
func TestARealVerticalExportKeepsOneSpeakersLinesInPlace(t *testing.T) {
	alone, burn := captionedShort(t, "line-alone", []Cue{{StartMS: 500, EndMS: 3500, Text: "second line"}}, CaptionStyle{})
	if !burn {
		return
	}
	bin, _ := integrationTools(t)
	want := bands(litRows(frameRGB(t, bin, alone, 2500*time.Millisecond)), 6)
	if len(want) == 0 {
		t.Fatal("the lone caption drew nothing")
	}
	for _, ov := range []int64{20, 200} {
		out, _ := captionedShort(t, fmt.Sprintf("line-overlap%d", ov), []Cue{
			{StartMS: 200, EndMS: 1500 + ov, Text: "first line"},
			{StartMS: 1500, EndMS: 3800, Text: "second line"},
		}, CaptionStyle{})
		got := bands(litRows(frameRGB(t, bin, out, 2500*time.Millisecond)), 6)
		if len(got) == 0 || got[0][0] != want[0][0] {
			t.Errorf("overlap %d ms: the second line is at %v, alone it is at %v", ov, got, want)
		}
	}
	var chain []Cue
	for i := 0; i < 5; i++ {
		chain = append(chain, Cue{StartMS: int64(200 + 700*i), EndMS: int64(200 + 700*(i+1) + 20), Text: fmt.Sprintf("line %d", i)})
	}
	out, _ := captionedShort(t, "line-chain", chain, CaptionStyle{})
	var tops []int
	for i := 0; i < 5; i++ {
		b := bands(litRows(frameRGB(t, bin, out, time.Duration(200+700*i+400)*time.Millisecond)), 6)
		if len(b) != 1 {
			t.Errorf("chain cue %d drew bands %v, want one", i, b)
			continue
		}
		tops = append(tops, b[0][0])
	}
	for i := 1; i < len(tops); i++ {
		if d := tops[i] - tops[0]; d < -6 || d > 6 {
			t.Errorf("a chain of one speaker's lines bounces: tops %v", tops)
			break
		}
	}
}

// edgeHeight is how many rows are lit in the n columns at one end of the lit
// span of rows [y0, y1): tall for a letter's stem, short for a full stop.
func edgeHeight(f []byte, y0, y1, n int, left bool) int {
	x0, x1 := VerticalWidth, -1
	for y := y0; y < y1; y++ {
		for x := 0; x < VerticalWidth; x++ {
			if r, g, b := pixel(f, x, y); r > 215 && g > 215 && b > 215 {
				x0, x1 = min(x0, x), max(x1, x)
			}
		}
	}
	if x1 < 0 {
		return 0
	}
	from, to := x0, x0+n
	if !left {
		from, to = x1-n+1, x1+1
	}
	h := 0
	for y := y0; y < y1; y++ {
		for x := from; x < to; x++ {
			if r, g, b := pixel(f, x, y); r > 215 && g > 215 && b > 215 {
				h++
				break
			}
		}
	}
	return h
}

// A RIGHT-TO-LEFT CAPTION READS RIGHT TO LEFT. The paragraph direction comes
// from the text, so a Hebrew sentence's full stop is drawn at its LEFT end,
// where a reader of Hebrew finishes -- not at the right, as a left-to-right
// paragraph would put it.
//
// MUTATION: the style forces Encoding 1 (left-to-right base). Observed: FAIL,
// the full stop at the right end.
func TestARealVerticalExportPutsRightToLeftPunctuationAtTheLeft(t *testing.T) {
	out, burn := captionedShort(t, "rtl-stop", []Cue{{StartMS: 500, EndMS: 3500, Text: "שלום עולם."}}, CaptionStyle{SizePct: 8})
	if !burn {
		return
	}
	bin, _ := integrationTools(t)
	f := frameRGB(t, bin, out, 2*time.Second)
	b := bands(litRows(f), 6)
	if len(b) != 1 {
		t.Fatalf("bands %v, want one line", b)
	}
	left, right := edgeHeight(f, b[0][0], b[0][1]+1, 8, true), edgeHeight(f, b[0][0], b[0][1]+1, 8, false)
	// A full stop is the shortest thing at either end; ש's right edge and ם's
	// left edge are both taller (measured: stop 20 rows, ש 36, ם 71).
	if left >= right {
		t.Errorf("the left end is %d rows tall and the right %d: the full stop is not at the left", left, right)
	}
}

// CAPTIONS STAY IN THE FRAME at the extremes of the position slider: at 5% the
// text hangs from near the top, at 95% it stands near the bottom, and two
// lines at either end are drawn whole.
func TestARealVerticalExportKeepsCaptionsInTheFrame(t *testing.T) {
	two := "A CAPTION LONG ENOUGH TO TAKE TWO LINES AT THIS SIZE"
	for _, pos := range []float64{5, 95} {
		out, burn := captionedShort(t, fmt.Sprintf("pos%.0f", pos), []Cue{{StartMS: 500, EndMS: 3500, Text: two}},
			CaptionStyle{PositionPct: pos})
		if !burn {
			continue
		}
		bin, _ := integrationTools(t)
		got := bands(litRows(frameRGB(t, bin, out, 2*time.Second)), 40)
		if len(got) == 0 {
			t.Fatalf("position %.0f%%: nothing drawn", pos)
		}
		first, last := got[0][0], got[len(got)-1][1]
		if first <= 0 || last >= VerticalHeight {
			t.Errorf("position %.0f%%: text runs to the frame's edge (%d..%d)", pos, first, last)
		}
		if pos == 5 && first > VerticalHeight/4 || pos == 95 && last < VerticalHeight*3/4 {
			t.Errorf("position %.0f%%: text at %d..%d, not near its edge", pos, first, last)
		}
	}
}

// HOSTILE TEXT IS DRAWN AS TEXT. An override block, a backslash-N, a percent
// expansion and quotes in what somebody said render as those characters at the
// caption size; none of them changes the size, breaks the line or expands.
func TestARealVerticalExportDrawsHostileTextAsText(t *testing.T) {
	out, burn := captionedShort(t, "hostile", []Cue{{StartMS: 500, EndMS: 3500,
		Text: `{\fs400}BIG it's "x" a\Nb 100% %{pts}`}}, CaptionStyle{})
	if !burn {
		return
	}
	bin, _ := integrationTools(t)
	got := bands(litRows(frameRGB(t, bin, out, 2*time.Second)), 30)
	if len(got) == 0 {
		t.Fatal("nothing drawn")
	}
	if h := got[len(got)-1][1] - got[0][0]; h > 400 {
		t.Errorf("the caption is %d px tall: an override in the text was obeyed", h)
	}
	// Text inside braces is DRAWN: unescaped, libass reads it as an override
	// block and draws nothing at all.
	// MUTATION: braces not escaped. Observed: FAIL, the braced caption is blank.
	braced, _ := captionedShort(t, "braced", []Cue{{StartMS: 500, EndMS: 3500, Text: "{HIDDEN WORDS}"}}, CaptionStyle{})
	if n := whiteIn(frameRGB(t, bin, braced, 2*time.Second), 1200, 1600); n < 800 {
		t.Errorf("a caption in braces drew %d px; libass swallowed it as an override block", n)
	}
}

// tofuOf is text with every character but a space replaced by one no font
// has, so libass draws each as the missing-glyph box: what text whose script
// has no font looks like.
func tofuOf(text string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' {
			return r
		}
		return '\U0010FFFD'
	}, text)
}

// litDiff is how many pixels are lit in one frame and not the other, over how
// many are lit in a.
func litDiff(a, b []byte) (diff, lit int) {
	for y := 0; y < VerticalHeight; y++ {
		for x := 0; x < VerticalWidth; x++ {
			ra, ga, ba := pixel(a, x, y)
			rb, gb, bb := pixel(b, x, y)
			la := ra > 215 && ga > 215 && ba > 215
			lb := rb > 215 && gb > 215 && bb > 215
			if la {
				lit++
			}
			if la != lb {
				diff++
			}
		}
	}
	return diff, lit
}

// OTHER SCRIPTS ARE LETTERS, NOT BOXES. Each script is rendered beside the same
// number of characters no font has; a script whose font fallback failed draws
// exactly those boxes, so its frame matches the boxes' and the test fails.
// Latin comes from the bundled font, Arabic and Hebrew from DejaVu through
// fontconfig -- which the shipping image installs and CI's Linux job installs,
// so there they are asserted. CJK and emoji are opt-in fonts: they are
// rendered for the evidence directory and logged.
//
// MUTATION: no fallback font (the image without DejaVu). Observed: FAIL,
// arabic and hebrew match their boxes.
func TestARealVerticalExportOfOtherScripts(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		asserted   bool
	}{
		{"latin", "Café déjà vu", true}, {"arabic", "مرحبا بالعالم", true}, {"hebrew", "שלום עולם", true},
		{"cjk", "你好，世界 こんにちは 안녕하세요", false}, {"emoji", "GG 🎮🔥😀", false},
	} {
		out, burn := captionedShort(t, tc.name, []Cue{{StartMS: 500, EndMS: 3500, Text: tc.text}}, CaptionStyle{})
		if !burn {
			continue
		}
		bin, _ := integrationTools(t)
		boxes, _ := captionedShort(t, tc.name+"-boxes", []Cue{{StartMS: 500, EndMS: 3500, Text: tofuOf(tc.text)}}, CaptionStyle{})
		f := frameRGB(t, bin, out, 2*time.Second)
		diff, lit := litDiff(f, frameRGB(t, bin, boxes, 2*time.Second))
		t.Logf("%s: %d lit px, %d differ from the same count of missing-glyph boxes", tc.name, lit, diff)
		if lit < 200 {
			t.Errorf("%s drew %d px", tc.name, lit)
			continue
		}
		if tc.asserted && runtime.GOOS == "linux" && diff*4 < lit {
			t.Errorf("%s drew boxes, not letters: only %d of %d lit px differ from missing-glyph boxes "+
				"(is fontconfig with DejaVu installed? the shipping image and CI install it)", tc.name, diff, lit)
		}
	}
}

// A REAL CAPTIONED ENCODE THAT FAILS says FFmpeg's reason and nothing libass
// said: libass, given the fonts directory, complains about every file in it
// that is not a font, and about every caption character no font has.
func TestARealFailedCaptionedEncodeSaysWhyWithoutLibassChatter(t *testing.T) {
	bin, ffprobe := integrationTools(t)
	if !hasLibass(t, bin) {
		return
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")
	verticalFixture(t, bin, src)
	fonts := filepath.Join(dir, ffmpeg.FontsDirName)
	if err := ffmpeg.EnsureFonts(fonts); err != nil {
		t.Fatal(err)
	}
	font, _ := ffmpeg.FontPath(fonts, DefaultCaptionFont)
	tl, _ := NewTimeline([]Segment{{Path: src, Duration: 12 * time.Second}})
	v := Vertical{Layout: LayoutCenter, Focus: 0.5, Captions: &Captions{Cues: []Cue{
		{StartMS: 100, EndMS: 3900, Text: "你好世界 GG"}}, FontFile: font, Burn: true}}
	_, err := New(testLog(), bin, ffprobe).Cut(context.Background(), tl, Request{In: 0, Out: 4 * time.Second,
		OutPath: filepath.Join(dir, "f.mp4"), Mode: ModePrecise, VideoEncoder: "h264_nonexistent264", Vertical: &v}, nil)
	if err == nil {
		t.Fatal("an encode with no encoder succeeded")
	}
	if !strings.Contains(err.Error(), "h264_nonexistent264") {
		t.Errorf("the error lost FFmpeg's reason: %q", err)
	}
	for _, leak := range []string{"Parsed_subtitles", "glyph", "LICENSE", "fontselect"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("the error carries %q: %q", leak, err)
		}
	}
}

// A REAL CAPTION RENDERER FAILURE SAYS WHY. When the subtitles filter itself
// cannot start -- here its script is gone by the time FFmpeg runs -- its own
// reason ("Unable to open ...captions.ass") is in the error; it names a file
// this server wrote, not anything that was said.
//
// MUTATION: every subtitles-filter line dropped. Observed: FAIL, only
// "Error initializing filters".
func TestARealCaptionRendererFailureSaysWhy(t *testing.T) {
	bin, ffprobe := integrationTools(t)
	if !hasLibass(t, bin) {
		return
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")
	verticalFixture(t, bin, src)
	fonts := filepath.Join(dir, ffmpeg.FontsDirName)
	if err := ffmpeg.EnsureFonts(fonts); err != nil {
		t.Fatal(err)
	}
	font, _ := ffmpeg.FontPath(fonts, DefaultCaptionFont)
	tl, _ := NewTimeline([]Segment{{Path: src, Duration: 12 * time.Second}})
	// The runner removes the script the step wrote, then runs FFmpeg for real.
	run := func(ctx context.Context, name string, args []string) ([]byte, error) {
		for i, a := range args {
			if a == "-/filter_complex" && i+1 < len(args) {
				scripts, _ := filepath.Glob(filepath.Join(filepath.Dir(args[i+1]), "*.ass"))
				for _, s := range scripts {
					if err := os.Remove(s); err != nil {
						t.Errorf("remove the script: %v", err)
					}
				}
			}
		}
		return runCommand(ctx, name, args)
	}
	v := Vertical{Layout: LayoutCenter, Focus: 0.5, Captions: &Captions{Cues: []Cue{
		{StartMS: 100, EndMS: 3900, Text: "你好世界 GG"}}, FontFile: font, Burn: true}}
	_, err := New(testLog(), bin, ffprobe, WithRunner(run)).Cut(context.Background(), tl, Request{In: 0, Out: 4 * time.Second,
		OutPath: filepath.Join(dir, "f.mp4"), Mode: ModePrecise, Vertical: &v}, nil)
	if err == nil {
		t.Fatal("an encode with no caption script succeeded")
	}
	if !strings.Contains(err.Error(), "Unable to open") || !strings.Contains(err.Error(), ".ass") {
		t.Errorf("the renderer's reason is not in the error: %q", err)
	}
	for _, leak := range []string{"glyph", "fontselect", "你", "GG"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("the error carries %q: %q", leak, err)
		}
	}
}
