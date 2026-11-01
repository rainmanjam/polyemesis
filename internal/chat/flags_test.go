package chat

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// UX Council A48: automod's findings were written to the log and nowhere else,
// so the Chat page had no way to show what automod had flagged. These pin the
// Hub's review list, which GET /chat/flags reads.

func TestAFlaggedMessageIsHeldForReview(t *testing.T) {
	h := testHub(t)
	a := &fakeAdapter{
		platform: db.PlatformTwitch, account: "acct",
		messages: []Message{
			{ID: "ok", Author: Author{ID: "u0", Name: "fine"}, Text: "hello"},
			{ID: "m1", Author: Author{ID: "u1", Name: "ana"}, Text: "buy followers"},
		},
	}
	h.SetModerator(&flagOnly{text: "buy followers", finding: automod.Finding{
		Checker: automod.CheckerRules, Action: automod.ActionFlag,
		Category: automod.CategorySpam, Reason: `matched rule "spam"`, RuleID: 7,
	}})
	attach(t, h, a)

	waitFor(t, "the flag to be held", func() bool { return len(h.Flagged("", 10)) == 1 })
	got := h.Flagged("", 10)[0]
	if got.Message.ID != "m1" || got.Message.Text != "buy followers" || got.Message.Author.Name != "ana" {
		t.Fatalf("held %+v, want message m1 from ana with its text", got.Message)
	}
	if len(got.Findings) != 1 || got.Findings[0].RuleID != 7 || got.Findings[0].Reason != `matched rule "spam"` {
		t.Fatalf("findings = %+v, want the one rule finding", got.Findings)
	}
	if got.Acted {
		t.Error("a finding the matrix permitted nothing for was reported as acted on")
	}
	if got.At.IsZero() {
		t.Error("a held flag has no time")
	}
}

// #495, held on the new surface. Finding.Note is the model's prose -- possibly
// a viewer's words in the model's voice -- and must not reach any client.
func TestAFlagNeverCarriesTheModelsNote(t *testing.T) {
	const planted = "PLANTED-MODEL-NOTE-a48"
	h := testHub(t)
	a := &fakeAdapter{
		platform: db.PlatformTwitch, account: "acct",
		messages: []Message{{ID: "m1", Author: Author{ID: "u1"}, Text: "x"}},
	}
	h.SetModerator(&flagOnly{text: "x", finding: automod.Finding{
		Checker: automod.CheckerModel, Action: automod.ActionFlag, Note: planted,
	}})
	attach(t, h, a)

	waitFor(t, "the flag to be held", func() bool { return len(h.Flagged("", 10)) == 1 })
	raw, err := json.Marshal(h.Flagged("", 10))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), planted) {
		t.Fatalf("the model's note reached the flag list's JSON: %s", raw)
	}
}

func TestFlaggedFiltersByPlatformAndListsNewestFirst(t *testing.T) {
	h := testHub(t)
	tw := &fakeAdapter{platform: db.PlatformTwitch, account: "a", messages: []Message{
		{ID: "t1", Author: Author{ID: "u1"}, Text: "bad", At: time.UnixMilli(1000)},
	}}
	yt := &fakeAdapter{platform: db.PlatformYouTube, account: "b", messages: []Message{
		{ID: "y1", Author: Author{ID: "u2"}, Text: "bad", At: time.UnixMilli(2000)},
	}}
	h.SetModerator(&flagOnly{text: "bad", finding: automod.Finding{
		Checker: automod.CheckerRules, Action: automod.ActionFlag,
	}})
	attach(t, h, tw)
	waitFor(t, "the twitch flag", func() bool { return len(h.Flagged("", 10)) == 1 })
	attach(t, h, yt)
	waitFor(t, "both flags", func() bool { return len(h.Flagged("", 10)) == 2 })

	all := h.Flagged("", 10)
	if all[0].Message.ID != "y1" || all[1].Message.ID != "t1" {
		t.Fatalf("order = %s, %s; want newest (y1) first", all[0].Message.ID, all[1].Message.ID)
	}
	only := h.Flagged(db.PlatformTwitch, 10)
	if len(only) != 1 || only[0].Message.ID != "t1" {
		t.Fatalf("twitch filter returned %+v", only)
	}
	if n := len(h.Flagged("", 1)); n != 1 {
		t.Fatalf("limit 1 returned %d", n)
	}
}

// The model answers after the fast checkers, about the SAME message. Its finding
// joins the existing entry rather than counting the message twice -- the chip's
// number is messages, not opinions.
func TestAModelFindingJoinsTheMessagesExistingFlag(t *testing.T) {
	h := testHub(t)
	a := &fakeAdapter{
		platform: db.PlatformTwitch, account: "acct",
		messages: []Message{{ID: "m1", Author: Author{ID: "u1"}, Text: "hmm"}},
	}
	rule := automod.Finding{Checker: automod.CheckerRules, Action: automod.ActionFlag}
	model := automod.Finding{Checker: automod.CheckerModel, Action: automod.ActionFlag, Category: automod.CategoryHarassment}
	h.SetModerator(&stubModerator{
		verdict:      automod.Verdict{Findings: []automod.Finding{rule}},
		modelOn:      true,
		modelVerdict: automod.Verdict{Findings: []automod.Finding{model}},
	})
	attach(t, h, a)

	waitFor(t, "both findings", func() bool {
		f := h.Flagged("", 10)
		return len(f) == 1 && len(f[0].Findings) == 2
	})
	if n := len(h.Flagged("", 10)); n != 1 {
		t.Fatalf("one message was held %d times", n)
	}
}

// A message nothing was found in is not a flag.
func TestACleanMessageIsNotHeld(t *testing.T) {
	h := testHub(t)
	a := &fakeAdapter{platform: db.PlatformTwitch, account: "acct",
		messages: []Message{{ID: "m1", Author: Author{ID: "u1"}, Text: "hi"}}}
	h.SetModerator(&stubModerator{})
	attach(t, h, a)
	waitFor(t, "the message", func() bool { return len(h.History(10)) == 1 })
	if n := len(h.Flagged("", 10)); n != 0 {
		t.Fatalf("a clean message was held: %d flags", n)
	}
}

// The list is bounded: a raid of flagged spam must not grow it without end.
func TestTheFlagListIsBounded(t *testing.T) {
	h := testHub(t)
	for i := 0; i < flagCap+25; i++ {
		h.noteFlag(Message{Platform: db.PlatformTwitch, ID: string(rune('a'+i%26)) + strings.Repeat("x", i)},
			[]automod.Finding{{Checker: automod.CheckerRules, Action: automod.ActionFlag}}, false)
	}
	if n := len(h.Flagged("", 0)); n != flagCap {
		t.Fatalf("held %d flags, want the cap of %d", n, flagCap)
	}
}

func TestAFlagThatActedSaysSo(t *testing.T) {
	h := testHub(t)
	f := automod.Finding{Checker: automod.CheckerRules, Action: automod.ActionFlag}
	a := &fakeAdapter{platform: db.PlatformTwitch, account: "acct",
		messages: []Message{{ID: "m1", Author: Author{ID: "u1"}, Text: "x"}}}
	h.SetModerator(&stubModerator{verdict: automod.Verdict{
		Findings: []automod.Finding{f}, Act: []automod.Finding{f},
	}})
	attach(t, h, a)
	waitFor(t, "the flag", func() bool { return len(h.Flagged("", 10)) == 1 })
	if !h.Flagged("", 10)[0].Acted {
		t.Fatal("a finding the matrix acted on was reported as not acted on")
	}
}

// flagOnly flags exactly the messages whose text matches, so a test can mix
// clean and flagged lines in one adapter.
type flagOnly struct {
	mu      sync.Mutex
	text    string
	finding automod.Finding
}

func (f *flagOnly) CheckFast(_ db.Platform, _ string, text string) automod.Verdict {
	f.mu.Lock()
	defer f.mu.Unlock()
	if text != f.text {
		return automod.Verdict{}
	}
	return automod.Verdict{Findings: []automod.Finding{f.finding}}
}

func (f *flagOnly) CheckModel(context.Context, db.Platform, string) (automod.Verdict, error) {
	return automod.Verdict{}, nil
}
func (f *flagOnly) ModelEnabled() bool             { return false }
func (f *flagOnly) ModelStats() automod.ModelStats { return automod.ModelStats{} }
