package db

import (
	"testing"
	"time"
)

// searchSeed writes messages whose text and author are both worth searching, at
// distinct times so ordering assertions mean something.
func searchSeed(t *testing.T) *DB {
	t.Helper()
	d := testDB(t)
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	msgs := []ChatMessage{
		{Platform: PlatformTwitch, Account: "a", MessageID: "m1", AuthorID: "u1",
			AuthorName: "Alice", Text: "the stream looks great", At: base},
		{Platform: PlatformTwitch, Account: "a", MessageID: "m2", AuthorID: "u2",
			AuthorName: "Bob", Text: "GREAT audio today", At: base.Add(time.Minute)},
		{Platform: PlatformYouTube, Account: "b", MessageID: "m3", AuthorID: "u3",
			AuthorName: "Carol", Text: "hello from youtube", At: base.Add(2 * time.Minute)},
		// The wildcard traps. Each literal is paired with a decoy that an
		// unescaped pattern would also match, so the assertions below fail if
		// escaping is dropped. Without the decoys the literal is the only row
		// containing "100" or matching "a?b" and the test passes either way.
		{Platform: PlatformTwitch, Account: "a", MessageID: "m4", AuthorID: "u4",
			AuthorName: "Dave", Text: "buffer is 100% full", At: base.Add(3 * time.Minute)},
		{Platform: PlatformTwitch, Account: "a", MessageID: "m5", AuthorID: "u5",
			AuthorName: "Erin", Text: "queue sat at 100 for a while", At: base.Add(4 * time.Minute)},
		{Platform: PlatformTwitch, Account: "a", MessageID: "m6", AuthorID: "u6",
			AuthorName: "Frank", Text: "marker a_b here", At: base.Add(5 * time.Minute)},
		{Platform: PlatformTwitch, Account: "a", MessageID: "m7", AuthorID: "u7",
			AuthorName: "Grace", Text: "marker axb here", At: base.Add(6 * time.Minute)},
	}
	if _, err := d.AppendChatMessages(msgs); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return d
}

func texts(msgs []ChatMessage) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Text
	}
	return out
}

func TestSearchChatMessagesMatchesTextAndAuthorCaseInsensitively(t *testing.T) {
	d := searchSeed(t)

	// SQLite's LIKE is ASCII case-insensitive, which is what an operator typing
	// into a search box expects; assert it rather than assume it.
	got, err := d.SearchChatMessages("", "great", 50)
	if err != nil {
		t.Fatalf("SearchChatMessages: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("text search got %v, want both 'great' messages", texts(got))
	}

	// Searching a name finds that person's messages, which is how a moderator
	// looks for "what did Carol say".
	got, err = d.SearchChatMessages("", "carol", 50)
	if err != nil {
		t.Fatalf("SearchChatMessages: %v", err)
	}
	if len(got) != 1 || got[0].AuthorName != "Carol" {
		t.Fatalf("author search got %v", texts(got))
	}
}

// The regression this file exists for. A `%` in the term is a literal the
// operator typed, not a wildcard, and treating it as one turns a narrow search
// into "return the table" — which looks like the feature working until someone
// notices the results are unrelated.
func TestSearchChatMessagesTreatsWildcardsAsLiterals(t *testing.T) {
	d := searchSeed(t)

	got, err := d.SearchChatMessages("", "100%", 50)
	if err != nil {
		t.Fatalf("SearchChatMessages: %v", err)
	}
	if len(got) != 1 || got[0].Text != "buffer is 100% full" {
		t.Fatalf("percent term got %v, want only the literal match", texts(got))
	}

	// `_` matches any single character unescaped, so this would also drag in
	// the "marker axb here" decoy.
	got, err = d.SearchChatMessages("", "a_b", 50)
	if err != nil {
		t.Fatalf("SearchChatMessages: %v", err)
	}
	if len(got) != 1 || got[0].Text != "marker a_b here" {
		t.Fatalf("underscore term got %v, want only the literal a_b message", texts(got))
	}

	// A bare wildcard must find nothing rather than everything.
	got, err = d.SearchChatMessages("", "%", 50)
	if err != nil {
		t.Fatalf("SearchChatMessages: %v", err)
	}
	if len(got) != 1 || got[0].Text != "buffer is 100% full" {
		t.Fatalf("bare %% got %v, want only the message containing a literal %%", texts(got))
	}
}

func TestSearchChatMessagesNarrowsToPlatform(t *testing.T) {
	d := searchSeed(t)

	got, err := d.SearchChatMessages(PlatformYouTube, "hello", 50)
	if err != nil {
		t.Fatalf("SearchChatMessages: %v", err)
	}
	if len(got) != 1 || got[0].Platform != PlatformYouTube {
		t.Fatalf("youtube search got %v", texts(got))
	}

	// Same term, wrong platform: the tab filter has to actually exclude.
	got, err = d.SearchChatMessages(PlatformTwitch, "hello", 50)
	if err != nil {
		t.Fatalf("SearchChatMessages: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("twitch search got %v, want none", texts(got))
	}
}

// Unlike every other read in this package, search is newest-first: the most
// recent match is the one the operator is almost always looking for.
func TestSearchChatMessagesReturnsNewestFirstAndHonoursLimit(t *testing.T) {
	d := searchSeed(t)

	got, err := d.SearchChatMessages("", "a", 50)
	if err != nil {
		t.Fatalf("SearchChatMessages: %v", err)
	}
	if len(got) < 2 {
		t.Fatalf("expected several matches, got %v", texts(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].At.After(got[i-1].At) {
			t.Fatalf("not newest-first: %v", texts(got))
		}
	}

	got, err = d.SearchChatMessages("", "a", 2)
	if err != nil {
		t.Fatalf("SearchChatMessages: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("limit ignored: got %d, want 2", len(got))
	}
}

func TestSearchChatMessagesEmptyTermFindsNothing(t *testing.T) {
	d := searchSeed(t)

	// An empty box must not mean "everything": the pane already shows recent
	// messages, and a search that silently returns the table hides that the
	// operator has not typed anything yet.
	for _, term := range []string{"", "   "} {
		got, err := d.SearchChatMessages("", term, 50)
		if err != nil {
			t.Fatalf("SearchChatMessages(%q): %v", term, err)
		}
		if len(got) != 0 {
			t.Fatalf("term %q returned %d messages, want none", term, len(got))
		}
	}

	// A limit of zero is a caller bug, not a request for everything.
	got, err := d.SearchChatMessages("", "great", 0)
	if err != nil {
		t.Fatalf("SearchChatMessages: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("zero limit returned %d messages", len(got))
	}
}
