package highlights

import (
	"math"
	"strings"
	"testing"
)

// steadyChat is a deterministic chat fixture: count messages in every bin of
// binMS across durMS, spread evenly inside the bin.
func steadyChat(durMS, binMS int64, count int) []int64 {
	var out []int64
	for b := int64(0); b < durMS; b += binMS {
		for i := 0; i < count; i++ {
			out = append(out, b+int64(i)*binMS/int64(count+1)+1)
		}
	}
	return out
}

func burst(at, spanMS int64, n int) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = at + int64(i)*spanMS/int64(n)
	}
	return out
}

// A burst of chat over a steady trickle is ONE spike, where the burst is.
func TestChatSpikeIsFoundWhereTheBurstIs(t *testing.T) {
	times := append(steadyChat(600_000, 5000, 1), burst(300_000, 10_000, 40)...)
	got := ChatSpikes(times, 600_000, DefaultChatParams())
	if len(got) != 1 {
		t.Fatalf("got %d spikes, want 1: %+v", len(got), got)
	}
	s := got[0]
	if s.Kind != KindChat || s.StartMS != 300_000 || s.EndMS != 310_000 {
		t.Errorf("spike %+v, want chat 300000..310000", s)
	}
	if s.Strength <= 0 || s.Strength > 1 {
		t.Errorf("strength %v outside (0,1]", s.Strength)
	}
}

// THE THRESHOLD, at its edge. A steady two messages per bin has median 2 and no
// spread, so the spread floor of one message applies: the threshold is
// max(MinMessages=5, 2 + 3*1, 2*2) = 5. A bin of exactly five is a spike; a bin
// of four is not.
//
// MUTATION: count >= threshold becomes count > threshold. Observed: FAIL, the
// bin of five is not a spike.
// MUTATION: the spread floor is removed. Observed: FAIL, every bin above two is
// a spike with zero MAD.
// MUTATION: MinMessages ignored. Observed: FAIL in the quiet-stream case below.
func TestChatSpikeThresholdIsExact(t *testing.T) {
	base := steadyChat(300_000, 5000, 2)
	at5 := append(append([]int64{}, base...), burst(100_000, 5000, 3)...) // 2+3 = 5
	at4 := append(append([]int64{}, base...), burst(100_000, 5000, 2)...) // 2+2 = 4
	if got := ChatSpikes(at5, 300_000, DefaultChatParams()); len(got) != 1 || got[0].StartMS != 100_000 {
		t.Errorf("a bin of 5 over a median of 2 is a spike; got %+v", got)
	}
	if got := ChatSpikes(at4, 300_000, DefaultChatParams()); len(got) != 0 {
		t.Errorf("a bin of 4 over a median of 2 is not a spike; got %+v", got)
	}
	// A bin of 3 over a median of 2 with one message of spread elsewhere must not
	// be a spike either: noise, not a moment.
	noisy := append(steadyChat(300_000, 5000, 2), burst(50_000, 5000, 1)...)
	if got := ChatSpikes(noisy, 300_000, DefaultChatParams()); len(got) != 0 {
		t.Errorf("one extra message is not a spike; got %+v", got)
	}
}

// EACH TERM OF THE THRESHOLD DECIDES SOMETHING. With the defaults the ratio
// usually binds; these fixtures make the other two bind.
//
// A noisy chat -- alternating 0 and 10 -- has median 5 and MAD 5, so the spread
// is 7.413 and the Z term, 5 + 3*7.413 = 27.24, is the threshold. 27 is not a
// spike; 28 is.
//
// With Ratio 1, a steady 4 has no spread at all, and only the one-message
// floor stops 4 + 3*0 = 4 making a bin of 6 a spike: the floored threshold is 7.
//
// MUTATION: Z replaced by 1. Observed: FAIL, 27 spikes.
// MUTATION: the spread floor is removed. Observed: FAIL, 6 spikes.
func TestEachTermOfTheChatThresholdBinds(t *testing.T) {
	var noisy []int64
	for b := int64(0); b < 300_000; b += 5000 {
		if (b/5000)%2 == 1 {
			noisy = append(noisy, burst(b, 5000, 10)...)
		}
	}
	// Bin 21 is an odd bin of 10: raise it to 27, then 28. Thirty zeros and
	// thirty nonzero bins keep the median at 5 and the MAD at 5 either way.
	if got := ChatSpikes(append(append([]int64{}, noisy...), burst(105_000, 5000, 17)...), 300_000, DefaultChatParams()); len(got) != 0 {
		t.Errorf("27 under a Z threshold of 27.24 spiked: %+v", got)
	}
	if got := ChatSpikes(append(append([]int64{}, noisy...), burst(105_000, 5000, 18)...), 300_000, DefaultChatParams()); len(got) != 1 {
		t.Errorf("28 over a Z threshold of 27.24 did not spike: %+v", got)
	}

	// A busy steady chat of 10: the Z term is 13 and the ratio term 20, so the
	// ratio decides -- 19 is ordinary for a chat this size, 20 is not.
	// MUTATION: the ratio term is removed. Observed: FAIL, 19 spikes.
	busy := steadyChat(300_000, 5000, 10)
	if got := ChatSpikes(append(append([]int64{}, busy...), burst(100_000, 5000, 9)...), 300_000, DefaultChatParams()); len(got) != 0 {
		t.Errorf("19 over a steady 10 spiked: %+v", got)
	}
	if got := ChatSpikes(append(append([]int64{}, busy...), burst(100_000, 5000, 10)...), 300_000, DefaultChatParams()); len(got) != 1 {
		t.Errorf("20 over a steady 10 is twice the median and must spike: %+v", got)
	}

	flat := ChatParams{BinMS: 5000, MinMessages: 5, Z: 3, Ratio: 1}
	steady := steadyChat(300_000, 5000, 4)
	if got := ChatSpikes(append(append([]int64{}, steady...), burst(100_000, 5000, 2)...), 300_000, flat); len(got) != 0 {
		t.Errorf("6 over a steady 4 spiked without the spread floor: %+v", got)
	}
	if got := ChatSpikes(append(append([]int64{}, steady...), burst(100_000, 5000, 3)...), 300_000, flat); len(got) != 1 {
		t.Errorf("7 over a steady 4 is the floored threshold and must spike: %+v", got)
	}
}

// A near-silent chat does not spike on three messages: MinMessages is the
// floor below which nothing is a moment, whatever the ratio says.
func TestAQuietChatNeedsAMinimumToSpike(t *testing.T) {
	times := burst(60_000, 5000, 4)
	if got := ChatSpikes(times, 300_000, DefaultChatParams()); len(got) != 0 {
		t.Errorf("four messages in an otherwise empty chat spiked: %+v", got)
	}
	if got := ChatSpikes(nil, 300_000, DefaultChatParams()); len(got) != 0 {
		t.Errorf("no chat at all spiked: %+v", got)
	}
}

// Adjacent spike bins are one moment; messages outside the recording are not
// counted at all.
func TestAdjacentSpikeBinsMergeAndOutsideMessagesAreIgnored(t *testing.T) {
	times := append(steadyChat(300_000, 5000, 1), burst(100_000, 15_000, 60)...)
	times = append(times, -5000, 300_000, 900_000)
	// Thirty messages after the end: counted anywhere, they would be a spike.
	times = append(times, burst(300_000, 4000, 30)...)
	got := ChatSpikes(times, 300_000, DefaultChatParams())
	if len(got) != 1 || got[0].StartMS != 100_000 || got[0].EndMS != 115_000 {
		t.Errorf("three hot bins should be one 100000..115000 spike: %+v", got)
	}
}

const ebur = `frame:0    pts:0       pts_time:0
lavfi.r128.M=-120.691
frame:1    pts:4410    pts_time:0.1
lavfi.r128.M=-30.5
frame:2    pts:8820    pts_time:0.2
lavfi.r128.M=-18.25
garbage line
frame:3    pts:13230   pts_time:0.3
lavfi.r128.M=nan
`

func TestEBUR128OutputParses(t *testing.T) {
	got, err := ParseMomentary(strings.NewReader(ebur), 60_000)
	if err != nil {
		t.Fatal(err)
	}
	want := []Sample{{60_000, -120.691}, {60_100, -30.5}, {60_200, -18.25}}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i].AtMS != want[i].AtMS || math.Abs(got[i].LUFS-want[i].LUFS) > 1e-9 {
			t.Errorf("sample %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	args := LoudnessArgs("/rec/a.mkv")
	j := strings.Join(args, " ")
	for _, w := range []string{"-i /rec/a.mkv", "-map 0:a:0?", "ebur128=metadata=1", "key=lavfi.r128.M", "-f null"} {
		if !strings.Contains(j, w) {
			t.Errorf("loudness command lacks %q: %s", w, j)
		}
	}
}

// HOSTILE TIMESTAMPS. pts_time comes out of a media file, and a corrupt or
// crafted one must not size an allocation. Non-finite, negative and absurd
// values are dropped by the parser; whatever reaches LoudnessPeaks outside the
// recording is ignored, and the bin array is bounded by the recording's
// duration, not by the largest timestamp in the input.
//
// MUTATION: ParseMomentary accepts any finite pts_time. Observed: FAIL, the
// 1e18 line yields a sample.
// MUTATION: LoudnessPeaks sizes its bins from the samples instead of durMS.
// Observed: FAIL (panic: makeslice: len out of range).
// MUTATION: LoudnessPeaks keeps samples past durMS. Observed: FAIL, a peak
// outside the recording.
func TestHostileTimestampsCannotSizeAnAllocation(t *testing.T) {
	hostile := "frame:0 pts:0 pts_time:1e18\nlavfi.r128.M=-10\n" +
		"frame:1 pts:0 pts_time:inf\nlavfi.r128.M=-10\n" +
		"frame:2 pts:0 pts_time:+Inf\nlavfi.r128.M=-10\n" +
		"frame:3 pts:0 pts_time:nan\nlavfi.r128.M=-10\n" +
		"frame:4 pts:0 pts_time:-5\nlavfi.r128.M=-10\n" +
		"frame:5 pts:0 pts_time:9223372036854775807\nlavfi.r128.M=-10\n" +
		"frame:6 pts:0 pts_time:1.5\nlavfi.r128.M=-10\n"
	got, err := ParseMomentary(strings.NewReader(hostile), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].AtMS != 1500 {
		t.Fatalf("only the 1.5s reading is a real timestamp; got %+v", got)
	}

	// Straight into the detector, bypassing the parser: a sample at an absurd
	// time, one just past the end, one negative. None may allocate or fire.
	samples := append(loud(120, -30, 0, 0, -30),
		Sample{AtMS: math.MaxInt64, LUFS: 0},
		Sample{AtMS: 1 << 62, LUFS: 0},
		Sample{AtMS: 120_000, LUFS: 0},
		Sample{AtMS: -1, LUFS: 0})
	if peaks := LoudnessPeaks(samples, 120_000, DefaultLoudParams()); len(peaks) != 0 {
		t.Errorf("samples outside the recording made peaks: %+v", peaks)
	}
	if peaks := LoudnessPeaks(samples, 0, DefaultLoudParams()); len(peaks) != 0 {
		t.Errorf("a recording with no duration has no peaks: %+v", peaks)
	}
}

// loud builds momentary samples, 10 per second: base everywhere, peak over
// [fromS, toS).
func loud(durS int, base float64, fromS, toS int, peak float64) []Sample {
	var out []Sample
	for i := 0; i < durS*10; i++ {
		v := base
		if i >= fromS*10 && i < toS*10 {
			v = peak
		}
		out = append(out, Sample{AtMS: int64(i) * 100, LUFS: v})
	}
	return out
}

// A shout over a steady programme is one peak, where the shout is.
func TestALoudnessPeakIsFoundWhereTheShoutIs(t *testing.T) {
	got := LoudnessPeaks(loud(120, -30, 60, 63, -18), 120_000, DefaultLoudParams())
	if len(got) != 1 || got[0].Kind != KindLoudness || got[0].StartMS != 60_000 || got[0].EndMS != 63_000 {
		t.Fatalf("got %+v, want one loudness peak 60000..63000", got)
	}
}

// THE LOUDNESS THRESHOLD, at its edge: baseline -30 LUFS, delta 8 LU. -22.0 is a
// peak; -22.1 is not. Gated silence (FFmpeg's -120.7) never drags the baseline
// down, or a stream with pauses would make every word a peak.
//
// MUTATION: >= becomes >. Observed: FAIL at -22.0.
// MUTATION: the silence gate is removed. Observed: FAIL, -26 over a half-silent
// stream becomes a peak.
func TestLoudnessThresholdIsExactAndIgnoresSilence(t *testing.T) {
	if got := LoudnessPeaks(loud(120, -30, 60, 62, -22), 120_000, DefaultLoudParams()); len(got) != 1 {
		t.Errorf("-22 over -30 is a peak; got %+v", got)
	}
	if got := LoudnessPeaks(loud(120, -30, 60, 62, -22.1), 120_000, DefaultLoudParams()); len(got) != 0 {
		t.Errorf("-22.1 over -30 is not a peak; got %+v", got)
	}
	half := loud(120, -30, 60, 62, -26)
	for i := 0; i < 600; i++ {
		half[i].LUFS = -120.691
	}
	if got := LoudnessPeaks(half, 120_000, DefaultLoudParams()); len(got) != 0 {
		t.Errorf("silence pulled the baseline down: %+v", got)
	}
	if got := LoudnessPeaks(nil, 120_000, DefaultLoudParams()); len(got) != 0 {
		t.Errorf("no samples, no peaks: %+v", got)
	}
}

var lines = []Line{
	{StartMS: 290_000, EndMS: 296_000, Text: "okay watch this"},
	{StartMS: 297_000, EndMS: 303_000, Text: "NO WAY he did it"},
	{StartMS: 400_000, EndMS: 402_000, Text: "unrelated"},
}

// A chat spike becomes a window that starts BEFORE the reaction -- viewers type
// after the moment -- carries the line spoken in it, and is scored from the
// signal that fired.
func TestASpikeBecomesAWindowAroundTheMoment(t *testing.T) {
	chat := Signal{Kind: KindChat, StartMS: 300_000, EndMS: 305_000, Strength: 0.6}
	got := Build([]Signal{chat}, lines, 600_000, DefaultBuildParams())
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	s := got[0]
	p := DefaultBuildParams()
	if s.StartMS != 300_000-p.ChatLeadMS || s.EndMS != 305_000+p.TailMS {
		t.Errorf("window %d..%d, want %d..%d", s.StartMS, s.EndMS, 300_000-p.ChatLeadMS, 305_000+p.TailMS)
	}
	if !strings.Contains(s.Excerpt, "NO WAY he did it") || strings.Contains(s.Excerpt, "unrelated") {
		t.Errorf("excerpt %q", s.Excerpt)
	}
	if math.Abs(s.Score-0.6) > 1e-9 || s.SignalScore != s.Score {
		t.Errorf("score %v, want 0.6", s.Score)
	}
}

// Two signals over the same moment are one suggestion that says both fired,
// and agreement scores higher than either alone (noisy-OR).
func TestOverlappingSignalsMergeAndAgreementScoresHigher(t *testing.T) {
	chat := Signal{Kind: KindChat, StartMS: 300_000, EndMS: 305_000, Strength: 0.5}
	shout := Signal{Kind: KindLoudness, StartMS: 298_000, EndMS: 300_000, Strength: 0.5}
	far := Signal{Kind: KindLoudness, StartMS: 500_000, EndMS: 501_000, Strength: 0.9}
	got := Build([]Signal{far, chat, shout}, lines, 600_000, DefaultBuildParams())
	if len(got) != 2 {
		t.Fatalf("got %d suggestions: %+v", len(got), got)
	}
	if got[0].StartMS != 500_000-DefaultBuildParams().LoudLeadMS {
		t.Errorf("highest score first: %+v", got)
	}
	both := got[1]
	if len(both.Signals) != 2 || math.Abs(both.Score-0.75) > 1e-9 {
		t.Errorf("merged suggestion %+v, want two signals and 1-(0.5*0.5)=0.75", both)
	}
}

// Windows are clamped to the recording and to the short's length, and padded
// to a minimum a clip is worth watching.
func TestWindowsAreClampedAndPadded(t *testing.T) {
	p := DefaultBuildParams()
	early := Signal{Kind: KindLoudness, StartMS: 1000, EndMS: 2000, Strength: 0.5}
	long := Signal{Kind: KindChat, StartMS: 100_000, EndMS: 200_000, Strength: 0.5}
	got := Build([]Signal{early, long}, nil, 600_000, p)
	for _, s := range got {
		if s.StartMS < 0 || s.EndMS > 600_000 {
			t.Errorf("window outside the recording: %+v", s)
		}
		if d := s.EndMS - s.StartMS; d > p.MaxMS || d < p.MinMS {
			t.Errorf("window %dms outside %d..%d", d, p.MinMS, p.MaxMS)
		}
	}
}

func TestTheExcerptIsBoundedAndClean(t *testing.T) {
	long := []Line{{StartMS: 0, EndMS: 1000, Text: strings.Repeat("palabra ", 100) + "\ue000x\ue001"}}
	ex := Excerpt(long, 0, 1000, 50)
	if n := len([]rune(ex)); n > 50 || !strings.HasSuffix(ex, "…") || strings.ContainsAny(ex, "\ue000\ue001") {
		t.Errorf("excerpt %q (%d runes)", ex, n)
	}
}

// The model is handed numbered excerpts and may only answer with numbers. Its
// prose is never kept: a transcript is somebody's speech, and anything the
// model writes back may be that speech steering it.
func TestModelScoresAreNumbersOnlyAndChecked(t *testing.T) {
	got, err := ParseScores(`{"scores":[{"id":0,"score":0.9},{"id":1,"score":1.7},{"id":7,"score":0.5},{"id":2,"reason":"x"}]}`, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != 0.9 {
		t.Errorf("only id 0 is valid (1 is out of range, 7 unknown, 2 has no score): %v", got)
	}
	for _, bad := range []string{`not json`, `{"scores":[]}`, `{"nope":1}`, `{"scores":[{"id":5,"score":0.2}]}`} {
		if _, err := ParseScores(bad, 3); err == nil {
			t.Errorf("ParseScores(%q) accepted a reply with no usable score", bad)
		}
	}
}

func TestModelScoresBlendAndReorder(t *testing.T) {
	sugs := []Suggestion{
		{StartMS: 0, Score: 0.8, SignalScore: 0.8, Excerpt: "a"},
		{StartMS: 10, Score: 0.6, SignalScore: 0.6, Excerpt: "b"},
		{StartMS: 20, Score: 0.5, SignalScore: 0.5},
	}
	cands := Candidates(sugs)
	if len(cands) != 2 || cands[0].Text != "a" {
		t.Fatalf("only suggestions with words are sent: %+v", cands)
	}
	out := ApplyModel(sugs, cands, map[int]float64{0: 0.0, 1: 1.0})
	if out[0].StartMS != 10 || math.Abs(out[0].Score-0.8) > 1e-9 || out[0].ModelScore == nil {
		t.Errorf("b should lead at (0.6+1)/2: %+v", out)
	}
	hasModel := false
	for _, s := range out[0].Signals {
		hasModel = hasModel || s.Kind == KindModel
	}
	if !hasModel {
		t.Errorf("a model score is recorded as a signal that fired: %+v", out[0].Signals)
	}
	for _, s := range out {
		if s.StartMS == 20 && (s.ModelScore != nil || s.Score != 0.5) {
			t.Errorf("an unscored suggestion keeps its signal score: %+v", s)
		}
		if s.StartMS == 0 && math.Abs(s.Score-0.4) > 1e-9 {
			t.Errorf("a model zero halves the score: %+v", s)
		}
	}
}
