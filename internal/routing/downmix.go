package routing

// Stereo downmix coefficient tables.
//
// These reproduce FFmpeg's own default rematrixing (libswresample), which
// applies the ITU-R BS.775 coefficients and then *normalizes* by the sum of
// the coefficients feeding each output channel. The normalization is the part
// people forget: without it, a hot 5.1 source downmixes to L = FL + 0.707*FC +
// 0.707*BL, which can reach 2.41x full scale and clip hard the moment centre
// and surround are correlated with the fronts.
//
// LFE is excluded, matching FFmpeg's default lfe_mix_level of 0. That exclusion
// is by channel *name*, not by position: the first version of this file keyed
// the whole table on channel count, which made every 3-channel track a 3.0
// (FL FR FC) and folded a 2.1 track's LFE into both legs — the exact thing the
// paragraph above promises never happens. The count table survives only as the
// fallback for a track whose layout the probe could not name.

// center is the -3 dB coefficient applied to centre and surround channels
// before normalization.
const center = 0.707

// Channel names, spelled as FFmpeg spells them.
const (
	chFL  = "FL"
	chFR  = "FR"
	chFC  = "FC"
	chLFE = "LFE"
	chBL  = "BL"
	chBR  = "BR"
	chBC  = "BC"
	chSL  = "SL"
	chSR  = "SR"
	chFLC = "FLC"
	chFRC = "FRC"
	chDL  = "DL"
	chDR  = "DR"
)

// layoutChannels is libavutil's channel order for every layout ffprobe names.
// Taken from libavutil/channel_layout.c so the indices match what actually
// arrives on the wire.
var layoutChannels = map[string][]string{
	"mono":           {chFC},
	"stereo":         {chFL, chFR},
	"downmix":        {chDL, chDR},
	"2.1":            {chFL, chFR, chLFE},
	"3.0":            {chFL, chFR, chFC},
	"3.0(back)":      {chFL, chFR, chBC},
	"4.0":            {chFL, chFR, chFC, chBC},
	"quad":           {chFL, chFR, chBL, chBR},
	"quad(side)":     {chFL, chFR, chSL, chSR},
	"3.1":            {chFL, chFR, chFC, chLFE},
	"5.0":            {chFL, chFR, chFC, chBL, chBR},
	"5.0(side)":      {chFL, chFR, chFC, chSL, chSR},
	"4.1":            {chFL, chFR, chFC, chLFE, chBC},
	"5.1":            {chFL, chFR, chFC, chLFE, chBL, chBR},
	"5.1(side)":      {chFL, chFR, chFC, chLFE, chSL, chSR},
	"6.0":            {chFL, chFR, chFC, chBC, chSL, chSR},
	"6.0(front)":     {chFL, chFR, chFLC, chFRC, chSL, chSR},
	"hexagonal":      {chFL, chFR, chFC, chBL, chBR, chBC},
	"6.1":            {chFL, chFR, chFC, chLFE, chBC, chSL, chSR},
	"6.1(back)":      {chFL, chFR, chFC, chLFE, chBL, chBR, chBC},
	"6.1(front)":     {chFL, chFR, chFLC, chFRC, chLFE, chSL, chSR},
	"7.0":            {chFL, chFR, chFC, chBL, chBR, chSL, chSR},
	"7.0(front)":     {chFL, chFR, chFC, chFLC, chFRC, chSL, chSR},
	"7.1":            {chFL, chFR, chFC, chLFE, chBL, chBR, chSL, chSR},
	"7.1(wide)":      {chFL, chFR, chFC, chLFE, chBL, chBR, chFLC, chFRC},
	"7.1(wide-side)": {chFL, chFR, chFC, chLFE, chFLC, chFRC, chSL, chSR},
	"octagonal":      {chFL, chFR, chFC, chBL, chBR, chBC, chSL, chSR},
}

// channelCoeffs is the pre-normalization contribution of one named channel to
// the left and right legs.
//
// Front left/right and their of-centre variants stay on their own side at
// unity; anything centred or surround folds in at -3 dB; LFE contributes
// nothing at all.
func channelCoeffs(name string) (l, r float64) {
	switch name {
	case chFL, chDL:
		return 1, 0
	case chFR, chDR:
		return 0, 1
	case chFC:
		return center, center
	case chLFE:
		return 0, 0
	case chBL, chSL:
		return center, 0
	case chBR, chSR:
		return 0, center
	case chBC:
		return center, center
	case chFLC:
		return 1, 0
	case chFRC:
		return 0, 1
	default:
		return 0, 0
	}
}

// DownmixMatrix returns a [OutChannels][channels] coefficient matrix taking a
// track with the given channel count and layout down to stereo.
//
// layout is the name ffprobe reported ("5.1", "2.1", "hexagonal", ...). An
// empty or unrecognized layout falls back to a table keyed on channel count
// alone, which assumes FFmpeg's native ordering for that count.
func DownmixMatrix(channels int, layout string) [OutChannels][]float64 {
	var m [OutChannels][]float64
	if channels <= 0 {
		return m
	}
	m[OutL] = make([]float64, channels)
	m[OutR] = make([]float64, channels)

	// Mono is the one case that is not a downmix at all: a centred mono source
	// belongs on both legs at unity, not at half power, so it skips both the
	// coefficient table and the normalization below.
	if channels == 1 {
		m[OutL][0] = 1
		m[OutR][0] = 1
		return m
	}

	names, ok := layoutChannels[layout]
	if !ok || len(names) != channels {
		// The layout is missing, unknown, or disagrees with the channel count.
		// The count is what the demuxer will actually hand us, so it wins.
		names = layoutByCount(channels)
	}

	if names == nil {
		// A width with no canonical ordering. Split even channels left and odd
		// right so nothing is silently discarded.
		for ch := 0; ch < channels; ch++ {
			if ch%2 == 0 {
				m[OutL][ch] = 1
			} else {
				m[OutR][ch] = 1
			}
		}
	} else {
		for ch, name := range names {
			m[OutL][ch], m[OutR][ch] = channelCoeffs(name)
		}
	}

	normalizeRows(&m)
	return m
}

// layoutByCount is FFmpeg's native layout for a given channel count, and the
// fallback whenever the probe could not name one. Returns nil for a width with
// no canonical ordering.
func layoutByCount(channels int) []string {
	switch channels {
	case 2:
		return layoutChannels["stereo"]
	case 3:
		return layoutChannels["3.0"]
	case 4:
		return layoutChannels["quad"]
	case 5:
		return layoutChannels["5.0"]
	case 6:
		return layoutChannels["5.1"]
	case 7:
		return layoutChannels["6.1"]
	case 8:
		return layoutChannels["7.1"]
	default:
		return nil
	}
}

// normalizeRows scales the coefficients so no output row can exceed unity,
// which is what keeps a fully correlated source from clipping.
//
// Both rows are divided by the SAME figure — the larger of the two sums —
// rather than each by its own. Dividing per row silently re-pans the mix
// whenever the two sides are not symmetric: the unknown-wide fallback puts one
// extra channel on the left at every odd width, and per-row normalization
// turned that into a permanent 1.94 dB image shift.
func normalizeRows(m *[OutChannels][]float64) {
	var div float64
	for out := range m {
		var sum float64
		for _, v := range m[out] {
			sum += v
		}
		if sum > div {
			div = sum
		}
	}
	if div <= 1 {
		// Already at or below unity (e.g. plain stereo); leave it alone rather
		// than *boosting* a sparse row up to unity.
		return
	}
	for out := range m {
		for ch := range m[out] {
			m[out][ch] /= div
		}
	}
}

// CellsForTrack expands one simple-mode track selection into the equivalent
// matrix cells. This is what makes matrix mode a true superset of simple mode:
// both compile through exactly the same code path below.
func CellsForTrack(track int, t Track, gain float64) []Cell {
	m := DownmixMatrix(t.Channels, t.Layout)
	var cells []Cell
	for out := 0; out < OutChannels; out++ {
		for ch, coeff := range m[out] {
			if coeff == 0 {
				continue
			}
			cells = append(cells, Cell{
				Track:   track,
				Channel: ch,
				Out:     out,
				Gain:    coeff * gain,
			})
		}
	}
	return cells
}

// peakGain is the largest total gain any single output channel applies, which
// is the figure that decides whether a mix can clip. Cells are summed per
// output by pan, so this is the row sum of the compiled matrix.
func peakGain(cells []Cell) float64 {
	var sums [OutChannels]float64
	for _, c := range cells {
		if c.Out < 0 || c.Out >= OutChannels || c.Gain <= 0 {
			continue
		}
		sums[c.Out] += c.Gain
	}
	var peak float64
	for _, s := range sums {
		if s > peak {
			peak = s
		}
	}
	return peak
}
