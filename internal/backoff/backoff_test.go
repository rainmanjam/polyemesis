package backoff

import (
	"testing"
	"time"
)

func TestExponentialDoublesFromBaseAndStopsAtMax(t *testing.T) {
	s := time.Second
	for retries, want := range map[int]time.Duration{
		-3: s, 0: s, 1: 2 * s, 2: 4 * s, 3: 8 * s, 4: 10 * s, 1000: 10 * s,
	} {
		if got := Exponential(retries, s, 10*s); got != want {
			t.Errorf("Exponential(%d) = %v, want %v", retries, got, want)
		}
	}
	if got := Exponential(0, time.Minute, time.Second); got != time.Second {
		t.Errorf("a base above max = %v, want max", got)
	}
}
