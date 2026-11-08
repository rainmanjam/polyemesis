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
	benchCheckerNames(b, text, NewNameSet([]string{"kestrel_demo", "tidewalker_mod"}))
}

// learnedNames is a name set at the spec's cap of learned names, with folded
// lengths clustered around the author's own (nine), the worst spread for the
// edit-distance arm.
func learnedNames() *NameSet {
	ns := NewNameSet([]string{"kestrel_demo", "tidewalker_mod"})
	for i := 0; ns.Len() < DefaultMaxLearnedNames+2; i++ {
		ns.Learn(raidWord(i) + raidWord(i + 7)[:i%3])
	}
	return ns
}

func benchCheckerNames(b *testing.B, text string, names *NameSet) {
	c, err := New(Config{Shape: DefaultShapeLimits(), Names: names})
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

// A plain line from an author who matches no one, with 500 learned names: the
// impersonation check runs against every name it could be.
func BenchmarkCheckerManyNames(b *testing.B) { benchCheckerNames(b, plainMessage, learnedNames()) }
func BenchmarkCheckerBusy(b *testing.B)      { benchChecker(b, benchMessage) }
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
		"500-names": BenchmarkCheckerManyNames,
	} {
		r := fastestOf(fn)
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

// A raid in full cry, and the worst case for it: MaxClusters live clusters of
// the SAME size as the probe (so the size precheck rejects none of them), and a
// probe that differs on every call (so it never matches the cluster the last
// call created and stops there). Every message is ordinary busy chat of distinct
// nine-word lines, each one scanned against every live cluster.
func BenchmarkRaidWorst(b *testing.B) {
	r := NewRaid(RaidConfig{})
	for i := 0; i < r.cfg.MaxClusters; i++ {
		r.Observe(t0.Add(time.Duration(i)*time.Microsecond), fmt.Sprintf("a%d", i), raidLine(i), 0)
	}
	const ring = 8192 // longer than MaxClusters, so a probe's old cluster is gone when it returns
	probes := make([]Forms, ring)
	for i := range probes {
		probes[i] = raidLine(100000 + i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Observe(t0.Add(time.Second), "probe", probes[i%ring], 0)
	}
}

// The author table at its cap, in a bot raid: every message is a new author, so
// every message is over the cap.
func BenchmarkNearDupCapEvict(b *testing.B) {
	d := NewNearDup(NearDupConfig{})
	f := NewForms("this is some ordinary chat text")
	for i := 0; i < d.cfg.MaxAuthors; i++ {
		d.Observe(t0.Add(time.Duration(i)*time.Microsecond), fmt.Sprintf("twitch:u%d", i), f)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d.Observe(t0.Add(time.Second+time.Duration(i)*time.Microsecond), fmt.Sprintf("twitch:n%d", i), f)
	}
}

func TestWindowedCostGate(t *testing.T) {
	gate := int64(costGateNs)
	if testing.CoverMode() != "" {
		gate *= 10
	}
	for name, fn := range map[string]func(*testing.B){"near-dup": BenchmarkNearDupWorst, "raid": BenchmarkRaidWorst, "near-dup-at-cap": BenchmarkNearDupCapEvict} {
		r := fastestOf(fn)
		t.Logf("%s worst case: %d ns/op, %d allocs/op", name, r.NsPerOp(), r.AllocsPerOp())
		if r.NsPerOp() > gate {
			t.Errorf("%s worst case costs %d ns/op, over the %d ns gate", name, r.NsPerOp(), gate)
		}
	}
}
