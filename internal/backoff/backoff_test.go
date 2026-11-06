package backoff

import (
	"context"
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

func TestSleepCtxZeroWaitIsAnsweredByTheContextAlone(t *testing.T) {
	done, cancel := context.WithCancel(context.Background())
	cancel()
	for range 50 {
		if SleepCtx(done, 0) {
			t.Fatal("a done context reported a completed wait")
		}
		if !SleepCtx(context.Background(), -time.Second) {
			t.Fatal("no wait on a live context must report completed")
		}
	}
}

func TestSleepCtxReturnsPromptlyWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	start := time.Now()
	if SleepCtx(ctx, time.Minute) {
		t.Fatal("reported a completed wait after cancel")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("did not return promptly")
	}
	if !SleepCtx(context.Background(), time.Millisecond) {
		t.Fatal("a short wait on a live context must complete")
	}
}
