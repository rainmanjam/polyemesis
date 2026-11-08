package pack

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"
)

// raidWord is a pronounceable word unique to n: alternating consonants and
// vowels, so the folding and token rules neither collapse nor merge it.
func raidWord(n int) string {
	const cons, vow = "bcdfghjklmnpqrstvwxz", "aeiou"
	n = n * 2654437 % 100000000 // spread neighbours; odd and not a multiple of 5, so distinct below 1e8
	var b []byte
	for k := 0; k < 4; k++ {
		b = append(b, cons[n%20], vow[n/20%5])
		n /= 100
	}
	return string(b)
}

// raidLine is a nine-word line whose words are unique to i, so no two lines
// share a token and none is similar to another.
func raidLine(i int) Forms {
	w := make([]string, 9)
	for j := range w {
		w[j] = raidWord(i*9 + j)
	}
	return NewForms(strings.Join(w, " "))
}

// Trimming an over-full table to exactly its cap makes every later arrival
// over the cap again, so each one re-sorts the whole table. It must trim to a
// low-water mark, which leaves room for the next arrivals.
func TestNearDupTrimsToALowWaterMark(t *testing.T) {
	d := NewNearDup(NearDupConfig{MaxAuthors: 100})
	f := NewForms("one message from many different people today")
	for i := 0; i <= 100; i++ {
		d.Observe(t0.Add(time.Duration(i)*time.Millisecond), fmt.Sprintf("twitch:%d", i), f)
	}
	if got, want := d.Tracked(), LowWater(100); got != want {
		t.Fatalf("after the cap was passed %d authors remain, want %d (the low-water mark)", got, want)
	}
	d.mu.Lock()
	_, newest := d.authors["twitch:100"]
	_, oldest := d.authors["twitch:0"]
	d.mu.Unlock()
	if !newest || oldest {
		t.Errorf("the trim must drop the least recently seen: newest kept=%v oldest kept=%v", newest, oldest)
	}
}

// The sketch is only a reject filter: it must never turn away a pair that
// Jaccard would accept.
func TestSketchNeverRejectsASimilarPair(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	vocab := make([]string, 40)
	for i := range vocab {
		vocab[i] = raidWord(i)
	}
	pick := func() tokenSet {
		s := tokenSet{}
		for n := 1 + rng.Intn(14); n > 0; n-- {
			s[vocab[rng.Intn(len(vocab))]] = struct{}{}
		}
		return s
	}
	for i := 0; i < 20000; i++ {
		a, b := pick(), pick()
		for _, sim := range []float64{0.5, 0.8, 0.95} {
			if jaccard(a, b) >= sim && !mayMatch(sketchOf(a), sketchOf(b), len(a), len(b), sim) {
				t.Fatalf("sketch rejected a pair with jaccard %.2f at threshold %.2f: %v %v", jaccard(a, b), sim, a, b)
			}
		}
	}
}

// Varying one word of nine is still the same raid (8 of 10 shared, exactly the
// threshold), and the sketch must not stop it clustering.
func TestRaidClustersLightlyVariedCopies(t *testing.T) {
	r := NewRaid(RaidConfig{})
	base := make([]string, 9)
	for i := range base {
		base[i] = raidWord(900 + i)
	}
	var hit bool
	for i := 0; i < 5; i++ {
		w := append([]string(nil), base...)
		w[8] = raidWord(5000 + i) // each raider changes the last word
		_, hit = r.Observe(t0.Add(time.Duration(i)*time.Second), fmt.Sprintf("twitch:r%d", i), NewForms(joinWords(w)), 0)
	}
	if !hit {
		t.Error("five accounts posting near copies were not a raid")
	}
	if r.Tracked() != 1 {
		t.Errorf("near copies made %d clusters, want 1", r.Tracked())
	}
}

func joinWords(w []string) string {
	s := ""
	for i, x := range w {
		if i > 0 {
			s += " "
		}
		s += x
	}
	return s
}
