//go:build !race

package pack

import "testing"

// costGateNs is the ceiling for the whole per-message pack form and lookup.
// CheckFast runs on the chat adapter goroutine, and at a raid's 2,000 msgs/s
// 15 µs is about 3% of one core. The measured cost is around a third of this,
// so the gate catches an accidental quadratic or a per-token allocation, not
// scheduler noise. It is skipped under -race, which inflates everything.
const costGateNs = 15_000

func TestPackFormCostGate(t *testing.T) {
	if testing.Short() {
		t.Skip("benchmark gate skipped with -short")
	}
	r := testing.Benchmark(BenchmarkPackForm)
	ns := r.NsPerOp()
	t.Logf("pack form + lookup: %d ns/op, %d allocs/op", ns, r.AllocsPerOp())
	if ns > costGateNs {
		t.Fatalf("pack form costs %d ns/op, over the %d ns gate", ns, costGateNs)
	}
}
