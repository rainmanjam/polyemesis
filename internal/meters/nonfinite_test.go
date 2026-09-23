package meters

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

// ebur128 prints `nan` for the momentary window after a signal drops to digital
// silence, and `-inf` for a window that never saw a sample. strconv.ParseFloat
// accepts both without an error, and encoding/json refuses both -- so one such
// frame used to turn GET /api/v1/status and /api/v1/loudness into a 200 with an
// empty body, and close every WebSocket carrying the loudness event.
func TestParseNeverYieldsANonFiniteReading(t *testing.T) {
	in := "frame:0 pts_time:1.0\n" +
		"lavfi.r128.M=nan\n" +
		"lavfi.r128.S=-inf\n" +
		"lavfi.r128.I=inf\n" +
		"lavfi.r128.LRA=nan\n" +
		"lavfi.r128.true_peak=nan\n"
	var got []Frame
	if err := Parse(strings.NewReader(in), func(f Frame) { got = append(got, f) }); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want one frame, got %d", len(got))
	}
	f := got[0]
	for name, v := range map[string]float64{
		"momentary": f.MomentaryLUFS, "shortTerm": f.ShortTermLUFS,
		"integrated": f.IntegratedLUFS, "range": f.RangeLU, "truePeak": f.TruePeakDBTP,
	} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Errorf("%s = %v; a non-finite reading must not leave the parser", name, v)
		}
	}
	if f.Integrated {
		t.Error("a non-finite integrated value is no measurement, so Integrated must be false")
	}
	if f.MomentaryLUFS != LUFSFloor || f.ShortTermLUFS != LUFSFloor {
		t.Errorf("non-finite loudness must read as the floor (no measurement), got M=%v S=%v", f.MomentaryLUFS, f.ShortTermLUFS)
	}
	target := Target{LUFS: -14, TruePeakDBTP: -1, ToleranceLU: ToleranceLU, Source: TargetProfile}
	if _, err := json.Marshal(Observe(1, "d", target, f, time.Now())); err != nil {
		t.Fatalf("the report built from this frame must encode: %v", err)
	}
}

func TestDBTPRejectsNonFiniteAmplitude(t *testing.T) {
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if got := DBTP(v); math.IsNaN(got) || math.IsInf(got, 0) {
			t.Errorf("DBTP(%v) = %v, want a finite value", v, got)
		}
	}
}
