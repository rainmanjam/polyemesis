package clipper

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A fast cut is one command and it copies. If a -c:v that is not "copy" ever
// appears here, the feature's whole claim — lossless, seconds not minutes — is
// gone and no other test would notice.
func TestFastCutIsOneStreamCopy(t *testing.T) {
	p := planFast(t, 5*time.Second, 15*time.Second)

	cmds, err := p.Commands("")
	if err != nil {
		t.Fatalf("Commands: %v", err)
	}
	if len(cmds) != 1 {
		t.Fatalf("got %d commands, want 1: %v", len(cmds), names(cmds))
	}
	want := []string{
		"-hide_banner", "-nostdin", "-loglevel", "warning", "-y",
		// The snapped in-point, not the requested one.
		"-ss", "4.000000",
		"-i", "/rec/seg0.mkv",
		"-t", "11.000000",
		"-map", "0:v:0?", "-map", "0:a?",
		"-c:v", "copy", "-c:a", "copy",
		"-avoid_negative_ts", "make_zero",
		"-f", "matroska",
		testOutPath,
	}
	assertArgs(t, cmds[0].Args, want)
	if cmds[0].Output != testOutPath {
		t.Errorf("output = %s", cmds[0].Output)
	}
	if len(cmds[0].Files) != 0 {
		t.Errorf("a single-file cut wrote a sidecar: %v", cmds[0].Files)
	}
}

// The precise mode's promise is that only the leading partial GOP costs an
// encode. Three commands, and exactly one of them names an encoder.
func TestPreciseCutIsHeadThenTailThenJoin(t *testing.T) {
	p := planPrecise(t, 5*time.Second, 15*time.Second)

	cmds, err := p.Commands(testWorkDir)
	if err != nil {
		t.Fatalf("Commands: %v", err)
	}
	if got := names(cmds); !equalStrings(got, []string{"head", "tail", "join"}) {
		t.Fatalf("commands = %v, want head tail join", got)
	}

	assertArgs(t, cmds[0].Args, []string{
		"-hide_banner", "-nostdin", "-loglevel", "warning", "-y",
		// Input seek to the keyframe a decoder has to start on...
		"-ss", "4.000000",
		"-i", "/rec/seg0.mkv",
		// ...then an output seek that drops the frames before the in-point.
		"-ss", "1.000000",
		"-t", "1.000000",
		"-map", "0:v:0?", "-map", "0:a?",
		"-c:v", "libx264", "-bf", "0", "-crf", "16", "-preset", "veryfast",
		"-c:a", "copy",
		"-avoid_negative_ts", "make_zero",
		"-f", "matroska",
		filepath.Join(testWorkDir, "head.mkv"),
	})
	assertArgs(t, cmds[1].Args, []string{
		"-hide_banner", "-nostdin", "-loglevel", "warning", "-y",
		"-ss", "6.000000",
		"-i", "/rec/seg0.mkv",
		"-t", "9.000000",
		"-map", "0:v:0?", "-map", "0:a?",
		"-c:v", "copy", "-c:a", "copy",
		"-avoid_negative_ts", "make_zero",
		"-f", "matroska",
		filepath.Join(testWorkDir, "tail.mkv"),
	})
	assertArgs(t, cmds[2].Args, []string{
		"-hide_banner", "-nostdin", "-loglevel", "warning", "-y",
		"-f", "concat", "-safe", "0", "-i", filepath.Join(testWorkDir, "join.txt"),
		"-map", "0", "-c", "copy",
		"-avoid_negative_ts", "make_zero",
		"-f", "matroska",
		testOutPath,
	})

	if len(cmds[2].Files) != 1 {
		t.Fatalf("the join has %d sidecars, want 1", len(cmds[2].Files))
	}
	// Head first. The other order produces a clip that plays the end before the
	// beginning, which is the sort of bug that ships.
	// Built with Join rather than written out: on Windows these are
	// \work\head.mkv, and a backslash inside the concat demuxer's single
	// quotes is LITERAL (verified against the real demuxer), so that list is
	// correct there -- it just is not this string.
	wantList := fmt.Sprintf("file '%s'\nfile '%s'\n",
		filepath.Join(testWorkDir, "head.mkv"), filepath.Join(testWorkDir, "tail.mkv"))
	if cmds[2].Files[0].Content != wantList {
		t.Errorf("join list = %q, want %q", cmds[2].Files[0].Content, wantList)
	}
}

// A clip shorter than one GOP has no keyframe inside it to resume copying at,
// so there is nothing to join and the encode writes the final file directly.
func TestAPreciseCutInsideOneGOPIsASingleEncode(t *testing.T) {
	tl := oneHourTimeline(t)
	p, err := PlanCut(tl, gop(10, 30*time.Second), req(35*time.Second, 45*time.Second, precise))
	if err != nil {
		t.Fatalf("PlanCut: %v", err)
	}
	cmds, err := p.Commands(testWorkDir)
	if err != nil {
		t.Fatalf("Commands: %v", err)
	}
	if got := names(cmds); !equalStrings(got, []string{"head"}) {
		t.Fatalf("commands = %v, want a single head", got)
	}
	if cmds[0].Output != testOutPath {
		t.Errorf("output = %s, want the final path", cmds[0].Output)
	}
}

// A precise cut whose in-point already sits on a keyframe has nothing to
// re-encode, so it collapses to the same single copy a fast cut produces.
func TestAPreciseCutOnAKeyframeCollapsesToACopy(t *testing.T) {
	tl := oneHourTimeline(t)
	p, err := PlanCut(tl, gop(60, 2*time.Second), req(6*time.Second, 15*time.Second, precise))
	if err != nil {
		t.Fatalf("PlanCut: %v", err)
	}
	cmds, err := p.Commands("")
	if err != nil {
		t.Fatalf("Commands: %v", err)
	}
	if got := names(cmds); !equalStrings(got, []string{"cut"}) {
		t.Fatalf("commands = %v, want a single cut", got)
	}
	if strings.Contains(strings.Join(cmds[0].Args, " "), "libx264") {
		t.Errorf("an aligned precise cut still ran an encoder: %v", cmds[0].Args)
	}
}

// The recorder writes hourly files, so a clip across the boundary has to be
// concatenated before it can be cut — and the seek has to be relative to the
// FIRST file in that concat, not to the timeline.
func TestACutSpanningSegmentsFeedsFFmpegAConcatList(t *testing.T) {
	tl := hourlyTimeline(t, 2, 10*time.Second)
	p, err := PlanCut(tl, gop(20, time.Second), req(9*time.Second, 12*time.Second))
	if err != nil {
		t.Fatalf("PlanCut: %v", err)
	}
	cmds, err := p.Commands(testWorkDir)
	if err != nil {
		t.Fatalf("Commands: %v", err)
	}
	assertArgs(t, cmds[0].Args, []string{
		"-hide_banner", "-nostdin", "-loglevel", "warning", "-y",
		// The seek is on the OUTPUT side, because the concat demuxer cannot seek.
		"-f", "concat", "-safe", "0", "-i", filepath.Join(testWorkDir, "sources.txt"),
		"-ss", "9.000000",
		"-t", "3.000000",
		"-map", "0:v:0?", "-map", "0:a?",
		"-c:v", "copy", "-c:a", "copy",
		"-avoid_negative_ts", "make_zero",
		"-f", "matroska",
		testOutPath,
	})
	if len(cmds[0].Files) != 1 {
		t.Fatalf("no concat list was produced")
	}
	want := "file '/seg0.mkv'\nfile '/seg1.mkv'\n"
	if cmds[0].Files[0].Content != want {
		t.Errorf("concat list = %q, want %q", cmds[0].Files[0].Content, want)
	}
}

// A cut that starts in a later segment seeks into THAT file. Using the timeline
// position would seek an hour into a one-hour file and produce nothing.
func TestSeeksAreRelativeToTheFirstSourceFile(t *testing.T) {
	tl := hourlyTimeline(t, 3, time.Hour)
	p, err := PlanCut(tl, gop(400, 10*time.Second), req(time.Hour+70*time.Second, time.Hour+80*time.Second))
	if err != nil {
		t.Fatalf("PlanCut: %v", err)
	}
	cmds, err := p.Commands("")
	if err != nil {
		t.Fatalf("Commands: %v", err)
	}
	joined := strings.Join(cmds[0].Args, " ")
	if !strings.Contains(joined, "-ss 70.000000") {
		t.Fatalf("seek is not relative to the file it opens: %s", joined)
	}
}

func TestConcatListQuotesAPathContainingAQuote(t *testing.T) {
	// The demuxer has no backslash escape inside quotes: a path with an
	// apostrophe has to close, escape and reopen or the list parses as
	// something else entirely.
	got := concatList([]string{"/media/Tom's stream/seg0.mkv"})
	want := `file '/media/Tom'\''s stream/seg0.mkv'` + "\n"
	if got != want {
		t.Fatalf("concatList = %q, want %q", got, want)
	}
}

func TestAudioSelectionChoosesTheMapsAndTheCodecs(t *testing.T) {
	tests := []struct {
		name       string
		mut        func(*Request)
		wantMaps   []string
		wantCodec  []string
		wantFilter string
	}{
		{
			name:      "every track, copied",
			mut:       func(*Request) {},
			wantMaps:  []string{"0:v:0?", "0:a?"},
			wantCodec: []string{"-c:a", "copy"},
		},
		{
			name:      "just the mic, still copied",
			mut:       func(r *Request) { r.Audio = AudioSelection{Mode: AudioTracks, Tracks: []int{2}} },
			wantMaps:  []string{"0:v:0?", "0:a:2?"},
			wantCodec: []string{"-c:a", "copy"},
		},
		{
			name:      "two named tracks keep their order",
			mut:       func(r *Request) { r.Audio = AudioSelection{Mode: AudioTracks, Tracks: []int{3, 0}} },
			wantMaps:  []string{"0:v:0?", "0:a:3?", "0:a:0?"},
			wantCodec: []string{"-c:a", "copy"},
		},
		{
			name:       "a mix maps the graph's output and encodes it",
			mut:        mixOfTracks(0, 1),
			wantMaps:   []string{"0:v:0?", "[aout]"},
			wantCodec:  []string{"-c:a", "flac"},
			wantFilter: "[0:a:0]",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tl := oneHourTimeline(t)
			p, err := PlanCut(tl, gop(60, 2*time.Second), req(0, 10*time.Second, tc.mut))
			if err != nil {
				t.Fatalf("PlanCut: %v", err)
			}
			cmds, err := p.Commands("")
			if err != nil {
				t.Fatalf("Commands: %v", err)
			}
			if got := mapValues(cmds[0].Args); !equalStrings(got, tc.wantMaps) {
				t.Errorf("maps = %v, want %v", got, tc.wantMaps)
			}
			joined := strings.Join(cmds[0].Args, " ")
			if !strings.Contains(joined, strings.Join(tc.wantCodec, " ")) {
				t.Errorf("missing %v in %s", tc.wantCodec, joined)
			}
			// Video is copied whatever happens to the audio. Mixing must never
			// cost a video encode.
			if !strings.Contains(joined, "-c:v copy") {
				t.Errorf("the video is not copied: %s", joined)
			}
			if tc.wantFilter != "" && !strings.Contains(joined, tc.wantFilter) {
				t.Errorf("missing the filter graph in %s", joined)
			}
			if tc.wantFilter == "" && strings.Contains(joined, "-filter_complex") {
				t.Errorf("a graph was compiled for a selection that needs none: %s", joined)
			}
		})
	}
}

func TestAMixCarriesItsBitrateOnlyWhenTheCodecIsLossy(t *testing.T) {
	tests := []struct {
		name      string
		outPath   string
		kbps      int
		wantFlag  bool
		wantCodec string
	}{
		{name: "flac ignores a bitrate", outPath: testOutPath, kbps: 192, wantCodec: "flac"},
		{name: "aac takes one", outPath: filepath.Join(testClipDir, "out.mp4"), kbps: 192, wantFlag: true, wantCodec: "aac"},
		{name: "aac with no bitrate keeps the encoder default", outPath: filepath.Join(testClipDir, "out.mp4"), wantCodec: "aac"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tl := oneHourTimeline(t)
			p, err := PlanCut(tl, gop(60, 2*time.Second), req(0, 10*time.Second, mixOfTracks(0, 1),
				func(r *Request) { r.OutPath = tc.outPath; r.Audio.Kbps = tc.kbps }))
			if err != nil {
				t.Fatalf("PlanCut: %v", err)
			}
			cmds, err := p.Commands("")
			if err != nil {
				t.Fatalf("Commands: %v", err)
			}
			joined := strings.Join(cmds[0].Args, " ")
			if !strings.Contains(joined, "-c:a "+tc.wantCodec) {
				t.Errorf("codec is not %s: %s", tc.wantCodec, joined)
			}
			if got := strings.Contains(joined, "-b:a 192k"); got != tc.wantFlag {
				t.Errorf("bitrate flag present = %v, want %v: %s", got, tc.wantFlag, joined)
			}
		})
	}
}

func TestTheContainerFollowsTheOutputExtension(t *testing.T) {
	tests := []struct {
		path string
		want []string
	}{
		{path: testOutPath, want: []string{"-f", "matroska"}},
		{path: filepath.Join(testClipDir, "out.mp4"), want: []string{"-movflags", "+faststart", "-f", "mp4"}},
		{path: filepath.Join(testClipDir, "out.ts"), want: []string{"-f", "mpegts"}},
		{path: filepath.Join(testClipDir, "out.unknown"), want: []string{"-f", "matroska"}},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			tl := oneHourTimeline(t)
			p, err := PlanCut(tl, gop(60, 2*time.Second), req(0, 10*time.Second, func(r *Request) { r.OutPath = tc.path }))
			if err != nil {
				t.Fatalf("PlanCut: %v", err)
			}
			cmds, err := p.Commands("")
			if err != nil {
				t.Fatalf("Commands: %v", err)
			}
			if joined := strings.Join(cmds[0].Args, " "); !strings.Contains(joined, strings.Join(tc.want, " ")) {
				t.Fatalf("missing %v in %s", tc.want, joined)
			}
		})
	}
}

// The title belongs on the clip the user keeps, not on an intermediate that is
// deleted seconds later.
func TestTheTitleIsWrittenOnlyOntoTheFinalOutput(t *testing.T) {
	tl := oneHourTimeline(t)
	p, err := PlanCut(tl, gop(60, 2*time.Second), req(5*time.Second, 15*time.Second, precise,
		func(r *Request) { r.Title = "The goal" }))
	if err != nil {
		t.Fatalf("PlanCut: %v", err)
	}
	cmds, err := p.Commands(testWorkDir)
	if err != nil {
		t.Fatalf("Commands: %v", err)
	}
	for i, c := range cmds {
		got := strings.Contains(strings.Join(c.Args, " "), "-metadata title=The goal")
		want := i == len(cmds)-1
		if got != want {
			t.Errorf("%s carries the title = %v, want %v", c.Name, got, want)
		}
	}
}

// The head is a fraction of a second of video and the machine's spare capacity
// belongs to the live stream first. -threads is the only lever there is.
func TestTheHeadEncodeHonoursAThreadCap(t *testing.T) {
	tl := oneHourTimeline(t)
	p, err := PlanCut(tl, gop(60, 2*time.Second), req(5*time.Second, 15*time.Second, precise,
		func(r *Request) { r.HeadThreads = 2 }))
	if err != nil {
		t.Fatalf("PlanCut: %v", err)
	}
	cmds, err := p.Commands(testWorkDir)
	if err != nil {
		t.Fatalf("Commands: %v", err)
	}
	if !strings.Contains(strings.Join(cmds[0].Args, " "), "-threads 2") {
		t.Errorf("the head encode is uncapped: %v", cmds[0].Args)
	}
	if strings.Contains(strings.Join(cmds[1].Args, " "), "-threads") {
		t.Errorf("the copied tail was given a thread cap it has no use for: %v", cmds[1].Args)
	}
}

func TestHeadQualityArgsOnlySendCRFToEncodersThatTakeIt(t *testing.T) {
	tests := []struct {
		encoder string
		want    []string
	}{
		{encoder: "libx264", want: []string{"-bf", "0", "-crf", "18", "-preset", "veryfast"}},
		{encoder: "libx265", want: []string{"-bf", "0", "-crf", "18", "-preset", "veryfast"}},
		{encoder: "h264_nvenc", want: []string{"-bf", "0"}},
		{encoder: "h264_videotoolbox", want: []string{"-bf", "0"}},
	}
	for _, tc := range tests {
		t.Run(tc.encoder, func(t *testing.T) {
			got := headQualityArgs(tc.encoder, 18, 0)
			if !equalStrings(got, tc.want) {
				t.Fatalf("headQualityArgs(%s) = %v, want %v", tc.encoder, got, tc.want)
			}
		})
	}
}

func TestCommandsRefuseWhatTheyCannotWrite(t *testing.T) {
	base := planPrecise(t, 5*time.Second, 15*time.Second)

	tests := []struct {
		name    string
		mut     func(*Plan)
		workDir string
		wantErr error
	}{
		{
			name:    "a plan with no sources",
			mut:     func(p *Plan) { p.Sources = nil },
			workDir: testWorkDir,
			wantErr: ErrNoSegments,
		},
		{
			name:    "an empty range",
			mut:     func(p *Plan) { p.Out = p.In },
			workDir: testWorkDir,
			wantErr: ErrEmptyRange,
		},
		{
			name:    "a head-and-tail cut with nowhere to put the intermediates",
			mut:     func(*Plan) {},
			workDir: "",
			wantErr: ErrNoWorkDir,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			tc.mut(&p)
			if _, err := p.Commands(tc.workDir); !errors.Is(err, tc.wantErr) {
				t.Fatalf("Commands: got %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func planFast(t *testing.T, in, out time.Duration) Plan {
	t.Helper()
	p, err := PlanCut(oneHourTimeline(t), gop(60, 2*time.Second), req(in, out))
	if err != nil {
		t.Fatalf("PlanCut: %v", err)
	}
	return p
}

func planPrecise(t *testing.T, in, out time.Duration) Plan {
	t.Helper()
	p, err := PlanCut(oneHourTimeline(t), gop(60, 2*time.Second), req(in, out, precise))
	if err != nil {
		t.Fatalf("PlanCut: %v", err)
	}
	return p
}

func names(cmds []Command) []string {
	out := make([]string, 0, len(cmds))
	for _, c := range cmds {
		out = append(out, c.Name)
	}
	return out
}

// mapValues pulls the -map arguments out of a command line, in order.
func mapValues(args []string) []string {
	var out []string
	for i, a := range args {
		if a == "-map" && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

func assertArgs(t *testing.T, got, want []string) {
	t.Helper()
	if !equalStrings(got, want) {
		t.Fatalf("args mismatch\n got: %s\nwant: %s", strings.Join(got, " "), strings.Join(want, " "))
	}
}
