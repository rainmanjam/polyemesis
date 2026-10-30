package routing

import (
	"encoding/json"
	"strings"
	"testing"
)

// A15: a matrix cell can invert the polarity of the channel it routes. It is
// rendered as a negative pan coefficient and nowhere else, so it never reaches
// the simple-mode path and a profile that does not use it compiles exactly as
// it did.

func phaseMatrix(invert bool) Profile {
	return Profile{
		Mode: ModeMatrix, Normalize: NormOff, SampleRate: 48000,
		Matrix: []Cell{
			{Track: 0, Channel: 0, Out: OutL, Gain: 1},
			{Track: 0, Channel: 1, Out: OutL, Gain: 0.5, Invert: invert},
			{Track: 0, Channel: 1, Out: OutR, Gain: 1, Invert: invert},
		},
	}
}

// An uninverted cell set renders byte for byte as before the field existed.
func TestPanFilterWithoutInvertIsUnchanged(t *testing.T) {
	got := PanFilter(phaseMatrix(false).Matrix)
	const want = "pan=stereo|c0=1*c0+0.5*c1|c1=1*c1"
	if got != want {
		t.Errorf("PanFilter = %q, want %q", got, want)
	}
}

// MUTATION: drop the sign in PanFilter. Observed: FAIL.
func TestPanFilterInvertsWithANegativeCoefficient(t *testing.T) {
	got := PanFilter(phaseMatrix(true).Matrix)
	// A negative term after "+" and as the first term of a leg: both forms
	// FFmpeg's pan parser accepts (checked against a real FFmpeg, which
	// cancels a channel summed with its inverted copy).
	const want = "pan=stereo|c0=1*c0+-0.5*c1|c1=-1*c1"
	if got != want {
		t.Errorf("PanFilter = %q, want %q", got, want)
	}
}

// An inverted cell at zero gain is still the absence of a contribution.
func TestInvertedZeroCellIsStillSilent(t *testing.T) {
	got := PanFilter([]Cell{{Track: 0, Channel: 0, Out: OutL, Gain: 0, Invert: true}})
	if strings.Contains(got, "-") {
		t.Errorf("a zero cell rendered a term: %q", got)
	}
}

// The field is omitted when false, so a stored profile's JSON does not change
// shape for anyone who never touches it.
func TestInvertIsOmittedWhenFalse(t *testing.T) {
	b, err := json.Marshal(Cell{Track: 0, Channel: 0, Out: OutL, Gain: 1})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "invert") {
		t.Errorf("an uninverted cell marshals the field: %s", b)
	}
	b, _ = json.Marshal(Cell{Gain: 1, Invert: true})
	if !strings.Contains(string(b), `"invert":true`) {
		t.Errorf("an inverted cell lost the field: %s", b)
	}
}

// Polarity is a pan coefficient, not a level: an invert-only edit must change
// the graph with its volume levels masked, which is the restart signature, so
// it restarts rather than being sent as a live volume command.
//
// MUTATION: drop the sign in PanFilter. Observed: FAIL.
func TestAnInvertOnlyChangeIsStructural(t *testing.T) {
	src := stereoSource(1)
	a, err := Compile(phaseMatrix(false), src)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Compile(phaseMatrix(true), src)
	if err != nil {
		t.Fatal(err)
	}
	if maskVolumes(a.FilterComplex) == maskVolumes(b.FilterComplex) {
		t.Errorf("an invert change survives the live-volume mask, so it would be sent as a level:\n%s\n%s",
			a.FilterComplex, b.FilterComplex)
	}
}

// A copy destination forwards samples untouched, so it cannot invert one.
// Refused, never silently ignored, like every other mix instruction copy drops.
//
// MUTATION: drop the invert check in CopyMixProblems. Observed: FAIL.
func TestCopyRefusesAnInvertedCell(t *testing.T) {
	p := Profile{
		Mode: ModeMatrix, Normalize: NormOff,
		Matrix: []Cell{
			{Track: 0, Channel: 0, Out: OutL, Gain: 1, Invert: true},
			{Track: 0, Channel: 1, Out: OutR, Gain: 1},
		},
	}
	probs := CopyMixProblems(p)
	if len(probs) != 1 || !strings.Contains(probs[0], "polarity") {
		t.Errorf("CopyMixProblems = %q, want one polarity refusal", probs)
	}
	p.Matrix[0].Invert = false
	if probs := CopyMixProblems(p); len(probs) != 0 {
		t.Errorf("an identity matrix is refused: %q", probs)
	}
}

// Against an unmeasured layout the drawn matrix, invert included, is replaced
// by a runtime downmix, and the result says so (Provisional).
func TestProvisionalCompileDropsInvertAndSaysSo(t *testing.T) {
	res, err := CompileProvisional(phaseMatrix(true), stereoSource(1))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Provisional {
		t.Error("a provisional compile is not marked provisional")
	}
	if strings.Contains(res.FilterComplex, "*c") {
		t.Errorf("a provisional compile still renders the drawn pan:\n%s", res.FilterComplex)
	}
}
