//go:build !race

package pack

import "testing"

// A plain English chat line, the common case: no digits, no links, nothing that
// should reach the doxxing or link detectors.
const plainMessage = "that was an amazing play, did you see the last round? lol i cannot believe it"

func benchChecker(b *testing.B, text string) {
	c, err := New(Config{Shape: DefaultShapeLimits(), Names: NewNameSet([]string{"kestrel_demo", "tidewalker_mod"})})
	if err != nil {
		b.Fatal(err)
	}
	m := Message{Platform: "twitch", AuthorID: "1", AuthorName: "viewer_one", Text: text}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.Inspect(m)
	}
}

func BenchmarkCheckerPlain(b *testing.B) { benchChecker(b, plainMessage) }
func BenchmarkCheckerBusy(b *testing.B)  { benchChecker(b, benchMessage) }
func BenchmarkCheckerDigits(b *testing.B) {
	benchChecker(b, "score is 21-19 at 12:30:45, running v1.2.3.4, order 4412 7788")
}

// TestCheckerCostGate holds the whole stateless checker to the same 15 µs
// ceiling as the form alone: CheckFast runs on the adapter goroutine.
func TestCheckerCostGate(t *testing.T) {
	gate := int64(costGateNs)
	if testing.CoverMode() != "" {
		gate *= 10
	}
	for name, fn := range map[string]func(*testing.B){
		"plain": BenchmarkCheckerPlain, "busy": BenchmarkCheckerBusy, "digits": BenchmarkCheckerDigits,
	} {
		r := testing.Benchmark(fn)
		t.Logf("%s: %d ns/op, %d allocs/op", name, r.NsPerOp(), r.AllocsPerOp())
		if r.NsPerOp() > gate {
			t.Errorf("%s costs %d ns/op, over the %d ns gate", name, r.NsPerOp(), gate)
		}
	}
}
