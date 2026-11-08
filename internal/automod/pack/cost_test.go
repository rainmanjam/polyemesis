//go:build !race

package pack

import "testing"

// costGateNs is the ceiling for the whole per-message pack form and lookup.
// CheckFast runs on the chat adapter goroutine, and at a raid's 2,000 msgs/s
// 15 µs is about 3% of one core. The measured cost is around a third of this,
// so the gate catches an accidental quadratic or a per-token allocation, not
// scheduler noise. The file is not built under -race; coverage instrumentation
// (the Sonar run) slows every statement, so there the gate is ten times wider
// rather than skipped -- a quadratic still trips it.
const costGateNs = 15_000

func TestPackFormCostGate(t *testing.T) {
	gate := int64(costGateNs)
	if testing.CoverMode() != "" {
		gate *= 10
	}
	r := fastestOf(BenchmarkPackForm)
	ns := r.NsPerOp()
	t.Logf("pack form + lookup: %d ns/op, %d allocs/op", ns, r.AllocsPerOp())
	if ns > gate {
		t.Fatalf("pack form costs %d ns/op, over the %d ns gate", ns, gate)
	}
}

// fastestOf runs a benchmark three times and keeps the fastest run. The gates
// are about what the code costs, and a shared CI runner that is busy with the
// other packages' tests only ever adds time: one slow run measured 15.9 µs for a
// line that costs 8 µs alone. The fastest of three keeps that noise out without
// widening the gate, so a real quadratic, slow on every run, still trips it.
func fastestOf(fn func(*testing.B)) testing.BenchmarkResult {
	best := testing.Benchmark(fn)
	for i := 0; i < 2; i++ {
		if r := testing.Benchmark(fn); r.NsPerOp() < best.NsPerOp() {
			best = r
		}
	}
	return best
}
