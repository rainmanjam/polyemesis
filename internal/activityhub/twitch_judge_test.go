package activityhub

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/activity"
	"github.com/rainmanjam/polyemesis/internal/twitch/eventsub"
)

// judgeAPIStatus and judgeSubscribeError decide what a refused subscribe means
// for one kind: the kind's state, the detail the operator reads, and whether to
// try again. Each row here is one of Twitch's answers and the decision it must
// produce; a wrong row shows up as a kind that is silently "unknown" forever or
// as a retry storm against a limit Twitch has already said is full.

func judgeSpec() eventsub.Spec { return eventsub.Catalogue()[0] }

func judgeTwitch(t *testing.T) (*Twitch, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	return NewTwitch(TwitchConfig{AccountRef: "1", Log: slog.New(slog.NewTextHandler(&logs, nil))}), &logs
}

func TestJudgeAPIStatusDecidesEachHelixAnswer(t *testing.T) {
	tests := []struct {
		name      string
		ae        *eventsub.APIError
		retryIn   time.Duration // when set, RetryAt is stamped just before the call
		attempt   int
		wantWait  time.Duration // exact wait; checked only when wantRetry and waitNear is zero
		waitNear  time.Duration // when non-zero, wait must be within (waitNear-200ms, waitNear]
		wantRetry bool
		wantState activity.KindState
		wantWhy   string
	}{
		{name: "403 is a refusal, not a retry", ae: &eventsub.APIError{Status: http.StatusForbidden},
			wantState: activity.KindUnavailable, wantWhy: detailRefused},
		{name: "410 means the type is gone", ae: &eventsub.APIError{Status: http.StatusGone},
			wantState: activity.KindUnavailable, wantWhy: detailGone},
		{name: "409 retries after a second and says another session holds it", ae: &eventsub.APIError{Status: http.StatusConflict},
			wantWait: time.Second, wantRetry: true, wantState: activity.KindUnknown, wantWhy: detailAnotherSes},
		{name: "429 with a reset 2s away waits it out on the first attempt", ae: &eventsub.APIError{Status: http.StatusTooManyRequests},
			retryIn: 2 * time.Second, waitNear: 2 * time.Second, wantRetry: true},
		{name: "429 on a later attempt does not wait again", ae: &eventsub.APIError{Status: http.StatusTooManyRequests},
			retryIn: 2 * time.Second, attempt: 1, wantState: activity.KindUnavailable, wantWhy: detailLimit},
		{name: "429 whose reset is already past is a limit", ae: &eventsub.APIError{Status: http.StatusTooManyRequests, RetryAt: time.Now().Add(-time.Second)},
			wantState: activity.KindUnavailable, wantWhy: detailLimit},
		{name: "429 whose reset is beyond the 5s window is a limit", ae: &eventsub.APIError{Status: http.StatusTooManyRequests, RetryAt: time.Now().Add(time.Minute)},
			wantState: activity.KindUnavailable, wantWhy: detailLimit},
		{name: "500 is transient and retried", ae: &eventsub.APIError{Status: http.StatusInternalServerError},
			wantWait: 500 * time.Millisecond, wantRetry: true},
		{name: "503 is transient and retried", ae: &eventsub.APIError{Status: http.StatusServiceUnavailable},
			wantWait: 500 * time.Millisecond, wantRetry: true},
		{name: "400 is refused as asked", ae: &eventsub.APIError{Status: http.StatusBadRequest},
			wantState: activity.KindUnavailable, wantWhy: detailRefused},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src, _ := judgeTwitch(t)
			if tc.retryIn != 0 {
				tc.ae.RetryAt = time.Now().Add(tc.retryIn)
			}
			var o subOutcome
			wait, retry := src.judgeAPIStatus(judgeSpec(), tc.ae, tc.attempt, &o)
			if retry != tc.wantRetry {
				t.Fatalf("retry = %v, want %v", retry, tc.wantRetry)
			}
			switch {
			case tc.waitNear != 0:
				if wait > tc.waitNear || wait <= tc.waitNear-500*time.Millisecond {
					t.Errorf("wait = %v, want about %v", wait, tc.waitNear)
				}
			case wait != tc.wantWait:
				t.Errorf("wait = %v, want %v", wait, tc.wantWait)
			}
			if tc.wantState != "" && o.state != tc.wantState {
				t.Errorf("state = %q, want %q", o.state, tc.wantState)
			}
			if tc.wantWhy != "" && o.why != tc.wantWhy {
				t.Errorf("why = %q, want %q", o.why, tc.wantWhy)
			}
			if o.fatal != nil {
				t.Errorf("a Helix status must never be fatal, got %v", o.fatal)
			}
		})
	}
}

func TestJudgeAPIStatusWarnsWhenTwitchRefusesAsked(t *testing.T) {
	src, logs := judgeTwitch(t)
	var o subOutcome
	src.judgeAPIStatus(judgeSpec(), &eventsub.APIError{Status: http.StatusBadRequest}, 0, &o)
	if !strings.Contains(logs.String(), "a subscription was refused") {
		t.Errorf("a 400 must be logged for the operator; log = %q", logs.String())
	}
}

func TestJudgeSubscribeErrorClassifiesErrors(t *testing.T) {
	t.Run("a dead grant is fatal and not retried", func(t *testing.T) {
		src, _ := judgeTwitch(t)
		var o subOutcome
		wait, retry := src.judgeSubscribeError(judgeSpec(), &eventsub.APIError{Status: http.StatusUnauthorized}, 0, &o)
		if retry || wait != 0 {
			t.Fatalf("wait,retry = %v,%v; want 0,false", wait, retry)
		}
		if o.fatal == nil {
			t.Fatal("a 401 is a dead grant and must set o.fatal")
		}
	})
	t.Run("a non-API error is a transport failure: retried briefly", func(t *testing.T) {
		src, _ := judgeTwitch(t)
		var o subOutcome
		wait, retry := src.judgeSubscribeError(judgeSpec(), errors.New("connection reset"), 0, &o)
		if !retry || wait != 500*time.Millisecond {
			t.Fatalf("wait,retry = %v,%v; want 500ms,true", wait, retry)
		}
		if o.fatal != nil || o.state != "" {
			t.Errorf("a transport error must not decide the kind: state=%q fatal=%v", o.state, o.fatal)
		}
	})
	t.Run("an API error is routed to the status judge", func(t *testing.T) {
		src, _ := judgeTwitch(t)
		var o subOutcome
		wait, retry := src.judgeSubscribeError(judgeSpec(), &eventsub.APIError{Status: http.StatusGone}, 0, &o)
		if retry || wait != 0 || o.state != activity.KindUnavailable || o.why != detailGone {
			t.Errorf("410 via judgeSubscribeError = (%v,%v) state=%q why=%q", wait, retry, o.state, o.why)
		}
	})
}

func TestNoteSubCostLogsOnlyWhenTwitchCharges(t *testing.T) {
	src, logs := judgeTwitch(t)
	src.noteSubCost(judgeSpec(), eventsub.SubResult{})
	if logs.Len() != 0 {
		t.Fatalf("a free subscription must log nothing; got %q", logs.String())
	}
	src.noteSubCost(judgeSpec(), eventsub.SubResult{Cost: 2, TotalCost: 5, MaxTotalCost: 10})
	if !strings.Contains(logs.String(), "a subscription has a cost") || !strings.Contains(logs.String(), "cost=2") {
		t.Errorf("a charged subscription must be logged with its cost; got %q", logs.String())
	}
}

func TestRecheckTierReadsTheAccountTypeAgain(t *testing.T) {
	t.Run("unchanged account type keeps running", func(t *testing.T) {
		f := newFakeTwitch(t)
		src := NewTwitch(twConfig(f, io.Discard))
		done, err := src.recheckTier(context.Background(), "affiliate")
		if done || err != nil {
			t.Fatalf("recheckTier = %v, %v; want false, nil", done, err)
		}
	})
	t.Run("a changed account type ends run so subscriptions are made again", func(t *testing.T) {
		f := newFakeTwitch(t)
		src := NewTwitch(twConfig(f, io.Discard))
		done, err := src.recheckTier(context.Background(), "partner")
		if !done || err == nil {
			t.Fatalf("recheckTier = %v, %v; want true with an error", done, err)
		}
	})
	t.Run("a transient read failure is read again at the next tick", func(t *testing.T) {
		f := newFakeTwitch(t)
		f.userCode = http.StatusInternalServerError
		src := NewTwitch(twConfig(f, io.Discard))
		done, err := src.recheckTier(context.Background(), "affiliate")
		if done || err != nil {
			t.Fatalf("recheckTier = %v, %v; want false, nil", done, err)
		}
	})
	t.Run("a dead grant on read parks until ctx ends", func(t *testing.T) {
		f := newFakeTwitch(t)
		f.userCode = http.StatusUnauthorized
		src := NewTwitch(twConfig(f, io.Discard))
		// The read must finish while ctx is live, so ctx is cancelled only once
		// the park has run. parkUntilReconnected is the one place that marks a
		// kind Reconnect after a dead grant, so that state is the signal.
		for k, st := range src.kindsSnapshot() {
			if st.State == activity.KindReconnect {
				t.Fatalf("precondition: kind %v already Reconnect before the read", k)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		go func() {
			for {
				for _, st := range src.kindsSnapshot() {
					if st.State == activity.KindReconnect {
						cancel()
						return
					}
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(2 * time.Millisecond):
				}
			}
		}()
		done, err := src.recheckTier(ctx, "affiliate")
		if !done || !errors.Is(err, context.Canceled) {
			t.Fatalf("recheckTier = %v, %v; want true, context.Canceled", done, err)
		}
	})
}
