//go:build !race

package pack

import (
	"math"
	"strings"
	"testing"
	"unicode"
)

// costGateNs is the ceiling for the whole per-message pack form and lookup.
// CheckFast runs on the chat adapter goroutine, and at a raid's 2,000 msgs/s
// 15 µs is about 3% of one core. The measured cost is around a third of this,
// so the gate catches an accidental quadratic or a per-token allocation, not
// scheduler noise. The file is not built under -race; coverage instrumentation
// (the Sonar run) slows every statement, so there the gate is ten times wider
// rather than skipped -- a quadratic still trips it.
const costGateNs = 15_000

func TestPackFormCostGate(t *testing.T) {
	r, gate := measure(t, costGateNs, BenchmarkPackForm)
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

// calibrationRefNs is what calibrationWork costs on the machine the gates were
// set on (an Apple M-series laptop, about 6.9 µs). The 15 µs budget is a budget on that
// hardware: a shared CI runner measured the same checker at about twice the
// time, every run, so an absolute gate there fails on the runner, not on the
// code.
const calibrationRefNs = 6_900

// calibrationWork is a fixed workload shaped like the checker's -- splitting a
// chat line, lower-casing runes and probing a small map -- and sharing none of
// its code, so a regression in the pack cannot also slow the yardstick.
func calibrationWork(b *testing.B) {
	words := map[string]bool{"amazing": true, "round": true, "believe": true, "play": true}
	line := "that was an amazing play, did you see the last round? lol i cannot believe it"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		n := 0
		for _, f := range strings.Fields(line) {
			if words[strings.ToLower(strings.Trim(f, ",.?!"))] {
				n++
			}
		}
		for j := 0; j < 20; j++ {
			for _, f := range strings.FieldsFunc(line, unicode.IsSpace) {
				n += len(f)
			}
		}
		_ = n
	}
}

// gateHeadroom is the slack on a machine-scaled gate. A calibration run and a
// measurement are two samples of a machine whose load moves, and a gate that
// must hold between them needs a little room. A real quadratic costs many times
// the budget, not a quarter more.
const gateHeadroom = 1.25

// measure runs fn and returns its fastest result with the gate it is held to,
// scaled to this machine by a calibration taken RIGHT NEXT TO it: one before
// and one after, the slower of the two, so a load spike during the benchmark
// raises the gate by as much as it raised the benchmark.
//
// It used to calibrate once per test and then measure each benchmark in turn.
// Load changes between the two (the whole suite's other packages ran beside
// this one), so a quiet calibration set a tight gate that a busy measurement
// then exceeded: digits cost 22,514 ns against a 21,121 ns gate on CI.
//
// The scale is clamped to 1..4 as before, so no runner is slow enough to hide a
// quadratic, and widened tenfold under coverage instrumentation, which slows
// every statement.
func measure(t *testing.T, budget int64, fn func(*testing.B)) (testing.BenchmarkResult, int64) {
	t.Helper()
	before := fastestOf(calibrationWork).NsPerOp()
	r := fastestOf(fn)
	after := fastestOf(calibrationWork).NsPerOp()
	cal := before
	if after > cal {
		cal = after
	}
	scale := math.Min(math.Max(float64(cal)/calibrationRefNs, 1), 4)
	if testing.CoverMode() != "" {
		scale *= 10
	}
	g := int64(float64(budget) * scale * gateHeadroom)
	t.Logf("time gate %d ns (budget %d ns x %.2f for this machine x %.2f headroom)", g, budget, scale, gateHeadroom)
	return r, g
}
