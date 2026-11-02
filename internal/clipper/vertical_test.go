package clipper

import (
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

var testVerticalOut = filepath.Join(testClipDir, "short.mp4")

func verticalReq(in, out time.Duration, v Vertical, mut ...func(*Request)) Request {
	r := Request{In: in, Out: out, OutPath: testVerticalOut, Mode: ModePrecise, Vertical: &v}
	for _, fn := range mut {
		fn(&r)
	}
	return r
}

func mustPlan(t *testing.T, tl Timeline, kf Keyframes, r Request) Plan {
	t.Helper()
	p, err := PlanCut(tl, kf, r)
	if err != nil {
		t.Fatalf("PlanCut: %v", err)
	}
	return p
}

func onlyCommand(t *testing.T, p Plan) Command {
	t.Helper()
	cmds, err := p.Commands(testWorkDir)
	if err != nil {
		t.Fatalf("Commands: %v", err)
	}
	if len(cmds) != 1 || cmds[0].Name != "vertical" {
		t.Fatalf("a vertical export is one encode, got %v", names(cmds))
	}
	return cmds[0]
}

func graphOf(t *testing.T, c Command) string {
	t.Helper()
	want := filepath.Join(testWorkDir, "vertical.graph")
	for _, f := range c.Files {
		if f.Path == want {
			return f.Content
		}
	}
	t.Fatalf("no filtergraph sidecar at %s in %+v", want, c.Files)
	return ""
}

// The face-cam box the operator dragged onto a preview frame is the crop the
// face panel is cut from: four fractions, rendered into the crop as fractions of
// the source's own width and height, so the same box lands in the same place on
// any resolution of master. The gameplay box crops the second panel; the panel
// heights are the share split of 1920, the face on top unless asked otherwise.
//
// MUTATION: cropTo swaps x and y. Observed: FAIL, the face crop no longer reads
// x=iw*0.700000:y=ih*0.050000.
// MUTATION: faceHeight ignores FaceShare. Observed: FAIL, 768 instead of 576.
// MUTATION: FaceBottom ignored. Observed: FAIL, [face][game] in both cases.
func TestTheFaceBoxIsTheCropTheFacePanelIsCutFrom(t *testing.T) {
	v := Vertical{
		Layout:    LayoutStacked,
		Face:      Rect{X: 0.7, Y: 0.05, W: 0.25, H: 0.3},
		Game:      Rect{X: 0.1, Y: 0.2, W: 0.6, H: 0.75},
		FaceShare: 0.3,
	}
	g := graphOf(t, onlyCommand(t, mustPlan(t, oneHourTimeline(t), gop(1000, 2*time.Second),
		verticalReq(10*time.Second, 40*time.Second, v))))

	for _, want := range []string{
		"[fa]crop=w='max(2,trunc(iw*0.250000/2)*2)':h='max(2,trunc(ih*0.300000/2)*2)':x=iw*0.700000:y=ih*0.050000," +
			"scale=w=1080:h=576:force_original_aspect_ratio=increase,crop=w=1080:h=576[face]",
		"[ga]crop=w='max(2,trunc(iw*0.600000/2)*2)':h='max(2,trunc(ih*0.750000/2)*2)':x=iw*0.100000:y=ih*0.200000," +
			"scale=w=1080:h=1344:force_original_aspect_ratio=increase,crop=w=1080:h=1344[game]",
		"[face][game]vstack=inputs=2[base]",
	} {
		if !strings.Contains(g, want) {
			t.Errorf("graph is missing\n  %s\ngot\n  %s", want, g)
		}
	}

	v.FaceBottom = true
	v.FaceShare = 0
	g = graphOf(t, onlyCommand(t, mustPlan(t, oneHourTimeline(t), gop(1000, 2*time.Second),
		verticalReq(10*time.Second, 40*time.Second, v))))
	if !strings.Contains(g, "[game][face]vstack=inputs=2[base]") {
		t.Errorf("FaceBottom did not put the face panel under the gameplay: %s", g)
	}
	if !strings.Contains(g, "crop=w=1080:h=768[face]") || !strings.Contains(g, "crop=w=1080:h=1152[game]") {
		t.Errorf("the default share is %.1f of 1920 = 768 over 1152: %s", DefaultFaceShare, g)
	}
}

// A zero gameplay box means the whole frame, cover-cropped into its panel. The
// panel heights always sum to the output and are even, so a 4:2:0 encode never
// rounds a seam.
func TestStackedPanelsAreEvenAndFillTheFrame(t *testing.T) {
	for _, share := range []float64{0.2, 0.33, 0.4, 0.45, 0.6} {
		v := Vertical{Layout: LayoutStacked, Face: Rect{X: 0, Y: 0, W: 0.3, H: 0.3}, FaceShare: share}
		fh := v.faceHeight()
		if fh%2 != 0 || fh <= 0 || fh >= VerticalHeight {
			t.Errorf("share %.2f: face panel %d is not an even height inside the frame", share, fh)
		}
		g := v.pictureGraph()
		if strings.Contains(g, "[ga]crop=") {
			t.Errorf("share %.2f: a zero gameplay box must not crop: %s", share, g)
		}
		if !strings.Contains(g, "crop=w=1080:h="+strconv.Itoa(VerticalHeight-fh)+"[game]") {
			t.Errorf("share %.2f: the panels do not sum to %d: %s", share, VerticalHeight, g)
		}
	}
}

// The centre crop pans: Focus 0 keeps the left edge of the frame, 1 the right.
func TestTheCentreCropPansWithFocus(t *testing.T) {
	for focus, want := range map[float64]string{0: "x=(iw-ow)*0.000000", 0.25: "x=(iw-ow)*0.250000", 1: "x=(iw-ow)*1.000000"} {
		g := Vertical{Layout: LayoutCenter, Focus: focus}.pictureGraph()
		if !strings.Contains(g, "crop=w=1080:h=1920:"+want+":y=(ih-oh)/2[base]") {
			t.Errorf("focus %.2f: %s", focus, g)
		}
	}
	if g := (Vertical{Layout: LayoutBlur}).pictureGraph(); !strings.Contains(g, "boxblur") ||
		!strings.Contains(g, "force_original_aspect_ratio=decrease") || !strings.Contains(g, "overlay=x=(W-w)/2:y=(H-h)/2[base]") {
		t.Errorf("the blur layout fits the frame over a blurred copy of itself: %s", g)
	}
}

// A vertical export re-encodes every frame, so it does not snap. A fast mode
// asked for still starts exactly on the requested frame -- a snapped in-point
// would move the picture and leave the captions where they were.
//
// MUTATION: PlanCut snaps a vertical plan as well. Observed: FAIL, In moved to
// the 8s keyframe.
func TestAVerticalExportStartsExactlyOnTheRequestedFrame(t *testing.T) {
	r := verticalReq(9*time.Second, 30*time.Second, Vertical{Layout: LayoutCenter, Focus: 0.5})
	r.Mode = ModeFast
	p := mustPlan(t, oneHourTimeline(t), gop(1000, 4*time.Second), r)
	if p.In != 9*time.Second || p.InDrift != 0 || !p.DriftKnown {
		t.Fatalf("In=%s drift=%s known=%v, want exactly 9s, no drift", p.In, p.InDrift, p.DriftKnown)
	}
	if !p.ReEncodes() || p.LosslessFraction() != 0 {
		t.Errorf("a vertical export encodes every frame: head %s of %s", p.HeadDuration, p.Duration())
	}
	c := onlyCommand(t, p)
	assertArgPair(t, c.Args, "-ss", "9.000000")
	assertArgPair(t, c.Args, "-t", "21.000000")
	assertArgPair(t, c.Args, "-c:v", "libx264")
	assertArgPair(t, c.Args, "-pix_fmt", "yuv420p")
	assertArgPair(t, c.Args, "-c:a", "aac")
	assertArgPair(t, c.Args, "-movflags", "+faststart")
	assertArgPair(t, c.Args, "-/filter_complex", filepath.Join(testWorkDir, "vertical.graph"))
	if !strings.Contains(p.Describe(), "Vertical 9:16 short") {
		t.Errorf("Describe should say what is being made: %q", p.Describe())
	}
}

func assertArgPair(t *testing.T, args []string, flag, want string) {
	t.Helper()
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			if args[i+1] != want {
				t.Errorf("%s %s, want %s", flag, args[i+1], want)
			}
			return
		}
	}
	t.Errorf("no %s in %v", flag, args)
}

// THE 60 SECOND GUARD WARNS AND DOES NOT REFUSE. 60.000s is a short and says
// nothing; a millisecond more is still planned, and is told why it may not be
// published as one.
//
// MUTATION: the guard compares >= instead of >. Observed: FAIL at 60.000s.
// MUTATION: the guard returns an error. Observed: FAIL, PlanCut refuses 60.001s.
// MUTATION: the warning is removed. Observed: FAIL, nothing said at 60.001s.
func TestTheSixtySecondGuardWarnsWithoutBlocking(t *testing.T) {
	v := Vertical{Layout: LayoutCenter, Focus: 0.5}
	at := mustPlan(t, oneHourTimeline(t), gop(1000, 2*time.Second), verticalReq(0, 60*time.Second, v))
	for _, w := range at.Warnings {
		if strings.Contains(w, "Shorts") {
			t.Errorf("exactly 60s is a short; warned anyway: %q", w)
		}
	}
	over, err := PlanCut(oneHourTimeline(t), gop(1000, 2*time.Second), verticalReq(0, 60*time.Second+time.Millisecond, v))
	if err != nil {
		t.Fatalf("a 60.001s short must still plan, got %v", err)
	}
	found := false
	for _, w := range over.Warnings {
		if strings.Contains(w, "Shorts") && strings.Contains(w, "1m0s") {
			found = true
		}
	}
	if !found {
		t.Errorf("60.001s said nothing about the short limit: %v", over.Warnings)
	}
	// A plain cut of any length says nothing about shorts.
	plain := mustPlan(t, oneHourTimeline(t), gop(1000, 2*time.Second), req(0, 5*time.Minute))
	for _, w := range plain.Warnings {
		if strings.Contains(w, "Shorts") {
			t.Errorf("an ordinary clip was warned about shorts: %q", w)
		}
	}
}

func captioned(cues ...Cue) *Captions {
	return &Captions{Cues: cues, FontFile: filepath.Join(testClipDir, "Inter-Bold.ttf"), DrawText: true}
}

// CAPTIONS ARE TIMED ON THE CLIP'S CLOCK. A cue spoken 12s into the recording
// is on screen 2s into a clip that starts at 10s; a cue that straddles the
// in-point starts at zero and one that runs past the out-point stops at the
// end; a cue wholly outside the clip is not drawn.
//
// MUTATION: screens forgets to subtract the in-point. Observed: FAIL, the cue
// is enabled at 12.000.
// MUTATION: screens drops the clamp to the clip's end. Observed: FAIL, 31.000.
func TestCaptionsAreTimedOnTheClipsClock(t *testing.T) {
	v := Vertical{Layout: LayoutCenter, Focus: 0.5, Captions: captioned(
		Cue{StartMS: 8000, EndMS: 11000, Text: "straddles the in point"},
		Cue{StartMS: 12000, EndMS: 14500, Text: "spoken at twelve"},
		Cue{StartMS: 29000, EndMS: 41000, Text: "runs past the out point"},
		Cue{StartMS: 50000, EndMS: 52000, Text: "never in the clip"},
	)}
	c := onlyCommand(t, mustPlan(t, oneHourTimeline(t), gop(1000, 2*time.Second),
		verticalReq(10*time.Second, 30*time.Second, v)))
	g := graphOf(t, c)
	for _, want := range []string{
		"enable='gte(t,0.000)*lt(t,1.000)'",
		"enable='gte(t,2.000)*lt(t,4.500)'",
		"enable='gte(t,19.000)*lt(t,20.000)'",
	} {
		if !strings.Contains(g, want) {
			t.Errorf("missing %s in\n%s", want, g)
		}
	}
	if n := strings.Count(g, "enable='"); n != len(captionTexts(c)) {
		t.Errorf("%d drawtext filters for %d caption lines", n, len(captionTexts(c)))
	}
	if strings.Count(g, "enable='gte(t,50") != 0 || strings.Count(g, "enable='gte(t,40") != 0 {
		t.Errorf("a cue outside the clip was drawn: %s", g)
	}
	texts := captionTexts(c)
	if strings.Join(texts, " ") != "straddles the in point spoken at twelve runs past the out point" {
		t.Errorf("caption files hold %q", texts)
	}
}

func captionTexts(c Command) []string {
	var out []string
	for _, f := range c.Files {
		if strings.Contains(filepath.Base(f.Path), "caption-") {
			out = append(out, f.Content)
		}
	}
	return out
}

// A clip that spans two files is read through the concat demuxer, which cannot
// seek; the seek moves to the output side, the filters see every frame from the
// first file's start, and the captions must be shifted by exactly the span
// FFmpeg discards. Without the shift every caption appears early by it.
//
// MUTATION: the shift is dropped. Observed: FAIL, enable reads 2.000 not 3302.000.
func TestCaptionsAcrossASegmentBoundaryFollowTheOutputSeek(t *testing.T) {
	tl, err := NewTimeline([]Segment{
		{Path: "/rec/a.mkv", Start: 0, Duration: time.Hour},
		{Path: "/rec/b.mkv", Start: time.Hour, Duration: time.Hour},
	})
	if err != nil {
		t.Fatal(err)
	}
	in := time.Hour - 20*time.Second
	v := Vertical{Layout: LayoutCenter, Focus: 0.5, Captions: captioned(
		Cue{StartMS: (in + 2*time.Second).Milliseconds(), EndMS: (in + 4*time.Second).Milliseconds(), Text: "across the seam"},
	)}
	p := mustPlan(t, tl, gop(4000, 2*time.Second), verticalReq(in, in+40*time.Second, v))
	if !p.Concat {
		t.Fatal("the clip spans both files")
	}
	c := onlyCommand(t, p)
	g := graphOf(t, c)
	if !strings.Contains(g, "enable='gte(t,3582.000)*lt(t,3584.000)'") {
		t.Errorf("caption not shifted by the output seek:\n%s", g)
	}
	// The seek itself is after -i, as every concat cut's is.
	i := indexOf(c.Args, "-i")
	s := indexOf(c.Args, "-ss")
	if i < 0 || s < i {
		t.Errorf("a concat input is seeked on the output side: %v", c.Args)
	}
}

func indexOf(args []string, v string) int {
	for i, a := range args {
		if a == v {
			return i
		}
	}
	return -1
}

// Timed words, when there are any, are shown MaxWords at a time on the
// transcriber's own timing.
func TestTimedWordsAreShownAFewAtATimeOnTheirOwnTiming(t *testing.T) {
	words := []Word{
		{StartMS: 1000, EndMS: 1300, Text: "one"}, {StartMS: 1300, EndMS: 1600, Text: "two"},
		{StartMS: 1600, EndMS: 2000, Text: "three"}, {StartMS: 2400, EndMS: 2900, Text: "four"},
	}
	c := Captions{Cues: []Cue{{StartMS: 1000, EndMS: 2900, Text: "one two three four", Words: words}}}
	style := CaptionStyle{MaxWords: 3}.Normalized()
	got := screens(c, style, func(t time.Duration) time.Duration { return t }, 0, time.Minute)
	if len(got) != 2 || got[0].From != time.Second || got[0].To != 2*time.Second ||
		got[0].Lines[0] != "one two three" || got[1].From != 2400*time.Millisecond || got[1].Lines[0] != "four" {
		t.Errorf("word chunks: %+v", got)
	}
}

// A cue that wraps past MaxLines becomes consecutive screens, splitting its span
// by how much text each holds and never leaving the cue's own span.
func TestALongCueBecomesPagesInsideItsOwnSpan(t *testing.T) {
	style := CaptionStyle{MaxLines: 1, SizePct: 6}.Normalized()
	long := strings.Repeat("word ", 30)
	got := screens(Captions{Cues: []Cue{{StartMS: 0, EndMS: 9000, Text: long}}}, style, func(t time.Duration) time.Duration { return t }, 0, time.Minute)
	if len(got) < 2 {
		t.Fatalf("expected several pages, got %+v", got)
	}
	if got[0].From != 0 || got[len(got)-1].To != 9*time.Second {
		t.Errorf("pages leave the cue's span: %+v", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i].From != got[i-1].To {
			t.Errorf("pages are not contiguous at %d: %+v", i, got)
		}
	}
}

// Without drawtext the short is still made, without captions, and says so.
//
// MUTATION: DrawText ignored. Observed: FAIL, drawtext in the graph.
func TestWithoutDrawtextTheShortIsMadeWithoutCaptionsAndSaysSo(t *testing.T) {
	caps := captioned(Cue{StartMS: 0, EndMS: 2000, Text: "hello"})
	caps.DrawText = false
	p := mustPlan(t, oneHourTimeline(t), gop(1000, 2*time.Second),
		verticalReq(0, 10*time.Second, Vertical{Layout: LayoutBlur, Captions: caps}))
	if g := graphOf(t, onlyCommand(t, p)); strings.Contains(g, "drawtext") {
		t.Errorf("drew text with no drawtext: %s", g)
	}
	said := false
	for _, w := range p.Warnings {
		said = said || strings.Contains(w, "captions were not burned")
	}
	if !said {
		t.Errorf("no warning that the captions were dropped: %v", p.Warnings)
	}
}

func TestCaptionTextIsCleanedAndStyled(t *testing.T) {
	got := styleText("  a\ue000b\ue001\tc\n d  ", CaptionStyle{Uppercase: true})
	if got != "AB C D" {
		t.Errorf("styleText = %q", got)
	}
	if lines := wrap("aa bb cc dddddddddd", 5); strings.Join(lines, "|") != "aa bb|cc|ddddd|ddddd" {
		t.Errorf("wrap = %q", lines)
	}
}

// Each caption line carries the operator's style, the font the server chose,
// and expansion=none, so a '%' in speech is drawn rather than interpreted.
func TestCaptionFiltersCarryTheStyle(t *testing.T) {
	style := CaptionStyle{Color: "#FFCC00", Outline: 4, OutlineColor: "#101010", Box: true, BoxOpacity: 0.4}.Normalized()
	f, files := captionFilters([]captionScreen{{From: 0, To: time.Second, Lines: []string{"100% real", "two"}}},
		style, "/fonts/Inter-Bold.ttf", testWorkDir)
	if len(f) != 2 || len(files) != 2 || files[0].Content != "100% real" {
		t.Fatalf("one filter and one file per line: %v %v", f, files)
	}
	for _, want := range []string{"fontcolor=0xffcc00", "borderw=4:bordercolor=0x101010", "box=1:boxcolor=0x000000@0.40",
		"expansion=none", "x=(w-text_w)/2", "/fonts/Inter-Bold.ttf"} {
		if !strings.Contains(f[0], want) {
			t.Errorf("%q missing from %s", want, f[0])
		}
	}
}

func TestVerticalRequestsAreValidated(t *testing.T) {
	good := Vertical{Layout: LayoutStacked, Face: Rect{X: 0.7, Y: 0.05, W: 0.25, H: 0.3}}
	cases := map[string]Request{
		"not mp4":       verticalReq(0, 10*time.Second, good, func(r *Request) { r.OutPath = testOutPath }),
		"routing mix":   verticalReq(0, 10*time.Second, good, func(r *Request) { r.Audio = AudioSelection{Mode: AudioMix} }),
		"unknown":       verticalReq(0, 10*time.Second, Vertical{Layout: "diagonal"}),
		"no face box":   verticalReq(0, 10*time.Second, Vertical{Layout: LayoutStacked}),
		"off the frame": verticalReq(0, 10*time.Second, Vertical{Layout: LayoutStacked, Face: Rect{X: 0.9, Y: 0, W: 0.2, H: 0.2}}),
		"tiny box":      verticalReq(0, 10*time.Second, Vertical{Layout: LayoutStacked, Face: Rect{W: 0.001, H: 0.5}}),
		"bad share":     verticalReq(0, 10*time.Second, Vertical{Layout: LayoutStacked, Face: good.Face, FaceShare: 0.9}),
		"bad focus":     verticalReq(0, 10*time.Second, Vertical{Layout: LayoutCenter, Focus: 1.5}),
		"bad colour": verticalReq(0, 10*time.Second, Vertical{Layout: LayoutBlur,
			Captions: &Captions{Style: CaptionStyle{Color: "red"}}}),
		"font path": verticalReq(0, 10*time.Second, Vertical{Layout: LayoutBlur,
			Captions: &Captions{Style: CaptionStyle{Font: "../etc/passwd"}}}),
	}
	for name, r := range cases {
		if err := r.Validate(); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%s: Validate = %v, want ErrInvalidRequest", name, err)
		}
	}
	if err := verticalReq(0, 10*time.Second, good).Validate(); err != nil {
		t.Errorf("a good stacked request was refused: %v", err)
	}
}

// A captioned export the server forgot to give a font is refused at command
// time rather than drawn with whatever fontconfig finds, which in the shipping
// image is nothing.
func TestACaptionedExportWithoutAFontIsRefused(t *testing.T) {
	caps := captioned(Cue{StartMS: 0, EndMS: 1000, Text: "x"})
	caps.FontFile = ""
	p := mustPlan(t, oneHourTimeline(t), gop(1000, 2*time.Second),
		verticalReq(0, 10*time.Second, Vertical{Layout: LayoutCenter, Focus: 0.5, Captions: caps}))
	if _, err := p.Commands(testWorkDir); !errors.Is(err, ErrNoCaptionFont) {
		t.Errorf("Commands = %v, want ErrNoCaptionFont", err)
	}
	if _, err := p.Commands(""); !errors.Is(err, ErrNoWorkDir) {
		t.Errorf("a vertical export needs a work directory: %v", err)
	}
}

// One stereo track: the first for "all", the one chosen, or a mix of several.
func TestAShortCarriesOneAudioTrack(t *testing.T) {
	v := Vertical{Layout: LayoutCenter, Focus: 0.5}
	for name, tc := range map[string]struct {
		audio AudioSelection
		want  string
	}{
		"all":     {AudioSelection{Mode: AudioAll}, "0:a:0?"},
		"one":     {AudioSelection{Mode: AudioTracks, Tracks: []int{2}}, "0:a:2?"},
		"several": {AudioSelection{Mode: AudioTracks, Tracks: []int{1, 3}}, "[aout]"},
	} {
		c := onlyCommand(t, mustPlan(t, oneHourTimeline(t), gop(1000, 2*time.Second),
			verticalReq(0, 10*time.Second, v, func(r *Request) { r.Audio = tc.audio })))
		maps := mapValues(c.Args)
		if len(maps) != 2 || maps[1] != tc.want {
			t.Errorf("%s: maps %v, want [vout] then %s", name, maps, tc.want)
		}
		if name == "several" && !strings.Contains(graphOf(t, c), "[0:a:1][0:a:3]amix=inputs=2") {
			t.Errorf("several tracks are mixed into one: %s", graphOf(t, c))
		}
	}
}

func TestAVerticalEncoderIsAlwaysH264(t *testing.T) {
	for in, want := range map[string]string{"libx264": "libx264", "h264_nvenc": "h264_nvenc", "libx265": "libx264", "": "libx264"} {
		if got := verticalEncoder(in); got != want {
			t.Errorf("verticalEncoder(%q) = %q, want %q", in, got, want)
		}
	}
}

// A RECORDING WITH A GAP. Two files 0..12 s and 20..32 s on the timeline: the
// concat demuxer plays them back to back, so timeline 21 s is 13 s into the
// joined input, not 21. A short of 10..26 s is therefore 8 s of picture, and
// a caption spoken at 21..22 s is on the filters' clock at 13..14 s.
//
// MUTATION: captions placed at timeline time. Observed: FAIL, gte(t,21.000).
// MUTATION: -t is the requested span. Observed: FAIL, 16.000000.
func TestCaptionsAndLengthFollowPlaybackAcrossAGap(t *testing.T) {
	tl, err := NewTimeline([]Segment{
		{Path: "/rec/a.mkv", Start: 0, Duration: 12 * time.Second},
		{Path: "/rec/b.mkv", Start: 20 * time.Second, Duration: 12 * time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	v := Vertical{Layout: LayoutCenter, Focus: 0.5, Captions: captioned(
		Cue{StartMS: 11_000, EndMS: 11_500, Text: "before the gap"},
		Cue{StartMS: 21_000, EndMS: 22_000, Text: "after the gap"},
		Cue{StartMS: 15_000, EndMS: 16_000, Text: "inside the gap"},
	)}
	p := mustPlan(t, tl, gop(100, 2*time.Second), verticalReq(10*time.Second, 26*time.Second, v))
	c := onlyCommand(t, p)
	g := graphOf(t, c)
	for _, want := range []string{"enable='gte(t,11.000)*lt(t,11.500)'", "enable='gte(t,13.000)*lt(t,14.000)'"} {
		if !strings.Contains(g, want) {
			t.Errorf("missing %s in\n%s", want, g)
		}
	}
	if strings.Contains(g, "inside the gap") || len(captionTexts(c)) != 2 {
		t.Errorf("a cue spoken during the gap has no picture to sit on: %q", captionTexts(c))
	}
	assertArgPair(t, c.Args, "-t", "8.000000")
}

// UNSPACED SCRIPTS WRAP. A Chinese or Japanese line has no spaces, so wrapping on
// spaces left it one line thousands of pixels wide. A word longer than a line is
// broken between characters instead.
//
// MUTATION: wrap keeps an over-long word whole. Observed: FAIL, one 40-rune line.
func TestALineWithoutSpacesStillWraps(t *testing.T) {
	cjk := strings.Repeat("字", 40)
	lines := wrap(cjk, 10)
	if len(lines) != 4 {
		t.Fatalf("wrap(40 runes, 10) = %d lines %q", len(lines), lines)
	}
	for _, l := range lines {
		if n := len([]rune(l)); n > 10 {
			t.Errorf("a line of %d runes exceeds 10", n)
		}
	}
	if got := wrap("short words stay whole", 10); strings.Join(got, "|") != "short|words stay|whole" {
		t.Errorf("wrap of spaced text = %q", got)
	}
}

// The bundled Inter has Latin, Greek and Cyrillic, and nothing for CJK, Arabic,
// Hebrew or emoji: those draw as boxes. The plan says so, naming a character,
// so the operator knows to add a font that has them -- and says nothing when
// the operator already chose a font of their own.
//
// MUTATION: the glyph check is removed. Observed: FAIL, no warning.
func TestThePlanWarnsWhenTheBundledFontCannotDrawTheCaptions(t *testing.T) {
	plan := func(font, text string) Plan {
		c := captioned(Cue{StartMS: 1000, EndMS: 3000, Text: text})
		c.Style.Font = font
		return mustPlan(t, oneHourTimeline(t), gop(1000, 2*time.Second),
			verticalReq(0, 10*time.Second, Vertical{Layout: LayoutBlur, Captions: c}))
	}
	warned := func(p Plan) bool {
		for _, w := range p.Warnings {
			if strings.Contains(w, "boxes") {
				return true
			}
		}
		return false
	}
	if !warned(plan("", "你好 世界")) || !warned(plan("Inter-Regular.ttf", "مرحبا")) {
		t.Error("CJK or Arabic under the bundled font said nothing")
	}
	if warned(plan("", "Hello, Привет, Γειά σου — 100%")) {
		t.Error("Latin, Cyrillic and Greek are in Inter and must not warn")
	}
	if warned(plan("NotoSansCJK-Regular.otf", "你好")) {
		t.Error("an operator's own font is not second-guessed")
	}
}

// CONCURRENT CUES DO NOT DRAW OVER EACH OTHER. Two tracks speaking at once give
// two captions with overlapping times; the second is stacked above the first
// instead of on the same line. Captions that do not overlap share the line.
//
// MUTATION: every screen at lane zero. Observed: FAIL, equal y.
func TestOverlappingCaptionsAreStackedNotDrawnOverEachOther(t *testing.T) {
	style := CaptionStyle{}.Normalized()
	ys := func(scr []captionScreen) []string {
		f, _ := captionFilters(scr, style, "/fonts/f.ttf", testWorkDir)
		var out []string
		for _, s := range f {
			i := strings.Index(s, ":y=")
			out = append(out, s[i:i+strings.IndexByte(s[i+1:], ':')+1])
		}
		return out
	}
	both := ys([]captionScreen{{From: 0, To: 2 * time.Second, Lines: []string{"one"}},
		{From: time.Second, To: 3 * time.Second, Lines: []string{"two"}}})
	if len(both) != 2 || both[0] == both[1] {
		t.Errorf("overlapping captions share a line: %q", both)
	}
	apart := ys([]captionScreen{{From: 0, To: time.Second, Lines: []string{"one"}},
		{From: time.Second, To: 2 * time.Second, Lines: []string{"two"}}})
	if len(apart) != 2 || apart[0] != apart[1] {
		t.Errorf("consecutive captions should share the line: %q", apart)
	}
}

// THE GRAPH IS BOUNDED. Thousands of cues used to become thousands of drawtext
// filters and gigabytes of memory; past maxCaptionLines the rest are dropped
// and the plan says how many.
//
// MUTATION: no cap. Observed: FAIL, 1500 drawtext filters.
func TestTheCaptionGraphIsBounded(t *testing.T) {
	var cues []Cue
	for i := 0; i < 1500; i++ {
		cues = append(cues, Cue{StartMS: int64(i * 200), EndMS: int64(i*200 + 150), Text: "w"})
	}
	p := mustPlan(t, oneHourTimeline(t), gop(1000, 2*time.Second),
		verticalReq(0, 5*time.Minute, Vertical{Layout: LayoutBlur, Captions: captioned(cues...)}))
	if n := strings.Count(graphOf(t, onlyCommand(t, p)), "drawtext="); n != maxCaptionLines {
		t.Errorf("%d drawtext filters, want the %d cap", n, maxCaptionLines)
	}
	said := false
	for _, w := range p.Warnings {
		said = said || strings.Contains(w, "were not drawn")
	}
	if !said {
		t.Errorf("dropped captions without saying so: %v", p.Warnings)
	}
}

// An empty style is the default style, outline included; -1 is "no outline".
//
// MUTATION: Normalized leaves Outline at zero. Observed: FAIL, outline 0.
func TestAnEmptyStyleHasTheDefaultOutline(t *testing.T) {
	if got := (CaptionStyle{}).Normalized().Outline; got != DefaultCaptionStyle().Outline {
		t.Errorf("empty style outline %d, want %d", got, DefaultCaptionStyle().Outline)
	}
	none := CaptionStyle{Outline: -1}.Normalized()
	if err := none.Validate(); err != nil {
		t.Fatalf("-1 is a valid outline: %v", err)
	}
	f, _ := captionFilters([]captionScreen{{From: 0, To: time.Second, Lines: []string{"x"}}}, none, "/f.ttf", testWorkDir)
	if strings.Contains(f[0], "borderw") {
		t.Errorf("outline -1 still drew one: %s", f[0])
	}
}
