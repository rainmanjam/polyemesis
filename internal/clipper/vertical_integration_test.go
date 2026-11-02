package clipper

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
// THE CAPTION CHECK HAS TWO BRANCHES AND NEITHER IS A SKIP. With drawtext (the
// shipping Alpine image, CI's FFmpeg) the caption band is lit inside the cue's
// window and dark outside it. Without it (a Homebrew FFmpeg has no libfreetype)
// the export must still succeed, carry the warning, and draw nothing.
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

func hasDrawtext(t *testing.T, bin string) bool {
	t.Helper()
	out, err := exec.Command(bin, "-hide_banner", "-filters").Output()
	if err != nil {
		t.Fatalf("ffmpeg -filters: %v", err)
	}
	return strings.Contains(string(out), " drawtext ")
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
	drawtext := hasDrawtext(t, bin)

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
			DrawText: drawtext,
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
	drawtext := hasDrawtext(t, bin)
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
		Cues: []Cue{{StartMS: 10000, EndMS: 11000, Text: "ACROSS THE SEAM"}}, FontFile: font, DrawText: drawtext,
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
	drawtext := hasDrawtext(t, bin)
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
		Cues: []Cue{{StartMS: 21_000, EndMS: 22_000, Text: "AFTER THE GAP"}}, FontFile: font, DrawText: drawtext}}
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
	drawtext := hasDrawtext(t, bin)
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
		Cues: []Cue{{StartMS: 2000, EndMS: 4000, Text: "it's [odd]; but, fine"}}, FontFile: font, DrawText: drawtext}}
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
