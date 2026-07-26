package routing

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// OutLabel is the filter_complex label carrying the finished stereo mix. The
// destination command builder maps it with -map "[aout]".
const OutLabel = "aout"

// Result is a compiled routing profile.
type Result struct {
	// FilterComplex is the full -filter_complex argument, ready to hand to
	// FFmpeg. Surfaced verbatim in the UI so routing is never a black box.
	FilterComplex string `json:"filterComplex"`
	// OutLabel is the label to -map for the destination's audio.
	OutLabel string `json:"outLabel"`
	// Summary is the human sentence shown on a destination card,
	// e.g. "Tracks 1, 2, 4 → stereo".
	Summary string `json:"summary"`
	// Tracks are the 0-based ingest tracks that actually contribute.
	Tracks []int `json:"tracks"`
	// Normalization records which clip-protection stage was actually applied,
	// after NormAuto has been resolved.
	Normalization NormMode `json:"normalization"`
	// Warnings are non-fatal mismatches between the profile and the live
	// source (e.g. the profile wants track 4 but the ingest sends three).
	Warnings []string `json:"warnings"`
}

// Compile turns a routing profile plus the live ingest layout into an FFmpeg
// filter graph.
//
// Shape of the generated graph, for N contributing tracks:
//
//	[0:a:0]pan=stereo|c0=...|c1=...[a_t0];
//	[0:a:1]pan=stereo|c0=...|c1=...[a_t1];
//	[a_t0][a_t1]amix=inputs=2:duration=longest:normalize=0[a_mix];
//	[a_mix]alimiter=limit=0.95:level=disabled[a_norm];
//	[a_norm]aresample=48000:async=1:first_pts=0[aout]
//
// Simple mode and matrix mode both funnel through this one path: simple mode
// is expanded to matrix cells using the standard downmix table first.
func Compile(p Profile, src Source) (Result, error) {
	if err := p.Validate(); err != nil {
		return Result{}, err
	}

	res := Result{OutLabel: OutLabel}

	cells, warns := resolveCells(p, src)
	res.Warnings = warns
	if len(cells) == 0 {
		return Result{}, ErrNoAudio
	}

	// Group by track, preserving ascending track order so the generated string
	// is deterministic (and therefore diffable and testable).
	byTrack := map[int][]Cell{}
	for _, c := range cells {
		byTrack[c.Track] = append(byTrack[c.Track], c)
	}
	tracks := make([]int, 0, len(byTrack))
	for t := range byTrack {
		tracks = append(tracks, t)
	}
	sort.Ints(tracks)
	res.Tracks = tracks

	var chains []string
	var mixInputs []string
	for _, t := range tracks {
		label := fmt.Sprintf("a_t%d", t)
		chains = append(chains, fmt.Sprintf("[0:a:%d]%s[%s]", t, PanFilter(byTrack[t]), label))
		mixInputs = append(mixInputs, "["+label+"]")
	}

	// Sum. amix's normalize=1 default divides by the input count, which would
	// silently drop a 3-track mix by ~9.5 dB. We want the true sum and handle
	// the resulting clip risk explicitly, below.
	cur := strings.TrimSuffix(strings.TrimPrefix(mixInputs[0], "["), "]")
	if len(mixInputs) > 1 {
		chains = append(chains, fmt.Sprintf("%samix=inputs=%d:duration=longest:normalize=0[a_mix]",
			strings.Join(mixInputs, ""), len(mixInputs)))
		cur = "a_mix"
	}

	norm := resolveNorm(p.Normalize, len(tracks))
	res.Normalization = norm
	if f := normFilter(norm); f != "" {
		chains = append(chains, fmt.Sprintf("[%s]%s[a_norm]", cur, f))
		cur = "a_norm"
	}

	// Final resample pins the rate the AAC encoder sees and lets FFmpeg absorb
	// the small timestamp drift that a UDP relay inevitably introduces.
	rate := p.SampleRate
	if rate == 0 {
		rate = 48000
	}
	chains = append(chains, fmt.Sprintf("[%s]aresample=%d:async=1:first_pts=0[%s]", cur, rate, OutLabel))

	res.FilterComplex = strings.Join(chains, ";")
	res.Summary = summarize(tracks)
	return res, nil
}

// resolveCells expands a profile into matrix cells validated against the live
// source, dropping (with a warning) anything the ingest cannot supply.
func resolveCells(p Profile, src Source) ([]Cell, []string) {
	var warns []string
	var out []Cell

	switch p.Mode {
	case ModeMatrix:
		for _, c := range p.Matrix {
			if c.Gain <= 0 {
				continue
			}
			t, ok := src.TrackByIndex(c.Track)
			if !ok {
				warns = append(warns, fmt.Sprintf("track %d is not present on the ingest; its matrix cells are ignored", c.Track+1))
				continue
			}
			if c.Channel >= t.Channels {
				warns = append(warns, fmt.Sprintf("track %d has %d channel(s); channel %d is ignored", c.Track+1, t.Channels, c.Channel+1))
				continue
			}
			out = append(out, c)
		}

	default: // ModeSimple
		for _, sel := range p.Tracks {
			if !sel.Enabled || sel.Gain <= 0 {
				continue
			}
			t, ok := src.TrackByIndex(sel.Track)
			if !ok {
				warns = append(warns, fmt.Sprintf("track %d is selected but not present on the ingest; it is ignored", sel.Track+1))
				continue
			}
			out = append(out, CellsForTrack(sel.Track, t.Channels, sel.Gain)...)
		}
	}

	warns = dedupe(warns)
	return out, warns
}

// PanFilter renders the pan filter for a single track's cells. Exported
// because it is the smallest independently meaningful unit of the engine and
// is what the tests pin down hardest.
func PanFilter(cells []Cell) string {
	// Deterministic ordering: by output, then by source channel.
	sorted := append([]Cell(nil), cells...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Out != sorted[j].Out {
			return sorted[i].Out < sorted[j].Out
		}
		return sorted[i].Channel < sorted[j].Channel
	})

	var exprs []string
	for out := 0; out < OutChannels; out++ {
		var terms []string
		for _, c := range sorted {
			if c.Out != out || c.Gain == 0 {
				continue
			}
			terms = append(terms, fmt.Sprintf("%s*c%d", fmtCoeff(c.Gain), c.Channel))
		}
		if len(terms) == 0 {
			// pan requires an expression for every output channel of the
			// declared layout. An explicitly silent leg is legal and is the
			// honest rendering of "nothing routed here".
			terms = []string{"0*c0"}
		}
		exprs = append(exprs, fmt.Sprintf("c%d=%s", out, strings.Join(terms, "+")))
	}
	return "pan=stereo|" + strings.Join(exprs, "|")
}

// resolveNorm turns NormAuto into a concrete stage. Summing is the only thing
// that creates new clipping, so a single-track profile stays untouched.
func resolveNorm(m NormMode, trackCount int) NormMode {
	if m != NormAuto {
		return m
	}
	if trackCount >= 2 {
		return NormLimiter
	}
	return NormOff
}

func normFilter(m NormMode) string {
	switch m {
	case NormLimiter:
		// level=disabled turns off alimiter's automatic makeup gain; we want
		// a ceiling, not an AGC that would undo the user's gain staging.
		return "alimiter=limit=0.95:level=disabled"
	case NormLoudnorm:
		// EBU R128 at the -16 LUFS target the streaming platforms expect.
		return "loudnorm=I=-16:TP=-1.5:LRA=11"
	default:
		return ""
	}
}

// fmtCoeff renders a gain coefficient at 4 decimal places (~0.0009 resolution,
// far below audibility) with trailing zeros trimmed, so filter strings stay
// readable and byte-stable across runs.
func fmtCoeff(g float64) string {
	s := strconv.FormatFloat(g, 'f', 4, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimSuffix(s, ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

func summarize(tracks []int) string {
	if len(tracks) == 0 {
		return "No audio"
	}
	parts := make([]string, len(tracks))
	for i, t := range tracks {
		parts[i] = strconv.Itoa(t + 1) // 1-based for humans
	}
	if len(tracks) == 1 {
		return "Track " + parts[0] + " → stereo"
	}
	return "Tracks " + strings.Join(parts, ", ") + " → stereo"
}

func dedupe(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
