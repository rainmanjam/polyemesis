package chat

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

const (
	ytForbidden403 = `{"error":{"code":403,"message":"forbidden","errors":[{"message":"x","domain":"youtube.liveChat","reason":"forbidden"}]}}`
	ytEnded403     = `{"error":{"code":403,"message":"ended","errors":[{"message":"x","domain":"youtube.liveChat","reason":"liveChatEnded"}]}}`
)

// hotLoopStub lists one active broadcast whose chat always refuses the poll.
func hotLoopStub(t *testing.T, pollBody string, polls, lists *atomic.Int64) *ytStub {
	return newYTStub(t, func(w http.ResponseWriter, r *http.Request, _ int64) {
		switch r.URL.Path {
		case "/liveBroadcasts":
			lists.Add(1)
			fmt.Fprint(w, `{"items":[{"snippet":{"liveChatId":"chat-1","title":"t"}}]}`)
		case "/liveChat/messages":
			polls.Add(1)
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, pollBody)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

// A 403 forbidden on the poll is the account's problem. It used to read as
// "chat closed", so Run rediscovered the same broadcast and was refused again
// with no sleep, burning the daily quota in about a second.
func TestYouTubeRunStopsOnAForbiddenPollInsteadOfSpinning(t *testing.T) {
	var polls, lists atomic.Int64
	stub := hotLoopStub(t, ytForbidden403, &polls, &lists)
	a := ytAdapter(t, stub.URL, func(c *YouTubeConfig) {
		c.Sleep = func(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil }
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := a.Run(ctx, SinkFunc(func(Message) {}))
	if !IsFatal(err) {
		t.Fatalf("Run = %v, want a fatal error", err)
	}
	if polls.Load() != 1 || lists.Load() != 1 {
		t.Errorf("polls=%d lists=%d, want 1 and 1", polls.Load(), lists.Load())
	}
	if a.Health().State != StateFailed {
		t.Errorf("state = %v, want failed", a.Health().State)
	}
}

// liveChatEnded while the broadcast is still listed as active must wait before
// rediscovering, and must not poll the chat that just ended a second time.
func TestYouTubeRunWaitsAfterAnEndedChatAndDoesNotRepollIt(t *testing.T) {
	var polls, lists atomic.Int64
	stub := hotLoopStub(t, ytEnded403, &polls, &lists)
	var sleeps atomic.Int64
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a := ytAdapter(t, stub.URL, func(c *YouTubeConfig) {
		c.Sleep = func(ctx context.Context, _ time.Duration) bool {
			if sleeps.Add(1) >= 6 {
				cancel()
			}
			return ctx.Err() == nil
		}
	})
	if err := a.Run(ctx, SinkFunc(func(Message) {})); err != nil {
		t.Fatalf("Run = %v", err)
	}
	if sleeps.Load() < 2 {
		t.Errorf("sleeps = %d, want a wait after the chat ended", sleeps.Load())
	}
	if polls.Load() != 1 {
		t.Errorf("polls = %d, want the ended chat polled once", polls.Load())
	}
}

// Kick health lines must not carry an HTML error page.
func TestKickSaidOmitsHTMLBodies(t *testing.T) {
	got := kickSaid(&apiError{Status: 502, Body: "<html><body>Bad gateway</body></html>"})
	if got != "502" {
		t.Errorf("kickSaid = %q, want just the status", got)
	}
	got = kickSaid(&apiError{Status: 400, Body: `{"x":1}`, Message: "bad slug"})
	if got != "400 bad slug" {
		t.Errorf("kickSaid = %q", got)
	}
}
