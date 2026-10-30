package polls

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/eventstream"
	"github.com/rainmanjam/polyemesis/internal/eventstream/streamtest"
)

// liveAdapter is a chat adapter a test feeds by hand, which can also delete
// and ban -- so moderation takes the hub's real path, not a call on the
// engine.
type liveAdapter struct {
	platform db.Platform
	in       chan chat.Message
}

func (a *liveAdapter) Platform() db.Platform { return a.platform }
func (a *liveAdapter) Account() string       { return "acct" }
func (a *liveAdapter) Run(ctx context.Context, sink chat.Sink) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case m := <-a.in:
			sink.Deliver(m)
		}
	}
}
func (a *liveAdapter) Delete(context.Context, string) error                     { return nil }
func (a *liveAdapter) Ban(context.Context, string, time.Duration, string) error { return nil }
func (a *liveAdapter) Unban(context.Context, string) error                      { return nil }

// TestTheChatHubDrivesAPollEndToEnd is the emission test for all four poll
// types, and the proof that the counting rules hold on the REAL path: the chat
// hub delivering, automod's verdict reaching the tap, an operator's delete and
// ban going through the hub's own moderation methods.
func TestTheChatHubDrivesAPollEndToEnd(t *testing.T) {
	stream := eventstream.NewHub()
	sub := streamtest.Subscribe(t, stream)
	ch := chat.New(chat.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(ch.Close)
	store := &memStore{}
	var mu sync.Mutex
	now := time.Unix(1_800_000_000, 0)
	clk := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	e := New(store, stream, WithClock(clk))
	t.Cleanup(e.Wait)
	ch.SetTap(e)

	tw := &liveAdapter{platform: db.PlatformTwitch, in: make(chan chat.Message, 16)}
	yt := &liveAdapter{platform: db.PlatformYouTube, in: make(chan chat.Message, 16)}
	for _, a := range []*liveAdapter{tw, yt} {
		if err := ch.Attach(context.Background(), a); err != nil {
			t.Fatal(err)
		}
	}

	r, err := e.Start(Spec{Question: "Next map?", Options: []string{"Dust", "Mirage"}, DurationSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	started := streamtest.Expect(t, sub, eventstream.TypePollStarted)
	if asResult(t, started).ID != r.ID {
		t.Errorf("poll.started = %+v", started.Data)
	}
	if d := streamtest.Expect(t, sub, eventstream.TypePollDisplay).Data.(eventstream.PollDisplay); d.Question != "Next map?" || len(d.Options) != 2 {
		t.Errorf("poll.display = %+v", d)
	}

	say := func(a *liveAdapter, id, author, text string) {
		a.in <- chat.Message{ID: id, Author: chat.Author{ID: author, Name: author}, Text: text}
	}
	say(tw, "t1", "alice", "1")
	say(tw, "t2", "bob", "2")
	say(tw, "t3", "alice", "!vote 2") // alice changes her mind
	say(yt, "y1", "alice", "1")       // a different alice
	say(tw, "t4", "mallory", "1")
	say(yt, "y2", "carol", "2")
	waitUntil(t, "six votes offered", func() bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.cur != nil && len(e.cur.votes) == 5
	})
	// Operator moderation through the hub: carol's message is deleted,
	// mallory is banned.
	if err := ch.Delete(context.Background(), db.PlatformYouTube, "acct", "y2"); err != nil {
		t.Fatal(err)
	}
	if err := ch.Ban(context.Background(), db.PlatformTwitch, "acct", "mallory", 0, "spam"); err != nil {
		t.Fatal(err)
	}
	say(tw, "t5", "mallory", "2") // ignored: banned

	mu.Lock()
	now = now.Add(time.Second)
	mu.Unlock()
	waitUntil(t, "a poll.vote with the moderated count", func() bool {
		e.Tick()
		c, _ := e.Current()
		return c.Total == 3
	})
	if v := asResult(t, streamtest.Expect(t, sub, eventstream.TypePollVote)); v.Total != 3 {
		t.Errorf("poll.vote carried %d votes, want 3", v.Total)
	}

	if _, err := e.End(r.ID); err != nil {
		t.Fatal(err)
	}
	final := asResult(t, streamtest.Expect(t, sub, eventstream.TypePollEnded))
	// alice(twitch) 2, bob 2, alice(youtube) 1; carol deleted, mallory banned.
	if !eq(votes(final), []int{1, 2}) || final.ByPlatform["twitch"] != 2 || final.ByPlatform["youtube"] != 1 {
		t.Errorf("final = %v by %v", votes(final), final.ByPlatform)
	}
}

// asResult reads a poll.* payload back. The events hub's redaction pass turns
// it into a JSON tree, which is what a subscriber receives on the wire.
func asResult(t *testing.T, env eventstream.Envelope) Result {
	t.Helper()
	b, err := json.Marshal(env.Data)
	if err != nil {
		t.Fatal(err)
	}
	var r Result
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
