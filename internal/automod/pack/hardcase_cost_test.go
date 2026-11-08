//go:build !race

package pack

import (
	"strings"
	"testing"
)

// Signals runs on the chat adapter goroutine for every unsettled message under
// scope "hard". The gates catch an allocation per word or a quadratic, not
// noise; measured cost is a small fraction of each. Ten times wider under
// coverage, as costGateNs is.
const (
	signalsEdgeLineGateNs = 60_000 // an 80-word line of edge-digit words ("4Head")
	signalsCutFieldGateNs = 12_000 // fields with dots and slashes, links and cuts
	signalsAllocGate      = 40     // allocs/op for the 80-word line (measured 18; fold-per-word was 178)
)

var (
	edgeLine = strings.TrimSpace(strings.Repeat("4Head 30fps lol gr8 10k ", 16))
	cutLine  = "twitch.tv/gg clips.twitch.tv/AbCdEf e.g. wait.. 1/2 v1.2.3 k.y/s ki.ll/ x"
)

func benchSignals(b *testing.B, text string) {
	h, err := DefaultHardCase()
	if err != nil {
		b.Fatal(err)
	}
	f := NewForms(text)
	in := SignalInput{Raw: text}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = h.Signals(f, in)
	}
}

func BenchmarkSignalsEdgeDigits(b *testing.B) { benchSignals(b, edgeLine) }
func BenchmarkSignalsCutFields(b *testing.B)  { benchSignals(b, cutLine) }

func TestSignalsCostGate(t *testing.T) {
	mult := int64(1)
	if testing.CoverMode() != "" {
		mult = 10
	}
	edge := fastestOf(BenchmarkSignalsEdgeDigits)
	cut := fastestOf(BenchmarkSignalsCutFields)
	t.Logf("edge digits: %d ns/op %d allocs/op; cut fields: %d ns/op %d allocs/op",
		edge.NsPerOp(), edge.AllocsPerOp(), cut.NsPerOp(), cut.AllocsPerOp())
	if edge.NsPerOp() > signalsEdgeLineGateNs*mult {
		t.Errorf("edge-digit line costs %d ns/op, over %d", edge.NsPerOp(), signalsEdgeLineGateNs*mult)
	}
	if edge.AllocsPerOp() > signalsAllocGate {
		t.Errorf("edge-digit line makes %d allocs/op, over %d", edge.AllocsPerOp(), signalsAllocGate)
	}
	if cut.NsPerOp() > signalsCutFieldGateNs*mult {
		t.Errorf("cut fields cost %d ns/op, over %d", cut.NsPerOp(), signalsCutFieldGateNs*mult)
	}
}
