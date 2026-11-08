package api

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/overlay"
)

// TestAHeldMessageStaysOffTheOverlayUntilReleasedFromTheReviewList drives the
// production chain: an adapter delivers into the real chat hub, a REAL automod
// engine with one flag-only, nothing-armed rule that sets holdOverlay finds it,
// the real gate withholds it, and the overlay's HTTP stream is read off a
// socket. The held line appears only after POST /chat/flags/release, once. The
// same words from a rule WITHOUT holdOverlay show at once.
func TestAHeldMessageStaysOffTheOverlayUntilReleasedFromTheReviewList(t *testing.T) {
	srv, h, sign, hub := overlayServer(t)
	s := serverUnderTest(t, h)
	gate := overlay.New(hub)
	s.overlayGate = gate
	rules, err := automod.NewRuleSet([]automod.Rule{
		{ID: 1, Name: "held", Enabled: true, Pattern: `maybeslur`, Action: automod.ActionFlag, HoldOverlay: true},
		{ID: 2, Name: "plain", Enabled: true, Pattern: `justflag`, Action: automod.ActionFlag},
	})
	if err != nil {
		t.Fatal(err)
	}
	ch := chat.New(chat.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(ch.Close)
	ch.SetDisplay(gate)
	ch.SetModerator(automod.New(automod.DefaultMatrix(), automod.PlatformCaps{}, rules, nil, nil))
	s.chat = ch
	tw := &chanAdapter{platform: db.PlatformTwitch, in: make(chan chat.Message, 8)}
	if err := ch.Attach(context.Background(), tw); err != nil {
		t.Fatal(err)
	}

	o := createOverlay(t, h, sign, "Twitch", []string{"twitch"})
	res := openOverlay(t, srv, "/api/v1/overlay/events", o.OBSKey)
	waitForListener(t, hub)
	br := bufio.NewReader(res.Body)

	viewer := chat.Author{ID: "u1", Name: "viewer"}
	tw.in <- chat.Message{ID: "held", Author: viewer, Text: "this is maybeslur ok"}
	tw.in <- chat.Message{ID: "flagonly", Author: viewer, Text: "this is justflag ok"}
	tw.in <- chat.Message{ID: "end", Author: viewer, Text: "SENTINEL"}
	frames := collectUntil(t, br, "SENTINEL")
	if len(frames) != 1 || frames[0].Env.Data.(map[string]any)["text"] != "this is justflag ok" {
		t.Fatalf("frames = %+v; want only the flag-only line", frames)
	}

	release := func(query string) *http.Request {
		r := jsonRequest(t, http.MethodPost, "/api/v1/chat/flags/release?"+query, nil)
		sign(r)
		return r
	}
	// Missing id: a client error, not a 404.
	if w := do(t, h, release("platform=twitch")); w.Code != http.StatusBadRequest {
		t.Fatalf("no id: %d %s", w.Code, w.Body.String())
	}
	// A line that was never held, and one that was only flagged.
	for _, id := range []string{"nope", "flagonly"} {
		if w := do(t, h, release("platform=twitch&account=acct&id="+id)); w.Code != http.StatusNotFound {
			t.Fatalf("release %s: %d %s", id, w.Code, w.Body.String())
		}
	}
	flagsBody := func() string {
		r := jsonRequest(t, http.MethodGet, "/api/v1/chat/flags", nil)
		sign(r)
		w := do(t, h, r)
		if w.Code != http.StatusOK {
			t.Fatalf("flags: %d %s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	if !strings.Contains(flagsBody(), `"held":true`) {
		t.Fatalf("the review list does not mark the held line: %s", flagsBody())
	}
	// The held one releases, and appears on the stream.
	if w := do(t, h, release("platform=twitch&account=acct&id=held")); w.Code != http.StatusOK {
		t.Fatalf("release: %d %s", w.Code, w.Body.String())
	}
	tw.in <- chat.Message{ID: "end2", Author: viewer, Text: "SENTINEL2"}
	frames = collectUntil(t, br, "SENTINEL2")
	if len(frames) != 1 || frames[0].Env.Data.(map[string]any)["text"] != "this is maybeslur ok" {
		t.Fatalf("frames after release = %+v", frames)
	}
	// Once only.
	if w := do(t, h, release("platform=twitch&account=acct&id=held")); w.Code != http.StatusNotFound {
		t.Fatalf("second release: %d", w.Code)
	}
	// FINDING 3: once released, the list stops offering it.
	if strings.Contains(flagsBody(), `"held":true`) {
		t.Fatalf("the review list still marks a released line held: %s", flagsBody())
	}
}

func TestReleaseWithNoOverlayGateIsNotFound(t *testing.T) {
	_, h, sign, _ := overlayServer(t)
	s := serverUnderTest(t, h)
	s.overlayGate = nil
	r := jsonRequest(t, http.MethodPost, "/api/v1/chat/flags/release?platform=twitch&id=x", nil)
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusNotFound {
		t.Fatalf("code %d", w.Code)
	}
}

// holdModerator holds every message on the fast pass and has the model on, its
// answer blocked until the test lets it through.
type holdModerator struct{ release chan struct{} }

func (holdModerator) CheckFast(_ db.Platform, sub automod.Subject) automod.Verdict {
	f := automod.Finding{Checker: automod.CheckerRules, Action: automod.ActionFlag, Hold: true}
	return automod.Verdict{Findings: []automod.Finding{f}, Hold: true}
}
func (m holdModerator) CheckModel(ctx context.Context, _ db.Platform, _ string) (automod.Verdict, error) {
	select {
	case <-m.release:
	case <-ctx.Done():
	}
	return automod.Verdict{}, nil
}
func (holdModerator) ModelEnabled() bool             { return true }
func (holdModerator) ModelStats() automod.ModelStats { return automod.ModelStats{} }

func releaseReq(t *testing.T, sign func(*http.Request), id string) *http.Request {
	t.Helper()
	r := jsonRequest(t, http.MethodPost, "/api/v1/chat/flags/release?platform=twitch&account=acct&id="+id, nil)
	sign(r)
	return r
}

// FINDING 2: refused (409) while the model is still being asked, accepted once
// it has answered.
func TestReleaseIsRefusedWhileTheModelIsStillAsking(t *testing.T) {
	_, h, sign, hub := overlayServer(t)
	s := serverUnderTest(t, h)
	s.overlayGate = overlay.New(hub)
	ch := chat.New(chat.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(ch.Close)
	ch.SetDisplay(s.overlayGate)
	mod := holdModerator{release: make(chan struct{})}
	ch.SetModerator(mod)
	s.chat = ch
	tw := &chanAdapter{platform: db.PlatformTwitch, in: make(chan chat.Message, 2)}
	if err := ch.Attach(context.Background(), tw); err != nil {
		t.Fatal(err)
	}
	tw.in <- chat.Message{ID: "h1", Author: chat.Author{ID: "u1", Name: "v"}, Text: "words"}

	deadline := time.Now().Add(3 * time.Second)
	for !ch.ModelPending(db.PlatformTwitch, "acct", "h1") {
		if time.Now().After(deadline) {
			t.Fatal("the model was never asked")
		}
		time.Sleep(5 * time.Millisecond)
	}
	w := do(t, h, releaseReq(t, sign, "h1"))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "model is still checking") {
		t.Fatalf("release during the model's question: %d %s", w.Code, w.Body.String())
	}
	close(mod.release)
	for ch.ModelPending(db.PlatformTwitch, "acct", "h1") {
		if time.Now().After(deadline) {
			t.Fatal("the question never cleared")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The model passed it as Clear, which does not touch a held line.
	if w := do(t, h, releaseReq(t, sign, "h1")); w.Code != http.StatusOK {
		t.Fatalf("release after the answer: %d %s", w.Code, w.Body.String())
	}
}

// FINDING 1: a line the gate holds but the hub no longer has -- it aged out of
// the ring, or its author was banned -- is not released.
func TestReleaseRefusesAMessageTheHubNoLongerHas(t *testing.T) {
	_, h, sign, hub := overlayServer(t)
	s := serverUnderTest(t, h)
	gate := overlay.New(hub)
	s.overlayGate = gate
	ch := chat.New(chat.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(ch.Close)
	s.chat = ch
	// Held in the gate directly: the hub never saw it, as if it had scrolled out.
	gate.Offer(chat.Message{ID: "old", Platform: db.PlatformTwitch, Account: "acct",
		Author: chat.Author{ID: "u1", Name: "v"}, Text: "old words"}, chat.DisplayHeld)
	w := do(t, h, releaseReq(t, sign, "old"))
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "no longer held by the chat") {
		t.Fatalf("release of a forgotten message: %d %s", w.Code, w.Body.String())
	}
	if gate.Stats().Released != 0 {
		t.Error("the gate published a line the hub no longer has")
	}
}
