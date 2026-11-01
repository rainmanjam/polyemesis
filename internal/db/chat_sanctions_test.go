package db

import (
	"testing"
	"time"
)

// UX Council A48: the user card has to say whether a viewer is under a ban or
// a timeout, so Lift ban is not a button that does nothing. These pin the
// table that answers it.

func TestChatSanctionTableIsCreatedIdempotentlyAndLeavesTheVersionAlone(t *testing.T) {
	d := testDB(t)
	before, err := d.UserVersion()
	if err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	for pass := 1; pass <= 2; pass++ {
		if err := d.MigrateChatSanctions(); err != nil {
			t.Fatalf("MigrateChatSanctions pass %d: %v", pass, err)
		}
	}
	var n int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'chat_sanctions'`).Scan(&n); err != nil {
		t.Fatalf("look up chat_sanctions: %v", err)
	}
	if n != 1 {
		t.Fatalf("chat_sanctions is missing after the slot ran")
	}
	after, err := d.UserVersion()
	if err != nil {
		t.Fatalf("read schema version after slot: %v", err)
	}
	if after != before {
		t.Fatalf("MigrateChatSanctions moved the schema version from %d to %d", before, after)
	}
}

func TestAPermanentBanIsActiveUntilItIsLifted(t *testing.T) {
	d := testDB(t)
	now := time.UnixMilli(1_800_000_000_000)
	if err := d.RecordChatSanction(ChatSanction{
		Platform: PlatformTwitch, Account: "acct", UserID: "u1",
		Action: "ban", At: now, By: "operator", Reason: "spam",
	}); err != nil {
		t.Fatalf("RecordChatSanction: %v", err)
	}

	got, ok, err := d.ActiveChatSanction(PlatformTwitch, "acct", "u1", now.Add(365*24*time.Hour))
	if err != nil || !ok {
		t.Fatalf("a permanent ban a year later reads as ok=%v err=%v, want active", ok, err)
	}
	if got.Action != "ban" || !got.Until.IsZero() || got.By != "operator" || got.Reason != "spam" {
		t.Fatalf("ban read back as %+v", got)
	}

	if err := d.ClearChatSanction(PlatformTwitch, "acct", "u1"); err != nil {
		t.Fatalf("ClearChatSanction: %v", err)
	}
	if _, ok, err := d.ActiveChatSanction(PlatformTwitch, "acct", "u1", now); err != nil || ok {
		t.Fatalf("after lifting, ok=%v err=%v, want no sanction", ok, err)
	}
}

// THE expiry rule. A timeout whose end has passed is not a sanction: reading it
// as one would leave Lift ban enabled for someone who can already talk, and a
// timeout read as permanent would be worse.
func TestATimeoutIsActiveOnlyUntilItExpires(t *testing.T) {
	d := testDB(t)
	start := time.UnixMilli(1_800_000_000_000)
	until := start.Add(10 * time.Minute)
	if err := d.RecordChatSanction(ChatSanction{
		Platform: PlatformKick, Account: "acct", UserID: "u2",
		Action: "timeout", Until: until, At: start, By: "automod",
	}); err != nil {
		t.Fatalf("RecordChatSanction: %v", err)
	}

	got, ok, err := d.ActiveChatSanction(PlatformKick, "acct", "u2", until.Add(-time.Second))
	if err != nil || !ok {
		t.Fatalf("one second before expiry: ok=%v err=%v, want active", ok, err)
	}
	if got.Action != "timeout" || !got.Until.Equal(until) {
		t.Fatalf("timeout read back as %+v, want until %v", got, until)
	}
	if _, ok, err := d.ActiveChatSanction(PlatformKick, "acct", "u2", until); err != nil || ok {
		t.Fatalf("at the expiry instant: ok=%v err=%v, want none -- the timeout is over", ok, err)
	}
	if _, ok, err := d.ActiveChatSanction(PlatformKick, "acct", "u2", until.Add(time.Hour)); err != nil || ok {
		t.Fatalf("an hour after expiry: ok=%v err=%v, want none", ok, err)
	}
}

// A second sanction replaces the first: a timeout escalated to a ban is a ban.
func TestTheLatestSanctionReplacesTheEarlierOne(t *testing.T) {
	d := testDB(t)
	now := time.UnixMilli(1_800_000_000_000)
	for _, s := range []ChatSanction{
		{Platform: PlatformTwitch, Account: "acct", UserID: "u3", Action: "timeout", Until: now.Add(time.Minute), At: now},
		{Platform: PlatformTwitch, Account: "acct", UserID: "u3", Action: "ban", At: now.Add(time.Second)},
	} {
		if err := d.RecordChatSanction(s); err != nil {
			t.Fatalf("RecordChatSanction: %v", err)
		}
	}
	got, ok, err := d.ActiveChatSanction(PlatformTwitch, "acct", "u3", now.Add(time.Hour))
	if err != nil || !ok || got.Action != "ban" {
		t.Fatalf("escalated sanction read as %+v ok=%v err=%v, want the permanent ban", got, ok, err)
	}
}

// Sanctions are per channel. An empty account is "any channel on this
// platform", which is what the card asks when it was opened without one.
func TestASanctionIsScopedToItsAccount(t *testing.T) {
	d := testDB(t)
	now := time.UnixMilli(1_800_000_000_000)
	if err := d.RecordChatSanction(ChatSanction{
		Platform: PlatformTwitch, Account: "chan-a", UserID: "u4", Action: "ban", At: now,
	}); err != nil {
		t.Fatalf("RecordChatSanction: %v", err)
	}
	if _, ok, _ := d.ActiveChatSanction(PlatformTwitch, "chan-b", "u4", now); ok {
		t.Fatal("a ban on chan-a read as active on chan-b")
	}
	if _, ok, _ := d.ActiveChatSanction(PlatformYouTube, "chan-a", "u4", now); ok {
		t.Fatal("a Twitch ban read as active on YouTube")
	}
	if _, ok, _ := d.ActiveChatSanction(PlatformTwitch, "", "u4", now); !ok {
		t.Fatal("an any-account read missed the chan-a ban")
	}
	// Lifting with no account lifts it wherever it is: that is the shape of an
	// unban sent without one.
	if err := d.ClearChatSanction(PlatformTwitch, "", "u4"); err != nil {
		t.Fatalf("ClearChatSanction: %v", err)
	}
	if _, ok, _ := d.ActiveChatSanction(PlatformTwitch, "chan-a", "u4", now); ok {
		t.Fatal("an any-account lift left the chan-a ban in place")
	}
}
