package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// UX Council A48. Two reads the Chat page lacked: what automod flagged, and
// whether the viewer on the user card is under a ban or a timeout right now.

// speakingAdapter delivers its messages once and then holds the connection.
type speakingAdapter struct {
	moderatableAdapter
	say []chat.Message
}

func (a *speakingAdapter) Run(ctx context.Context, sink chat.Receiver) error {
	for _, m := range a.say {
		sink.Deliver(m)
	}
	<-ctx.Done()
	return nil
}

// flagText flags any message whose text is the given string.
type flagText struct{ text string }

func (f flagText) CheckFast(_ db.Platform, sub automod.Subject) automod.Verdict {
	text := sub.Text
	if text != f.text {
		return automod.Verdict{}
	}
	return automod.Verdict{Findings: []automod.Finding{{
		Checker: automod.CheckerRules, Action: automod.ActionFlag,
		Category: automod.CategorySpam, Reason: `matched rule "links"`,
		Note: "PLANTED-NOTE-A48",
	}}}
}
func (flagText) CheckModel(context.Context, db.Platform, string) (automod.Verdict, error) {
	return automod.Verdict{}, nil
}
func (flagText) ModelEnabled() bool             { return false }
func (flagText) ModelStats() automod.ModelStats { return automod.ModelStats{} }

type flagsResponse struct {
	Flags []struct {
		Message  chat.Message      `json:"message"`
		Findings []automod.Finding `json:"findings"`
		Acted    bool              `json:"acted"`
	} `json:"flags"`
	Truncated bool           `json:"truncated"`
	Note      string         `json:"note"`
	Counts    map[string]int `json:"counts"`
}

func TestChatFlagsListsWhatAutomodFlagged(t *testing.T) {
	s, h, _ := testServer(t, config.Config{})
	hub := chat.New()
	t.Cleanup(hub.Close)
	s.chat = hub
	hub.SetModerator(flagText{text: "cheap followers here"})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a := &speakingAdapter{
		moderatableAdapter: moderatableAdapter{platform: db.PlatformTwitch, account: "acct"},
		say: []chat.Message{
			{ID: "ok", Author: chat.Author{ID: "u0", Name: "fine"}, Text: "hello"},
			{ID: "bad", Author: chat.Author{ID: "u1", Name: "spammer"}, Text: "cheap followers here"},
		},
	}
	if err := hub.Attach(ctx, a); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	sign := login(t, h)

	var out flagsResponse
	deadline := time.Now().Add(2 * time.Second)
	for {
		raw := send(t, h, sign, http.MethodGet, "/api/v1/chat/flags", nil, http.StatusOK)
		if strings.Contains(string(raw), "PLANTED-NOTE-A48") {
			t.Fatalf("the model's note reached a client through /chat/flags: %s", raw)
		}
		decodeInto(t, raw, &out)
		if len(out.Flags) > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(out.Flags) != 1 {
		t.Fatalf("flags = %+v, want the one flagged message", out.Flags)
	}
	f := out.Flags[0]
	if f.Message.ID != "bad" || f.Message.Author.ID != "u1" || f.Message.Text != "cheap followers here" {
		t.Fatalf("flagged message = %+v", f.Message)
	}
	if len(f.Findings) != 1 || f.Findings[0].Category != automod.CategorySpam || f.Findings[0].Reason == "" {
		t.Fatalf("findings = %+v", f.Findings)
	}
	// review #3/#9: per-platform counts over the whole held list, independent
	// of limit, so the chip can count without fetching every entry.
	var small flagsResponse
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/chat/flags?limit=1", nil, http.StatusOK), &small)
	if small.Counts["twitch"] != 1 || len(small.Counts) != 1 {
		t.Errorf("counts = %v, want twitch 1", small.Counts)
	}
	if out.Note == "" {
		t.Error("no note: an empty list would read as \"automod never flagged anything\"")
	}

	// Narrowed to a platform it has nothing on, the list is empty -- and still
	// a list, so a client can render it without a null check.
	raw := send(t, h, sign, http.MethodGet, "/api/v1/chat/flags?platform=youtube", nil, http.StatusOK)
	if !strings.Contains(string(raw), `"flags":[]`) {
		t.Fatalf("a filtered-out list is not an empty array: %s", raw)
	}
}

func TestChatFlagsWithNoChatIsAnEmptyList(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)
	raw := send(t, h, sign, http.MethodGet, "/api/v1/chat/flags", nil, http.StatusOK)
	if !strings.Contains(string(raw), `"flags":[]`) {
		t.Fatalf("no chat hub answered %s, want an empty flags array", raw)
	}
	if !strings.Contains(string(raw), `"counts":{}`) {
		t.Fatalf("no chat hub answered %s, want an empty counts object", raw)
	}
}

type cardWithSanction struct {
	Sanction *struct {
		Action string     `json:"action"`
		Until  *time.Time `json:"until"`
		By     string     `json:"by"`
	} `json:"sanction"`
}

func readCard(t *testing.T, h http.Handler, sign func(*http.Request), q string) cardWithSanction {
	t.Helper()
	var c cardWithSanction
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/chat/users?"+q, nil, http.StatusOK), &c)
	return c
}

// THE behaviour the finding is about. The card says what is in force, and what
// it says follows the bans made through polyemesis.
func TestTheUserCardCarriesTheViewersSanction(t *testing.T) {
	h, sign, _, _ := moderationFixture(t)

	if c := readCard(t, h, sign, "platform=twitch&account=acct&authorId=u-1"); c.Sanction == nil || c.Sanction.Action != "none" {
		t.Fatalf("an unsanctioned viewer's card carries %+v, want action \"none\"", c.Sanction)
	}

	send(t, h, sign, http.MethodPost, "/api/v1/chat/bans?platform=twitch&account=acct&userId=u-1&seconds=600", nil, http.StatusOK)
	c := readCard(t, h, sign, "platform=twitch&account=acct&authorId=u-1")
	if c.Sanction == nil || c.Sanction.Action != "timeout" || c.Sanction.Until == nil {
		t.Fatalf("after a 10-minute timeout the card carries %+v, want a timeout with an expiry", c.Sanction)
	}
	if left := time.Until(*c.Sanction.Until); left < 9*time.Minute || left > 11*time.Minute {
		t.Errorf("the timeout expires in %v, want about 10 minutes", left)
	}
	if c.Sanction.By != "operator" {
		t.Errorf("by = %q, want operator", c.Sanction.By)
	}

	send(t, h, sign, http.MethodPost, "/api/v1/chat/bans?platform=twitch&account=acct&userId=u-1", nil, http.StatusOK)
	if c := readCard(t, h, sign, "platform=twitch&authorId=u-1"); c.Sanction == nil || c.Sanction.Action != "ban" || c.Sanction.Until != nil {
		t.Fatalf("after a permanent ban the card carries %+v, want a ban with no expiry", c.Sanction)
	}

	send(t, h, sign, http.MethodDelete, "/api/v1/chat/bans?platform=twitch&account=acct&userId=u-1", nil, http.StatusOK)
	if c := readCard(t, h, sign, "platform=twitch&account=acct&authorId=u-1"); c.Sanction == nil || c.Sanction.Action != "none" {
		t.Fatalf("after lifting, the card carries %+v, want action \"none\"", c.Sanction)
	}
}

// An expired timeout is no sanction at all.
func TestAnExpiredTimeoutReadsAsNone(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	// The Hub's clock is an hour behind, so the 60-second timeout it records
	// ended long before the card reads it.
	hub := chat.New(chat.WithStore(store), chat.WithClock(func() time.Time { return time.Now().Add(-time.Hour) }))
	t.Cleanup(hub.Close)
	s.chat = hub
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := hub.Attach(ctx, &moderatableAdapter{platform: db.PlatformTwitch, account: "acct"}); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	sign := login(t, h)

	send(t, h, sign, http.MethodPost, "/api/v1/chat/bans?platform=twitch&account=acct&userId=u-9&seconds=60", nil, http.StatusOK)
	if c := readCard(t, h, sign, "platform=twitch&account=acct&authorId=u-9"); c.Sanction == nil || c.Sanction.Action != "none" {
		t.Fatalf("an expired timeout reads as %+v, want action \"none\"", c.Sanction)
	}
}
