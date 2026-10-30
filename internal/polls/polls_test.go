package polls

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestOnlyABareNumberOrBangVoteIsAVote(t *testing.T) {
	for _, tc := range []struct {
		text string
		n    int
		want int
		ok   bool
	}{
		{"1", 3, 0, true},
		{" 3 ", 3, 2, true},
		{"!vote 2", 3, 1, true},
		{"!VOTE   2", 3, 1, true},
		{"!Vote\t1", 2, 0, true},
		// A number in a sentence is chat, not a ballot.
		{"2 please", 3, 0, false},
		{"I vote 2", 3, 0, false},
		{"lol 2 funny", 3, 0, false},
		{"22", 3, 0, false},
		{"!vote2", 3, 0, false},
		{"!vote", 3, 0, false},
		{"!vote 2 3", 3, 0, false},
		// Out of range for this poll.
		{"4", 3, 0, false},
		{"0", 3, 0, false},
		{"!vote 7", 6, 0, false},
		// Lookalike digits are not ASCII digits.
		{"２", 3, 0, false},
		{"٢", 3, 0, false},
		{"", 3, 0, false},
	} {
		got, ok := ParseVote(tc.text, tc.n)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("ParseVote(%q, %d) = %d, %v; want %d, %v", tc.text, tc.n, got, ok, tc.want, tc.ok)
		}
	}
}

// TestOperatorTextCannotInjectIntoChat: the question and options are sent into
// every chat. A newline in an IRC PRIVMSG is a second IRC command, and a bidi
// override can make a question read as something other than what was typed.
// Both become spaces before anything is stored or sent.
func TestOperatorTextCannotInjectIntoChat(t *testing.T) {
	s, err := Spec{
		Question:        "Next map?\r\nPRIVMSG #other :spam",
		Options:         []string{"  Dust‮2 ", "Mir\x00age", "Nuke​"},
		DurationSeconds: 60,
	}.Normalise()
	if err != nil {
		t.Fatal(err)
	}
	all := s.Question + strings.Join(s.Options, "")
	for _, r := range all {
		if r < 0x20 || r == 0x7f || r == '‮' || r == '​' {
			t.Errorf("a control or format character survived: %q", all)
		}
	}
	if s.Question != "Next map? PRIVMSG #other :spam" || s.Options[0] != "Dust 2" || s.Options[1] != "Mir age" || s.Options[2] != "Nuke" {
		t.Errorf("cleaned to %q %q", s.Question, s.Options)
	}
	if strings.Contains(StartText(s), "\n") || strings.Contains(StartText(s), "\r") {
		t.Error("the announcement carries a line break")
	}
}

func TestASpecOutsideTheLimitsIsRefused(t *testing.T) {
	ok := Spec{Question: "q", Options: []string{"a", "b"}, DurationSeconds: 60}
	long := strings.Repeat("x", MaxQuestion+1)
	for name, s := range map[string]Spec{
		"no question":      {Options: ok.Options, DurationSeconds: 60},
		"blank question":   {Question: " \n\t ", Options: ok.Options, DurationSeconds: 60},
		"long question":    {Question: long, Options: ok.Options, DurationSeconds: 60},
		"one option":       {Question: "q", Options: []string{"a"}, DurationSeconds: 60},
		"seven options":    {Question: "q", Options: []string{"a", "b", "c", "d", "e", "f", "g"}, DurationSeconds: 60},
		"empty option":     {Question: "q", Options: []string{"a", " "}, DurationSeconds: 60},
		"long option":      {Question: "q", Options: []string{"a", strings.Repeat("y", MaxOption+1)}, DurationSeconds: 60},
		"duplicate option": {Question: "q", Options: []string{"Yes", "yes"}, DurationSeconds: 60},
		"too short":        {Question: "q", Options: ok.Options, DurationSeconds: 14},
		"too long":         {Question: "q", Options: ok.Options, DurationSeconds: 3601},
	} {
		if _, err := s.Normalise(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: Normalise = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := ok.Normalise(); err != nil {
		t.Errorf("a valid spec was refused: %v", err)
	}
}

// TestTheAnnouncementFitsTheStrictestChatAndNamesNoPlatform: one text goes to
// every chat, so it must fit YouTube's 200 characters even at the limits, and
// it must never name a platform -- a Twitch channel told to "also vote on
// YouTube" is advertising a rival.
func TestTheAnnouncementFitsTheStrictestChatAndNamesNoPlatform(t *testing.T) {
	opts := make([]string, MaxOptions)
	for i := range opts {
		opts[i] = strings.Repeat(string(rune('a'+i)), MaxOption)
	}
	s := Spec{Question: strings.Repeat("Q", MaxQuestion), Options: opts, DurationSeconds: 90}
	r := Result{Question: s.Question, Total: 10}
	for _, o := range opts {
		r.Options = append(r.Options, OptionResult{Label: o, Votes: 2})
	}
	r.Options[0].Votes = 0 // a five-way tie
	for _, text := range []string{StartText(s), EndText(r)} {
		if n := utf8.RuneCountInString(text); n > MaxAnnouncement {
			t.Errorf("%d characters: %q", n, text)
		}
		for _, p := range []string{"twitch", "youtube", "kick", "facebook", "rumble"} {
			if strings.Contains(strings.ToLower(text), p) {
				t.Errorf("the announcement names %s: %q", p, text)
			}
		}
	}
	short := StartText(Spec{Question: "Next map?", Options: []string{"Dust", "Mirage"}, DurationSeconds: 90})
	if short != "POLL: Next map? — 1) Dust 2) Mirage — type 1-2 or !vote N to vote. 1 min 30 s to vote." {
		t.Errorf("start text = %q", short)
	}
}

func TestTheClosingAnnouncementSaysWhoWon(t *testing.T) {
	r := Result{Question: "Next map?", Total: 4, Options: []OptionResult{{Label: "Dust", Votes: 3}, {Label: "Mirage", Votes: 1}}}
	if got := EndText(r); got != "POLL CLOSED: Next map? — Dust wins with 75% (3 of 4 votes)." {
		t.Errorf("winner: %q", got)
	}
	r.Options[1].Votes, r.Total = 3, 6
	if got := EndText(r); got != "POLL CLOSED: Next map? — a tie between Dust, Mirage, 3 votes each (6 in all)." {
		t.Errorf("tie: %q", got)
	}
	if got := EndText(Result{Question: "q", Options: []OptionResult{{Label: "a"}, {Label: "b"}}}); got != "POLL CLOSED: q — no votes." {
		t.Errorf("none: %q", got)
	}
}
