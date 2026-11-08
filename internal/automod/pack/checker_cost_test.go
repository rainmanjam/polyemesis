//go:build !race

package pack

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

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

// ---- the windowed detectors, worst case

// A chain of near-copies: a sliding window of ten words, so each message is
// similar to its neighbours (9 of 11 shared words) but not to anything two steps
// away. That defeats the early exit a flat set of copies enjoys and makes the
// breadth-first search compare every pair, the worst input for it.
func BenchmarkNearDupWorst(b *testing.B) {
	d := NewNearDup(NearDupConfig{})
	forms := make([]Forms, 40)
	for i := range forms {
		var w []string
		for j := 0; j < 10; j++ {
			w = append(w, fmt.Sprintf("word%c%c", 'a'+(i+j)/26, 'a'+(i+j)%26))
		}
		forms[i] = NewForms(strings.Join(w, " "))
	}
	for i := 0; i < 23; i++ {
		d.Observe(t0.Add(time.Duration(i)*time.Millisecond), "twitch:spam", forms[i])
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Walk the chain forwards, so the newest is always similar to its
		// neighbour only and the 24 retained are a chain; the clock stays inside
		// the window.
		d.Observe(t0.Add(time.Second+time.Duration(i%1000)*time.Microsecond), "twitch:spam", forms[(23+i)%40])
	}
}

// A raid in full cry: MaxClusters live, distinct, same-length texts, and one
// more message that matches none of them.
func BenchmarkRaidWorst(b *testing.B) {
	r := NewRaid(RaidConfig{})
	for i := 0; i < r.cfg.MaxClusters; i++ {
		r.Observe(t0.Add(time.Duration(i)*time.Microsecond), fmt.Sprintf("a%d", i),
			NewForms(fmt.Sprintf("alpha%d bravo%d charlie%d delta%d echo%d", i, i, i, i, i)), 0)
	}
	probe := NewForms("completely different words appear in this one line")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Observe(t0.Add(time.Second), "probe", probe, 0)
	}
}

func TestWindowedCostGate(t *testing.T) {
	gate := int64(costGateNs)
	if testing.CoverMode() != "" {
		gate *= 10
	}
	for name, fn := range map[string]func(*testing.B){"near-dup": BenchmarkNearDupWorst, "raid": BenchmarkRaidWorst} {
		r := testing.Benchmark(fn)
		t.Logf("%s worst case: %d ns/op, %d allocs/op", name, r.NsPerOp(), r.AllocsPerOp())
		if r.NsPerOp() > gate {
			t.Errorf("%s worst case costs %d ns/op, over the %d ns gate", name, r.NsPerOp(), gate)
		}
	}
}
