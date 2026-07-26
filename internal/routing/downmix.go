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
// LFE is excluded, matching FFmpeg's default lfe_mix_level of 0.

// center is the -3 dB coefficient applied to centre and surround channels
// before normalization.
const center = 0.707

// DownmixMatrix returns a [OutChannels][channels] coefficient matrix taking a
// track with the given channel count down to stereo.
//
// Channel order follows FFmpeg's native layout ordering for each count, e.g.
// 6 channels is 5.1 = FL FR FC LFE BL BR.
func DownmixMatrix(channels int) [OutChannels][]float64 {
	var m [OutChannels][]float64
	if channels <= 0 {
		return m
	}
	m[OutL] = make([]float64, channels)
	m[OutR] = make([]float64, channels)

	// set assigns coefficient v from source channel ch to output out, before
	// normalization.
	set := func(out, ch int, v float64) {
		if ch < channels {
			m[out][ch] = v
		}
	}

	switch channels {
	case 1: // mono -> both legs at unity; a centred mono source, not half-power.
		set(OutL, 0, 1)
		set(OutR, 0, 1)
		return m // deliberately unnormalized: mono to stereo is not a downmix.

	case 2: // stereo -> passthrough
		set(OutL, 0, 1)
		set(OutR, 1, 1)
		return m

	case 3: // 3.0 = FL FR FC
		set(OutL, 0, 1)
		set(OutR, 1, 1)
		set(OutL, 2, center)
		set(OutR, 2, center)

	case 4: // quad = FL FR BL BR
		set(OutL, 0, 1)
		set(OutR, 1, 1)
		set(OutL, 2, center)
		set(OutR, 3, center)

	case 5: // 5.0 = FL FR FC BL BR
		set(OutL, 0, 1)
		set(OutR, 1, 1)
		set(OutL, 2, center)
		set(OutR, 2, center)
		set(OutL, 3, center)
		set(OutR, 4, center)

	case 6: // 5.1 = FL FR FC LFE BL BR   (LFE dropped)
		set(OutL, 0, 1)
		set(OutR, 1, 1)
		set(OutL, 2, center)
		set(OutR, 2, center)
		set(OutL, 4, center)
		set(OutR, 5, center)

	case 7: // 6.1 = FL FR FC LFE BC SL SR   (LFE dropped)
		set(OutL, 0, 1)
		set(OutR, 1, 1)
		set(OutL, 2, center)
		set(OutR, 2, center)
		set(OutL, 4, center)
		set(OutR, 4, center)
		set(OutL, 5, center)
		set(OutR, 6, center)

	case 8: // 7.1 = FL FR FC LFE BL BR SL SR   (LFE dropped)
		set(OutL, 0, 1)
		set(OutR, 1, 1)
		set(OutL, 2, center)
		set(OutR, 2, center)
		set(OutL, 4, center)
		set(OutR, 5, center)
		set(OutL, 6, center)
		set(OutR, 7, center)

	default:
		// Unknown wide layout: split even channels left, odd channels right so
		// nothing is silently discarded, then normalize below.
		for ch := 0; ch < channels; ch++ {
			if ch%2 == 0 {
				set(OutL, ch, 1)
			} else {
				set(OutR, ch, 1)
			}
		}
	}

	normalizeRows(&m)
	return m
}

// normalizeRows scales each output row so its coefficients sum to 1, which is
// what keeps a fully correlated source from exceeding full scale.
func normalizeRows(m *[OutChannels][]float64) {
	for out := range m {
		var sum float64
		for _, v := range m[out] {
			sum += v
		}
		if sum <= 1 {
			// Already at or below unity (e.g. plain stereo); leave it alone
			// rather than *boosting* a sparse row up to unity.
			continue
		}
		for ch := range m[out] {
			m[out][ch] /= sum
		}
	}
}

// CellsForTrack expands one simple-mode track selection into the equivalent
// matrix cells. This is what makes matrix mode a true superset of simple mode:
// both compile through exactly the same code path below.
func CellsForTrack(track, channels int, gain float64) []Cell {
	m := DownmixMatrix(channels)
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
