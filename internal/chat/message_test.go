package chat

import (
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

func TestNormaliseRepairsRatherThanRejects(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		in    Message
		check func(t *testing.T, got Message)
	}{
		{
			name: "a message with no timestamp is stamped with the clock",
			in:   Message{Platform: db.PlatformTwitch, ID: "1", Text: "hi"},
			check: func(t *testing.T, got Message) {
				if !got.At.Equal(now) {
					t.Fatalf("At = %v, want %v", got.At, now)
				}
			},
		},
		{
			name: "a platform timestamp is kept and normalised to UTC",
			in: Message{Platform: db.PlatformTwitch, ID: "1", Text: "hi",
				At: now.In(time.FixedZone("PT", -8*3600))},
			check: func(t *testing.T, got Message) {
				if !got.At.Equal(now) || got.At.Location() != time.UTC {
					t.Fatalf("At = %v (%v), want %v UTC", got.At, got.At.Location(), now)
				}
			},
		},
		{
			name: "an emote range past the end of the text is dropped and the message survives",
			in: Message{Platform: db.PlatformTwitch, ID: "1", Text: "hello",
				Emotes: []Emote{{ID: "ok", Start: 0, End: 5}, {ID: "past", Start: 4, End: 99}}},
			check: func(t *testing.T, got Message) {
				if got.Text != "hello" {
					t.Fatalf("text lost: %q", got.Text)
				}
				if len(got.Emotes) != 1 || got.Emotes[0].ID != "ok" {
					t.Fatalf("emotes = %+v, want only the in-range one", got.Emotes)
				}
			},
		},
		{
			name: "an inverted or negative emote range is dropped",
			in: Message{Platform: db.PlatformTwitch, ID: "1", Text: "hello",
				Emotes: []Emote{{ID: "back", Start: 3, End: 2}, {ID: "neg", Start: -1, End: 2}}},
			check: func(t *testing.T, got Message) {
				if len(got.Emotes) != 0 {
					t.Fatalf("emotes = %+v, want none", got.Emotes)
				}
			},
		},
		{
			name: "emotes are sorted by position so a renderer can walk them once",
			in: Message{Platform: db.PlatformTwitch, ID: "1", Text: "aaaaaaaaaa",
				Emotes: []Emote{{ID: "b", Start: 5, End: 7}, {ID: "a", Start: 0, End: 2}}},
			check: func(t *testing.T, got Message) {
				if len(got.Emotes) != 2 || got.Emotes[0].ID != "a" {
					t.Fatalf("emotes = %+v, want a before b", got.Emotes)
				}
			},
		},
		{
			name: "emote offsets are counted in runes not bytes",
			in: Message{Platform: db.PlatformTwitch, ID: "1", Text: "é😀ok",
				Emotes: []Emote{{ID: "fits", Start: 0, End: 4}}},
			check: func(t *testing.T, got Message) {
				if len(got.Emotes) != 1 {
					t.Fatalf("emote dropped; the text is 4 runes and the range is 4 runes: %+v", got.Emotes)
				}
			},
		},
		{
			name: "a colour without its hash is accepted and lowercased",
			in:   Message{Platform: db.PlatformTwitch, ID: "1", Text: "hi", Author: Author{Color: "1E90FF"}},
			check: func(t *testing.T, got Message) {
				if got.Author.Color != "#1e90ff" {
					t.Fatalf("color = %q, want #1e90ff", got.Author.Color)
				}
			},
		},
		{
			name: "an unparseable colour is dropped rather than failing the message",
			in:   Message{Platform: db.PlatformTwitch, ID: "1", Text: "hi", Author: Author{Color: "rebeccapurple"}},
			check: func(t *testing.T, got Message) {
				if got.Author.Color != "" {
					t.Fatalf("color = %q, want empty", got.Author.Color)
				}
				if got.Text != "hi" {
					t.Fatalf("text lost: %q", got.Text)
				}
			},
		},
		{
			name: "control characters are dropped and line breaks become spaces",
			in:   Message{Platform: db.PlatformTwitch, ID: "1", Text: "one\x07 two\nthree\rfour"},
			check: func(t *testing.T, got Message) {
				if got.Text != "one two three four" {
					t.Fatalf("text = %q", got.Text)
				}
			},
		},
		{
			name: "text is clamped to the rune limit",
			in:   Message{Platform: db.PlatformTwitch, ID: "1", Text: strings.Repeat("é", MaxTextRunes+500)},
			check: func(t *testing.T, got Message) {
				if n := len([]rune(got.Text)); n != MaxTextRunes {
					t.Fatalf("clamped to %d runes, want %d", n, MaxTextRunes)
				}
			},
		},
		{
			name: "a moderator badge promotes the moderator flag",
			in: Message{Platform: db.PlatformTwitch, ID: "1", Text: "hi",
				Author: Author{Badges: []Badge{{ID: "moderator", Version: "1"}}}},
			check: func(t *testing.T, got Message) {
				if !got.Author.Moderator {
					t.Fatal("moderator badge did not set the moderator flag")
				}
			},
		},
		{
			name: "the broadcaster is always a moderator of their own channel",
			in: Message{Platform: db.PlatformKick, ID: "1", Text: "hi",
				Author: Author{Badges: []Badge{{ID: "broadcaster"}}}},
			check: func(t *testing.T, got Message) {
				if !got.Author.Broadcaster || !got.Author.Moderator {
					t.Fatalf("broadcaster=%v moderator=%v, want both", got.Author.Broadcaster, got.Author.Moderator)
				}
			},
		},
		{
			name: "a nameless author falls back to their id rather than rendering blank",
			in:   Message{Platform: db.PlatformYouTube, ID: "1", Text: "hi", Author: Author{ID: "UC123"}},
			check: func(t *testing.T, got Message) {
				if got.Author.Name != "UC123" {
					t.Fatalf("name = %q, want the id", got.Author.Name)
				}
			},
		},
		{
			name: "an author with neither name nor id is still renderable",
			in:   Message{Platform: db.PlatformYouTube, ID: "1", Text: "hi"},
			check: func(t *testing.T, got Message) {
				if got.Author.Name != "unknown" {
					t.Fatalf("name = %q, want unknown", got.Author.Name)
				}
			},
		},
		{
			name: "a badge with only a label gets an id derived from it",
			in: Message{Platform: db.PlatformKick, ID: "1", Text: "hi",
				Author: Author{Badges: []Badge{{Label: "Moderator"}}}},
			check: func(t *testing.T, got Message) {
				if len(got.Author.Badges) != 1 || got.Author.Badges[0].ID != "moderator" {
					t.Fatalf("badges = %+v", got.Author.Badges)
				}
				if !got.Author.Moderator {
					t.Fatal("label-only moderator badge did not set the flag")
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.check(t, tc.in.Normalise(fixedClock(now)))
		})
	}
}

func TestSynthesisedIDIsStableForTheSameMessage(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	base := Message{Platform: db.PlatformKick, Account: "42", Text: "hello", At: now,
		Author: Author{ID: "7", Name: "someone"}}

	first := base.Normalise(fixedClock(now))
	second := base.Normalise(fixedClock(now.Add(time.Hour)))

	if first.ID == "" || !strings.HasPrefix(first.ID, "syn-") {
		t.Fatalf("id = %q, want a syn- prefixed synthesised id", first.ID)
	}
	if first.ID != second.ID {
		t.Fatalf("redelivery produced a different id (%q vs %q), so dedupe would fail", first.ID, second.ID)
	}

	other := base
	other.Text = "hello!"
	if other.Normalise(fixedClock(now)).ID == first.ID {
		t.Fatal("two different messages hashed to the same id")
	}
}

func TestKeyIsScopedToPlatformAndAccount(t *testing.T) {
	a := Message{ID: "1", Platform: db.PlatformTwitch, Account: "alice"}
	b := Message{ID: "1", Platform: db.PlatformKick, Account: "alice"}
	c := Message{ID: "1", Platform: db.PlatformTwitch, Account: "bob"}

	if a.Key() == b.Key() {
		t.Fatal("two platforms sharing a message id collided")
	}
	if a.Key() == c.Key() {
		t.Fatal("two accounts on one platform sharing a message id collided")
	}
}

func TestDBRoundTripPreservesTheRenderedMessage(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	in := Message{
		ID: "abc", Platform: db.PlatformTwitch, Account: "42", Channel: "chan",
		Author: Author{ID: "7", Name: "Someone", Color: "#1e90ff",
			Badges: []Badge{{ID: "subscriber", Version: "12"}}, Subscriber: true},
		Text:      "hello there",
		Emotes:    []Emote{{ID: "25", Start: 0, End: 5}},
		At:        now,
		ReplyToID: "prev", ReplyTo: "Other",
	}.Normalise(fixedClock(now))

	got := FromDB(ToDB(in))

	if got.ID != in.ID || got.Text != in.Text || got.Author.Color != in.Author.Color {
		t.Fatalf("round trip lost scalars: %+v", got)
	}
	if len(got.Author.Badges) != 1 || got.Author.Badges[0].Version != "12" {
		t.Fatalf("round trip lost badges: %+v", got.Author.Badges)
	}
	if len(got.Emotes) != 1 || got.Emotes[0].End != 5 {
		t.Fatalf("round trip lost emotes: %+v", got.Emotes)
	}
	if !got.At.Equal(in.At) {
		t.Fatalf("round trip lost the timestamp: %v vs %v", got.At, in.At)
	}
}

func TestZeroMessageMeansThePlatformWillEchoItBack(t *testing.T) {
	if !(Message{}).Zero() {
		t.Fatal("the zero Message did not report itself as zero")
	}
	if (Message{ID: "1"}).Zero() {
		t.Fatal("a message with an id reported itself as zero")
	}
}
