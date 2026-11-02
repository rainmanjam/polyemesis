// Package highlights finds the moments in a recording worth cutting into a
// short, and hands them to a human. It never cuts anything itself.
//
// Three signals, each cheap, each wrong on its own in a way the others are not:
//
//   - CHAT. Viewers type when something happens. A burst of messages over the
//     stream's own baseline is the most direct "that was a moment" there is --
//     and it lags the moment by the seconds it takes to react, so the window it
//     produces starts before it.
//   - LOUDNESS. People shout. EBU R128 momentary loudness over the recording's
//     own median finds a laugh, a scream, a crowd. It is blind to a quiet
//     moment that mattered, which chat is not.
//   - THE MODEL, optional. The operator's own automod provider, asked to rate
//     the words spoken in each candidate. Numbers only: the transcript is
//     somebody's speech, and nothing the model writes back is kept.
//
// Everything here is a pure function of its inputs except the job worker, so
// every threshold is tested against a fixture whose right answer is arithmetic.
//
// Each detector compares a recording against ITSELF -- its median and its
// spread -- rather than against an absolute number. A 40-viewer stream and a
// 40,000-viewer one have nothing in common in raw message counts, and a quiet
// podcast and a shooter have nothing in common in raw loudness.
package highlights

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Kind is which detector a Signal came from. Stored and sent to the UI, so the
// values are part of the API and are not renamed.
type Kind string

const (
	KindChat     Kind = "chat"
	KindLoudness Kind = "loudness"
	KindModel    Kind = "model"
)

// Signal is one detector firing over a span of the timeline.
type Signal struct {
	Kind    Kind  `json:"kind"`
	StartMS int64 `json:"startMs"`
	EndMS   int64 `json:"endMs"`
	// Strength is 0..1, how far past its threshold the detector went.
	Strength float64 `json:"strength"`
	// Peak and Baseline are in the detector's own unit -- messages per bin for
	// chat, LUFS for loudness, the model's 0..1 for the model -- so the review
	// queue can say WHY rather than only that.
	Peak     float64 `json:"peak"`
	Baseline float64 `json:"baseline"`
}

// ----------------------------------------------------------------- chat

// ChatParams are the chat detector's thresholds.
type ChatParams struct {
	// BinMS is the counting window.
	BinMS int64
	// MinMessages is the floor below which no bin is a moment, whatever the
	// ratio says: four messages in an empty chat is somebody saying hello.
	MinMessages int
	// Z is how many spreads above the median a bin must be.
	Z float64
	// Ratio is how many times the median a bin must be, which is what keeps a
	// busy chat's ordinary variance from reading as a moment.
	Ratio float64
}

// DefaultChatParams are the shipped thresholds.
func DefaultChatParams() ChatParams {
	return ChatParams{BinMS: 5000, MinMessages: 5, Z: 3, Ratio: 2}
}

// ChatSpikes finds bursts of chat. times are message arrival times in TIMELINE
// milliseconds; anything outside [0, durMS) is ignored.
//
// A bin is a spike when its count is at least
//
//	max(MinMessages, median + Z*spread, Ratio*median)
//
// where spread is 1.4826 x the median absolute deviation, floored at one
// message so a perfectly steady chat does not turn every extra message into a
// spike. Adjacent spike bins are one moment.
func ChatSpikes(times []int64, durMS int64, p ChatParams) []Signal {
	if durMS <= 0 || p.BinMS <= 0 || len(times) == 0 {
		return nil
	}
	n := int((durMS + p.BinMS - 1) / p.BinMS)
	counts := make([]float64, n)
	for _, at := range times {
		if at < 0 || at >= durMS {
			continue
		}
		counts[at/p.BinMS]++
	}
	med := median(counts)
	spread := math.Max(1, 1.4826*mad(counts, med))
	thr := math.Max(float64(p.MinMessages), math.Max(med+p.Z*spread, p.Ratio*med))
	return runs(counts, func(v float64) bool { return v >= thr }, p.BinMS, KindChat,
		func(peak float64) float64 { return clamp01(0.5 * (peak - med) / (thr - med)) }, med)
}

// ------------------------------------------------------------- loudness

// Sample is one momentary-loudness reading, in timeline milliseconds and LUFS.
type Sample struct {
	AtMS int64   `json:"atMs"`
	LUFS float64 `json:"lufs"`
}

// LoudParams are the loudness detector's thresholds.
type LoudParams struct {
	// BinMS is the window a peak is measured over: the loudest momentary
	// reading in each.
	BinMS int64
	// DeltaLU is how far above the recording's median a bin must be.
	DeltaLU float64
	// SilenceLUFS gates silence out of the baseline. EBU R128's own absolute
	// gate is -70 LUFS, and FFmpeg reports digital silence as about -120.7.
	SilenceLUFS float64
}

// DefaultLoudParams are the shipped thresholds.
func DefaultLoudParams() LoudParams {
	return LoudParams{BinMS: 1000, DeltaLU: 8, SilenceLUFS: -70}
}

// maxPTSSeconds bounds a timestamp ParseMomentary will believe: thirty days,
// far past any one recording file, far short of anything that overflows.
const maxPTSSeconds = 30 * 24 * 3600

// LoudnessArgs is the FFmpeg invocation that measures one file: the first
// audio track's EBU R128 momentary loudness, printed every 100ms to stdout.
// Audio only, so hours of recording measure in a minute.
func LoudnessArgs(path string) []string {
	return []string{
		"-hide_banner", "-nostdin", "-loglevel", "error",
		"-i", path,
		"-map", "0:a:0?", "-vn", "-sn", "-dn",
		"-af", "ebur128=metadata=1,ametadata=mode=print:key=lavfi.r128.M:file=-",
		"-f", "null", "-",
	}
}

// ParseMomentary reads LoudnessArgs' output: alternating
//
//	frame:12   pts:52920   pts_time:1.2
//	lavfi.r128.M=-23.4
//
// lines. offsetMS places the file on the timeline. A reading that is not a
// number is skipped, not an error: the format is FFmpeg's, and one odd line is
// not a reason to throw away an hour of measurement.
func ParseMomentary(r io.Reader, offsetMS int64) ([]Sample, error) {
	var out []Sample
	sc := bufio.NewScanner(r)
	at := int64(-1)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.Index(line, "pts_time:"); i >= 0 {
			f := strings.Fields(line[i+len("pts_time:"):])
			at = -1
			if len(f) > 0 {
				// The timestamp comes out of a media file. Anything that is
				// not a finite number inside one plausible recording is
				// dropped BEFORE it is scaled: ParseFloat accepts "inf", and
				// int64(+Inf) is whatever the platform makes of it.
				if v, err := strconv.ParseFloat(f[0], 64); err == nil &&
					!math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= maxPTSSeconds {
					at = int64(math.Round(v * 1000))
				}
			}
			continue
		}
		if v, ok := strings.CutPrefix(line, "lavfi.r128.M="); ok && at >= 0 {
			lufs, err := strconv.ParseFloat(v, 64)
			if err == nil && !math.IsNaN(lufs) && !math.IsInf(lufs, 0) {
				out = append(out, Sample{AtMS: offsetMS + at, LUFS: lufs})
			}
			at = -1
		}
	}
	return out, sc.Err()
}

// LoudnessPeaks finds the bins at least DeltaLU above the recording's median
// non-silent loudness.
//
// durMS is the recording's length and is what sizes the bins: never the
// samples, whose timestamps came out of a media file. A sample outside
// [0, durMS) is ignored, as ChatSpikes ignores a message outside it.
func LoudnessPeaks(samples []Sample, durMS int64, p LoudParams) []Signal {
	if len(samples) == 0 || p.BinMS <= 0 || durMS <= 0 {
		return nil
	}
	n := int((durMS + p.BinMS - 1) / p.BinMS)
	bins := make([]float64, n)
	for i := range bins {
		bins[i] = math.Inf(-1)
	}
	for _, s := range samples {
		if s.AtMS < 0 || s.AtMS >= durMS {
			continue
		}
		i := int(s.AtMS / p.BinMS)
		bins[i] = math.Max(bins[i], s.LUFS)
	}
	var voiced []float64
	for _, v := range bins {
		if v > p.SilenceLUFS {
			voiced = append(voiced, v)
		}
	}
	if len(voiced) == 0 {
		return nil
	}
	base := median(voiced)
	thr := base + p.DeltaLU
	return runs(bins, func(v float64) bool { return v > p.SilenceLUFS && v >= thr }, p.BinMS, KindLoudness,
		func(peak float64) float64 { return clamp01(0.5 + (peak-thr)/(2*p.DeltaLU)) }, base)
}

// runs collapses consecutive bins that pass into signals.
func runs(bins []float64, hot func(float64) bool, binMS int64, kind Kind, strength func(float64) float64, base float64) []Signal {
	var out []Signal
	for i := 0; i < len(bins); {
		if !hot(bins[i]) {
			i++
			continue
		}
		j, peak := i, bins[i]
		for j < len(bins) && hot(bins[j]) {
			peak = math.Max(peak, bins[j])
			j++
		}
		out = append(out, Signal{
			Kind: kind, StartMS: int64(i) * binMS, EndMS: int64(j) * binMS,
			Strength: strength(peak), Peak: peak, Baseline: base,
		})
		i = j
	}
	return out
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	m := len(s) / 2
	if len(s)%2 == 1 {
		return s[m]
	}
	return (s[m-1] + s[m]) / 2
}

func mad(v []float64, med float64) float64 {
	d := make([]float64, len(v))
	for i, x := range v {
		d[i] = math.Abs(x - med)
	}
	return median(d)
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

// ------------------------------------------------------------- building

// Line is one transcript line in timeline milliseconds.
type Line struct {
	StartMS int64  `json:"startMs"`
	EndMS   int64  `json:"endMs"`
	Text    string `json:"text"`
}

// Suggestion is one candidate short, waiting for a human.
type Suggestion struct {
	StartMS int64 `json:"startMs"`
	EndMS   int64 `json:"endMs"`
	// Score is what the queue is sorted by: SignalScore, blended with the
	// model's when the model answered for this one.
	Score       float64  `json:"score"`
	SignalScore float64  `json:"signalScore"`
	ModelScore  *float64 `json:"modelScore,omitempty"`
	Signals     []Signal `json:"signals"`
	Excerpt     string   `json:"excerpt"`
}

// BuildParams shape the windows signals become.
type BuildParams struct {
	// ChatLeadMS is how far before a chat spike the window starts: people
	// type AFTER the moment.
	ChatLeadMS int64
	// LoudLeadMS is the run-up before a loudness peak: the setup is part of
	// the joke.
	LoudLeadMS int64
	// TailMS is the reaction after either.
	TailMS int64
	// MinMS and MaxMS bound a window. MaxMS is the short's length.
	MinMS, MaxMS int64
	// Limit is how many suggestions one scan keeps.
	Limit int
	// ExcerptRunes bounds the transcript excerpt.
	ExcerptRunes int
}

// DefaultBuildParams are the shipped window shapes.
func DefaultBuildParams() BuildParams {
	return BuildParams{ChatLeadMS: 15_000, LoudLeadMS: 8_000, TailMS: 5_000,
		MinMS: 15_000, MaxMS: 60_000, Limit: 20, ExcerptRunes: 280}
}

// Build turns signals into ranked suggestions: each signal becomes a window,
// overlapping windows merge, windows are clamped to the recording and to a
// short's length, and each is scored by noisy-OR over the strongest signal of
// each kind -- two detectors agreeing is worth more than either alone.
func Build(signals []Signal, lines []Line, durMS int64, p BuildParams) []Suggestion {
	type win struct {
		start, end int64
		sigs       []Signal
	}
	// Each signal's window is SHAPED first -- lead and tail, clamped to the
	// recording, padded to the minimum, cut to a short's length -- and only
	// then merged, so two windows that touch only once padded are one
	// suggestion rather than two overlapping ones.
	var wins []win
	for _, s := range signals {
		lead := p.LoudLeadMS
		if s.Kind == KindChat {
			lead = p.ChatLeadMS
		}
		start, end := max(0, s.StartMS-lead), min(durMS, s.EndMS+p.TailMS)
		if end-start < p.MinMS {
			end = min(durMS, start+p.MinMS)
			start = max(0, end-p.MinMS)
		}
		if end-start > p.MaxMS {
			// The lead is shorter than a short, so the cut keeps the signal.
			end = start + p.MaxMS
		}
		wins = append(wins, win{start: start, end: end, sigs: []Signal{s}})
	}
	sort.Slice(wins, func(i, j int) bool { return wins[i].start < wins[j].start })
	// A merge never grows past a short's length: a window that would push the
	// union past it starts a suggestion of its own. Cutting the union instead
	// left the later signal listed -- and scored -- on a span that no longer
	// contained it.
	var merged []win
	for _, w := range wins {
		k := len(merged) - 1
		if k < 0 || w.start >= merged[k].end {
			merged = append(merged, w)
			continue
		}
		if max(merged[k].end, w.end)-merged[k].start <= p.MaxMS {
			merged[k].end = max(merged[k].end, w.end)
			merged[k].sigs = append(merged[k].sigs, w.sigs...)
			continue
		}
		// Overlapping, but the union would be too long for a short.
		if sig := w.sigs[0]; sig.EndMS <= merged[k].end {
			// The signal itself is already on screen in the earlier window:
			// it belongs there, not in a window of its own.
			merged[k].sigs = append(merged[k].sigs, sig)
			continue
		}
		// Otherwise it starts a suggestion where the earlier one ends, so the
		// two never overlap, padded back to the minimum if that shortened it.
		w.start = merged[k].end
		if w.end-w.start < p.MinMS {
			w.end = min(durMS, w.start+p.MinMS)
		}
		merged = append(merged, w)
	}
	out := make([]Suggestion, 0, len(merged))
	for _, w := range merged {
		start, end := w.start, w.end
		score := noisyOR(w.sigs)
		sort.Slice(w.sigs, func(i, j int) bool { return w.sigs[i].StartMS < w.sigs[j].StartMS })
		out = append(out, Suggestion{
			StartMS: start, EndMS: end, Score: score, SignalScore: score,
			Signals: w.sigs, Excerpt: Excerpt(lines, start, end, p.ExcerptRunes),
		})
	}
	rank(out)
	if p.Limit > 0 && len(out) > p.Limit {
		out = out[:p.Limit]
	}
	return out
}

func noisyOR(sigs []Signal) float64 {
	best := map[Kind]float64{}
	for _, s := range sigs {
		best[s.Kind] = math.Max(best[s.Kind], s.Strength)
	}
	miss := 1.0
	for _, v := range best {
		miss *= 1 - v
	}
	return 1 - miss
}

func rank(s []Suggestion) {
	sort.SliceStable(s, func(i, j int) bool {
		if s[i].Score != s[j].Score {
			return s[i].Score > s[j].Score
		}
		return s[i].StartMS < s[j].StartMS
	})
}

// Excerpt is the transcript spoken over [a, b), cleaned and cut to maxRunes.
func Excerpt(lines []Line, a, b int64, maxRunes int) string {
	var parts []string
	for _, l := range lines {
		if l.EndMS > a && l.StartMS < b {
			if t := clean(l.Text); t != "" {
				parts = append(parts, t)
			}
		}
	}
	out := []rune(strings.Join(parts, " "))
	if maxRunes > 1 && len(out) > maxRunes {
		return strings.TrimSpace(string(out[:maxRunes-1])) + "…"
	}
	return string(out)
}

// clean drops control characters and the transcript store's search-highlight
// markers, and collapses whitespace.
func clean(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\ue000', r == '\ue001':
		case unicode.IsControl(r), unicode.IsSpace(r):
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// ---------------------------------------------------------------- model

// Candidate is one suggestion as the model sees it: a number and the words.
// Index points back into the suggestion list and never leaves the process.
type Candidate struct {
	ID    int    `json:"id"`
	Text  string `json:"text"`
	Index int    `json:"-"`
}

// Candidates are the suggestions that have words to judge. A window nobody
// spoke in is left to its signals rather than sent as an empty string.
func Candidates(sugs []Suggestion) []Candidate {
	var out []Candidate
	for i, s := range sugs {
		if s.Excerpt != "" {
			out = append(out, Candidate{ID: len(out), Text: s.Excerpt, Index: i})
		}
	}
	return out
}

// ErrNoScores is a model reply with nothing in it this package can use.
var ErrNoScores = errors.New("the model's reply held no usable scores")

// ParseScores reads the model's reply: {"scores":[{"id":0,"score":0.7},...]}.
//
// Everything that is not a known id with a score in 0..1 is dropped, and any
// other field -- a "reason", say -- is never read. A reply with nothing usable
// is an error, so a model that answered {"No": null} is reported as failing
// rather than as having thought nothing was good.
func ParseScores(content string, n int) (map[int]float64, error) {
	var reply struct {
		Scores *[]struct {
			ID    *int     `json:"id"`
			Score *float64 `json:"score"`
		} `json:"scores"`
	}
	if err := json.Unmarshal([]byte(content), &reply); err != nil || reply.Scores == nil {
		return nil, ErrNoScores
	}
	out := map[int]float64{}
	for _, s := range *reply.Scores {
		if s.ID == nil || s.Score == nil || *s.ID < 0 || *s.ID >= n ||
			math.IsNaN(*s.Score) || *s.Score < 0 || *s.Score > 1 {
			continue
		}
		out[*s.ID] = *s.Score
	}
	if len(out) == 0 {
		return nil, ErrNoScores
	}
	return out, nil
}

// ApplyModel blends the model's scores into the suggestions it answered for --
// the mean of the signal score and the model's -- records each as a signal
// that fired, and re-ranks. A suggestion the model did not score keeps its
// signal score.
func ApplyModel(sugs []Suggestion, cands []Candidate, scores map[int]float64) []Suggestion {
	out := append([]Suggestion(nil), sugs...)
	for _, c := range cands {
		v, ok := scores[c.ID]
		if !ok || c.Index < 0 || c.Index >= len(out) {
			continue
		}
		s := &out[c.Index]
		ms := v
		s.ModelScore = &ms
		s.Score = (s.SignalScore + v) / 2
		s.Signals = append(append([]Signal(nil), s.Signals...),
			Signal{Kind: KindModel, StartMS: s.StartMS, EndMS: s.EndMS, Strength: v, Peak: v})
	}
	rank(out)
	return out
}
