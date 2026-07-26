package ffmpeg

import (
	"io"
	"strconv"
	"strings"
)

// Progress is one snapshot from FFmpeg's -progress stream.
type Progress struct {
	Frame       int64   `json:"frame"`
	FPS         float64 `json:"fps"`
	BitrateKbps float64 `json:"bitrateKbps"`
	TotalSize   int64   `json:"totalSize"`
	OutTimeMS   int64   `json:"outTimeMs"`
	DupFrames   int64   `json:"dupFrames"`
	DropFrames  int64   `json:"dropFrames"`
	Speed       float64 `json:"speed"`
	// Done is true on the final block, when FFmpeg reports progress=end.
	Done bool `json:"done"`
}

// ParseProgress reads FFmpeg's -progress output and calls fn once per complete
// block.
//
// The format is one key=value per line, with a `progress=continue` or
// `progress=end` line terminating each block. Emitting only on the terminator
// means consumers never observe a half-updated snapshot.
func ParseProgress(r io.Reader, fn func(Progress)) error {
	var p Progress
	return scanLines(r, func(line string) {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			return
		}
		v = strings.TrimSpace(v)
		switch k {
		case "frame":
			p.Frame = parseI(v)
		case "fps":
			p.FPS = parseF(v)
		case "bitrate":
			// Arrives as "1234.5kbits/s", or "N/A" before the first output.
			p.BitrateKbps = parseF(strings.TrimSuffix(v, "kbits/s"))
		case "total_size":
			p.TotalSize = parseI(v)
		case "out_time_ms", "out_time_us":
			// Long-standing FFmpeg quirk: the key says ms but the value is
			// microseconds. Both spellings appear across versions and both
			// carry microseconds.
			p.OutTimeMS = parseI(v) / 1000
		case "dup_frames":
			p.DupFrames = parseI(v)
		case "drop_frames":
			p.DropFrames = parseI(v)
		case "speed":
			p.Speed = parseF(strings.TrimSuffix(v, "x"))
		case "progress":
			p.Done = v == "end"
			fn(p)
			// Counters are cumulative and carry over; only the terminator flag
			// must be cleared.
			p.Done = false
		}
	})
}

// Levels is one metering snapshot: peak and RMS in dBFS for every channel of
// every track.
type Levels struct {
	// Peak[track][channel] in dBFS. -100 represents silence.
	Peak [][]float64 `json:"peak"`
	RMS  [][]float64 `json:"rms"`
}

// SilenceDB is the floor reported for a channel with no signal. astats emits
// -inf, which JSON cannot represent and a meter cannot draw.
const SilenceDB = -100.0

// ParseLevels reads the metering sidecar's stdout and calls fn once per frame
// with levels de-interleaved back into per-track slices.
//
// trackChannels maps the merged astats channel numbering (1-based, contiguous
// across all tracks) back to (track, channel). See MetersArgs for why the
// tracks are merged in the first place.
func ParseLevels(r io.Reader, trackChannels []int, fn func(Levels)) error {
	total := 0
	for _, c := range trackChannels {
		total += c
	}
	if total == 0 {
		return nil
	}

	// merged channel index (0-based) -> (track, channel)
	type loc struct{ track, ch int }
	index := make([]loc, total)
	n := 0
	for t, c := range trackChannels {
		for i := 0; i < c; i++ {
			index[n] = loc{t, i}
			n++
		}
	}

	fresh := func() Levels {
		l := Levels{Peak: make([][]float64, len(trackChannels)), RMS: make([][]float64, len(trackChannels))}
		for t, c := range trackChannels {
			l.Peak[t] = make([]float64, c)
			l.RMS[t] = make([]float64, c)
			for i := range l.Peak[t] {
				l.Peak[t][i] = SilenceDB
				l.RMS[t][i] = SilenceDB
			}
		}
		return l
	}

	cur := fresh()
	have := false

	return scanLines(r, func(line string) {
		line = strings.TrimSpace(line)
		if line == "" {
			return
		}
		// A new "frame:N ..." header terminates the previous frame's block.
		if strings.HasPrefix(line, "frame:") {
			if have {
				fn(cur)
			}
			cur = fresh()
			have = false
			return
		}

		key, val, ok := strings.Cut(line, "=")
		if !ok || !strings.HasPrefix(key, "lavfi.astats.") {
			return
		}
		rest := strings.TrimPrefix(key, "lavfi.astats.")
		chStr, metric, ok := strings.Cut(rest, ".")
		if !ok {
			return
		}
		chNum, err := strconv.Atoi(chStr)
		if err != nil || chNum < 1 || chNum > total {
			return
		}
		l := index[chNum-1]

		db := parseDB(val)
		switch metric {
		case "Peak_level":
			cur.Peak[l.track][l.ch] = db
			have = true
		case "RMS_level":
			cur.RMS[l.track][l.ch] = db
			have = true
		}
	})
}

// parseDB converts an astats level to a drawable number. Digital silence
// reports "-inf"; NaN shows up on the very first frame of some inputs.
func parseDB(s string) float64 {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "-inf", "inf", "nan", "-nan":
		return SilenceDB
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return SilenceDB
	}
	if v < SilenceDB {
		return SilenceDB
	}
	// astats can report a hair above 0 dBFS on inter-sample peaks; clamp so a
	// meter never draws past its own ceiling.
	if v > 0 {
		return 0
	}
	return v
}

func parseI(s string) int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

func parseF(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return v
}
