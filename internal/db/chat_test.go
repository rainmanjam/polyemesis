package db

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func chatMsg(platform Platform, id string, at time.Time) ChatMessage {
	return ChatMessage{
		Platform:   platform,
		Account:    "acct-1",
		MessageID:  id,
		Channel:    "chan",
		AuthorID:   "u1",
		AuthorName: "Viewer",
		Text:       "message " + id,
		At:         at,
	}
}

func TestChatMessagesRoundTripEverythingTheRendererNeeds(t *testing.T) {
	d := testDB(t)
	at := time.Date(2026, 3, 1, 12, 0, 0, 123_000_000, time.UTC)

	in := ChatMessage{
		Platform: PlatformTwitch, Account: "42", MessageID: "m1", Channel: "streamer",
		AuthorID: "7", AuthorName: "Bob", AuthorColor: "#1e90ff",
		Moderator: true, Subscriber: true, Broadcaster: true,
		Text:      "hello there",
		Badges:    json.RawMessage(`[{"id":"subscriber","version":"12"}]`),
		Emotes:    json.RawMessage(`[{"id":"25","start":0,"end":5}]`),
		ReplyToID: "m0", ReplyTo: "Alice", Echo: true, At: at,
	}
	if n, err := d.AppendChatMessages([]ChatMessage{in}); err != nil || n != 1 {
		t.Fatalf("AppendChatMessages = %d, %v", n, err)
	}

	got, err := d.RecentChatMessages(10)
	if err != nil {
		t.Fatalf("RecentChatMessages: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d messages, want 1", len(got))
	}
	m := got[0]
	if m.Text != in.Text || m.AuthorName != "Bob" || m.AuthorColor != "#1e90ff" {
		t.Errorf("message = %+v", m)
	}
	if !m.Moderator || !m.Subscriber || !m.Broadcaster || !m.Echo {
		t.Errorf("flags lost: %+v", m)
	}
	if m.ReplyToID != "m0" || m.ReplyTo != "Alice" {
		t.Errorf("reply lost: %q/%q", m.ReplyToID, m.ReplyTo)
	}
	if string(m.Badges) != string(in.Badges) || string(m.Emotes) != string(in.Emotes) {
		t.Errorf("badges/emotes lost: %s %s", m.Badges, m.Emotes)
	}
	// Millisecond resolution matters: chat arrives in bursts and a
	// second-resolution sort scrambles a fast exchange.
	if !m.At.Equal(at) {
		t.Errorf("timestamp = %v, want %v", m.At, at)
	}
}

func TestRedeliveredMessagesAreStoredOnce(t *testing.T) {
	d := testDB(t)
	at := time.Now()
	m := chatMsg(PlatformKick, "dup-1", at)

	if n, _ := d.AppendChatMessages([]ChatMessage{m}); n != 1 {
		t.Fatalf("first insert reported %d rows", n)
	}
	// A webhook retry, or a poll page overlapping the previous one.
	if n, err := d.AppendChatMessages([]ChatMessage{m, m}); err != nil || n != 0 {
		t.Fatalf("redelivery inserted %d rows (err %v); chat would show duplicates", n, err)
	}

	got, _ := d.RecentChatMessages(10)
	if len(got) != 1 {
		t.Fatalf("stored %d copies of one message", len(got))
	}
}

func TestTheSameMessageIDOnTwoPlatformsIsTwoMessages(t *testing.T) {
	d := testDB(t)
	at := time.Now()

	if _, err := d.AppendChatMessages([]ChatMessage{
		chatMsg(PlatformTwitch, "1", at),
		chatMsg(PlatformKick, "1", at),
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := d.RecentChatMessages(10)
	if len(got) != 2 {
		t.Fatalf("stored %d messages; two platforms sharing an id collided", len(got))
	}
}

func TestRecentChatMessagesReturnsTheNewestInReadingOrder(t *testing.T) {
	d := testDB(t)
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	var batch []ChatMessage
	for i := 0; i < 10; i++ {
		batch = append(batch, chatMsg(PlatformTwitch, fmt.Sprintf("m%d", i), base.Add(time.Duration(i)*time.Second)))
	}
	if _, err := d.AppendChatMessages(batch); err != nil {
		t.Fatal(err)
	}

	got, err := d.RecentChatMessages(3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d, want 3", len(got))
	}
	if got[0].MessageID != "m7" || got[2].MessageID != "m9" {
		t.Fatalf("got %s..%s, want the newest three oldest-first", got[0].MessageID, got[2].MessageID)
	}
}

func TestRecentChatMessagesForOnePlatform(t *testing.T) {
	d := testDB(t)
	at := time.Now()
	if _, err := d.AppendChatMessages([]ChatMessage{
		chatMsg(PlatformTwitch, "t1", at),
		chatMsg(PlatformKick, "k1", at),
		chatMsg(PlatformTwitch, "t2", at.Add(time.Second)),
	}); err != nil {
		t.Fatal(err)
	}

	got, err := d.RecentChatMessagesFor(PlatformTwitch, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d Twitch messages, want 2", len(got))
	}
	for _, m := range got {
		if m.Platform != PlatformTwitch {
			t.Fatalf("platform filter leaked %q", m.Platform)
		}
	}
}

func TestPurgeKeepsTheNewestHoweverOldTheyAre(t *testing.T) {
	d := testDB(t)
	old := time.Now().Add(-48 * time.Hour)

	var batch []ChatMessage
	for i := 0; i < 10; i++ {
		batch = append(batch, chatMsg(PlatformTwitch, fmt.Sprintf("m%d", i), old.Add(time.Duration(i)*time.Second)))
	}
	if _, err := d.AppendChatMessages(batch); err != nil {
		t.Fatal(err)
	}

	// Everything is older than the cutoff, but a quiet channel must still have
	// something to show rather than an empty pane that reads as broken.
	deleted, err := d.PurgeChatMessages(time.Now().Add(-time.Hour), 3)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 7 {
		t.Fatalf("deleted %d, want 7", deleted)
	}
	got, _ := d.RecentChatMessages(100)
	if len(got) != 3 || got[2].MessageID != "m9" {
		t.Fatalf("kept %d messages ending at %s, want the newest three", len(got), got[len(got)-1].MessageID)
	}
}

func TestPurgeLeavesRecentMessagesAlone(t *testing.T) {
	d := testDB(t)
	now := time.Now()
	if _, err := d.AppendChatMessages([]ChatMessage{
		chatMsg(PlatformTwitch, "fresh", now),
		chatMsg(PlatformTwitch, "stale", now.Add(-24*time.Hour)),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := d.PurgeChatMessages(now.Add(-time.Hour), 0); err != nil {
		t.Fatal(err)
	}
	got, _ := d.RecentChatMessages(10)
	if len(got) != 1 || got[0].MessageID != "fresh" {
		t.Fatalf("kept %+v, want only the fresh message", got)
	}
}

func TestDeleteChatMessageIsIdempotent(t *testing.T) {
	d := testDB(t)
	if _, err := d.AppendChatMessages([]ChatMessage{chatMsg(PlatformKick, "gone", time.Now())}); err != nil {
		t.Fatal(err)
	}

	if err := d.DeleteChatMessage(PlatformKick, "acct-1", "gone"); err != nil {
		t.Fatal(err)
	}
	// A moderator deleting something we already purged has already got what
	// they wanted; that must not be an error.
	if err := d.DeleteChatMessage(PlatformKick, "acct-1", "gone"); err != nil {
		t.Fatalf("deleting an absent message failed: %v", err)
	}
	if n, _ := d.ChatMessageCount(); n != 0 {
		t.Fatalf("count = %d, want 0", n)
	}
}

func TestChatEdgeCasesDoNotError(t *testing.T) {
	d := testDB(t)

	if n, err := d.AppendChatMessages(nil); err != nil || n != 0 {
		t.Fatalf("appending nothing = %d, %v", n, err)
	}
	if got, err := d.RecentChatMessages(0); err != nil || len(got) != 0 {
		t.Fatalf("a zero limit = %v, %v", got, err)
	}

	// Malformed JSON in badges must not poison the row: the text is what the
	// reader came for.
	m := chatMsg(PlatformTwitch, "odd", time.Now())
	m.Badges = json.RawMessage("not json")
	m.Emotes = nil
	if _, err := d.AppendChatMessages([]ChatMessage{m}); err != nil {
		t.Fatal(err)
	}
	got, _ := d.RecentChatMessages(10)
	if len(got) != 1 || string(got[0].Badges) != "[]" {
		t.Fatalf("badges = %s, want an empty array", got[0].Badges)
	}

	// A message with no timestamp is stamped rather than sorted to 1970.
	m2 := chatMsg(PlatformTwitch, "notime", time.Time{})
	m2.At = time.Time{}
	if _, err := d.AppendChatMessages([]ChatMessage{m2}); err != nil {
		t.Fatal(err)
	}
	got, _ = d.RecentChatMessagesFor(PlatformTwitch, 10)
	for _, g := range got {
		if g.MessageID == "notime" && g.At.Year() < 2020 {
			t.Fatalf("a missing timestamp stored as %v", g.At)
		}
	}
}

// The moderator's user card reads from OUR store, because no platform publishes
// a user-chat-history API. That makes two properties load-bearing.
func TestChatMessagesByAuthor(t *testing.T) {
	d := testDB(t)

	msgs := []ChatMessage{
		{Platform: PlatformTwitch, MessageID: "t1", AuthorID: "7", AuthorName: "Bob", Text: "first", At: time.UnixMilli(1000)},
		{Platform: PlatformTwitch, MessageID: "t2", AuthorID: "9", AuthorName: "Eve", Text: "other person", At: time.UnixMilli(2000)},
		{Platform: PlatformTwitch, MessageID: "t3", AuthorID: "7", AuthorName: "Bob", Text: "second", At: time.UnixMilli(3000)},
		// Same author id on a DIFFERENT platform. Ids are platform-scoped and
		// nothing makes them unique across platforms, so a card for Twitch's
		// user 7 must not show Kick's user 7 — they are different people.
		{Platform: PlatformKick, MessageID: "k1", AuthorID: "7", AuthorName: "Someone else", Text: "kick", At: time.UnixMilli(4000)},
	}
	if _, err := d.AppendChatMessages(msgs); err != nil {
		t.Fatal(err)
	}

	got, err := d.ChatMessagesByAuthor(PlatformTwitch, "7", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d messages, want the 2 from Twitch user 7: %+v", len(got), got)
	}
	// Oldest first, like every other chat read: a card is read top to bottom.
	if got[0].Text != "first" || got[1].Text != "second" {
		t.Fatalf("order = %q, %q; want oldest first", got[0].Text, got[1].Text)
	}
	for _, m := range got {
		if m.Platform != PlatformTwitch {
			t.Fatalf("a Kick message leaked into a Twitch user's card: %+v", m)
		}
	}
}

// The limit is what makes Truncated meaningful upstream, so it has to actually
// bound the read.
func TestChatMessagesByAuthorRespectsTheLimit(t *testing.T) {
	d := testDB(t)
	var msgs []ChatMessage
	for i := 0; i < 10; i++ {
		msgs = append(msgs, ChatMessage{
			Platform: PlatformTwitch, MessageID: fmt.Sprintf("m%d", i),
			AuthorID: "7", AuthorName: "Bob", Text: "line", At: time.UnixMilli(int64(1000 + i)),
		})
	}
	if _, err := d.AppendChatMessages(msgs); err != nil {
		t.Fatal(err)
	}
	got, err := d.ChatMessagesByAuthor(PlatformTwitch, "7", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d, want 3", len(got))
	}
	// The NEWEST three, not the oldest: a moderator wants what was just said.
	if got[2].Text != "line" || got[2].At.UnixMilli() != 1009 {
		t.Fatalf("last message = %+v, want the newest", got[2])
	}
}

func TestChatMessagesByAuthorRefusesAnEmptyAuthor(t *testing.T) {
	d := testDB(t)
	if _, err := d.AppendChatMessages([]ChatMessage{
		{Platform: PlatformTwitch, MessageID: "t1", AuthorID: "", Text: "anonymous", At: time.UnixMilli(1)},
	}); err != nil {
		t.Fatal(err)
	}
	// Both spellings, and the EMPTY one is the load-bearing case.
	//
	// author_id defaults to '' for any platform that sends none, so without the
	// guard `ChatMessagesByAuthor(p, "")` matches every anonymous message and
	// pools unrelated strangers into one person's card. A whitespace query alone
	// does not prove that: `author_id = '  '` matches nothing whether or not the
	// guard exists, so a test using only that passes against a broken build.
	// This was found by removing the guard and watching the test still pass.
	for _, id := range []string{"", "  "} {
		got, err := d.ChatMessagesByAuthor(PlatformTwitch, id, 50)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("author id %q returned %d messages; anonymous authors would be "+
				"pooled into one card", id, len(got))
		}
	}
}
