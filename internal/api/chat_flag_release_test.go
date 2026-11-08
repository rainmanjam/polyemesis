package api

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

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
	// The review list marks it held, which is what shows the button.
	w := do(t, h, func() *http.Request {
		r := jsonRequest(t, http.MethodGet, "/api/v1/chat/flags", nil)
		sign(r)
		return r
	}())
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"held":true`) {
		t.Fatalf("flags list lacks the held marker: %d %s", w.Code, w.Body.String())
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
