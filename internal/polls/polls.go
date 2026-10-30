// Package polls is the cross-platform chat poll (#14): a question with two to
// six options, open for a set time, voted on by typing `1` or `!vote 1` in any
// connected chat, and counted as one result.
//
// WHERE THE VOTES COME FROM. The counter is a chat.Tap: it sees each message
// with automod's verdict on it at the same moment the overlay gate does, later
// verdicts for messages the model checker was still judging, and every
// moderation action with its cause. A vote therefore counts only once automod
// has let the message stand -- the same bar a line has to clear to reach an
// overlay -- and stops counting the moment a moderator removes the message or
// the person.
//
// THE RULES, which docs/POLLS.md states for operators:
//
//   - One vote per viewer per platform, keyed by the platform's own user id.
//     The last vote a viewer casts is the one that counts.
//   - A message automod blocks is not a vote. A message automod is still
//     judging becomes a vote when it is cleared, and is dropped if no verdict
//     arrives within PendingGrace.
//   - A deleted or hidden vote message voids that vote. The viewer may vote
//     again; an earlier vote does not come back.
//   - A ban or timeout voids the viewer's vote and ignores them for the rest
//     of the poll, whether or not anything they said is still in chat history.
//   - A cleared room is housekeeping, not a verdict: votes stand.
//
// WHO VOTED NEVER LEAVES THIS PACKAGE. The counter holds viewer ids in memory
// for the life of one poll, to enforce the rules above. No Result, event,
// stored row or export has a field to carry one.
package polls

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Limits on what a poll may be.
const (
	MinOptions = 2
	MaxOptions = 6
	// MaxQuestion and MaxOption are in characters. Short enough that the
	// announcement fits the strictest chat (YouTube's 200) and the widget
	// stays readable on stream.
	MaxQuestion = 100
	MaxOption   = 30
	MinDuration = 15 * time.Second
	MaxDuration = time.Hour
)

// PendingGrace bounds how long a vote waits for the model checker's verdict,
// and how long a poll stays "closing" for the votes still being judged when
// its time runs out. The same four seconds as the overlay's hold
// (overlay.MaxHold), for the same reason: long enough for a hosted model's
// usual answer, and a vote never judged is not evidence it was fine.
const PendingGrace = 4 * time.Second

// Spec is what an operator asks for.
type Spec struct {
	Question string   `json:"question"`
	Options  []string `json:"options"`
	// DurationSeconds is how long the poll is open.
	DurationSeconds int `json:"durationSeconds"`
	// MirrorTwitch also runs the poll as a native Twitch poll on every
	// connected Twitch channel that allows it, and adds its votes in.
	MirrorTwitch bool `json:"mirrorTwitch,omitempty"`
	// MirrorYouTube does the same as a YouTube live poll in every connected
	// YouTube chat, when the poll fits YouTube's limits and the quota above
	// the send reserve covers it (40 units).
	MirrorYouTube bool `json:"mirrorYouTube,omitempty"`
}

// Duration is the spec's duration.
func (s Spec) Duration() time.Duration { return time.Duration(s.DurationSeconds) * time.Second }

// ErrInvalid wraps every validation failure, so the API can answer 400.
var ErrInvalid = errors.New("invalid poll")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Clean makes operator text safe to send into every chat and draw on stream.
//
// Every control and format character becomes a space -- a newline in an IRC
// PRIVMSG is a second command, and a bidi override in a question can make it
// read differently from what was typed -- runs of space collapse to one, and
// the ends are trimmed. Invalid UTF-8 is replaced rather than passed on.
func Clean(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	var b strings.Builder
	space := false
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.IsSpace(r) {
			if !space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = true
			continue
		}
		b.WriteRune(r)
		space = false
	}
	return strings.TrimRight(b.String(), " ")
}

// Normalise cleans and validates a spec.
func (s Spec) Normalise() (Spec, error) {
	out := Spec{Question: Clean(s.Question), DurationSeconds: s.DurationSeconds,
		MirrorTwitch: s.MirrorTwitch, MirrorYouTube: s.MirrorYouTube}
	if out.Question == "" {
		return Spec{}, invalid("a question is required")
	}
	if n := utf8.RuneCountInString(out.Question); n > MaxQuestion {
		return Spec{}, invalid("the question is %d characters; the limit is %d", n, MaxQuestion)
	}
	if len(s.Options) < MinOptions || len(s.Options) > MaxOptions {
		return Spec{}, invalid("a poll has %d to %d options, not %d", MinOptions, MaxOptions, len(s.Options))
	}
	seen := map[string]bool{}
	for i, o := range s.Options {
		o = Clean(o)
		if o == "" {
			return Spec{}, invalid("option %d is empty", i+1)
		}
		if n := utf8.RuneCountInString(o); n > MaxOption {
			return Spec{}, invalid("option %d is %d characters; the limit is %d", i+1, n, MaxOption)
		}
		k := strings.ToLower(o)
		if seen[k] {
			return Spec{}, invalid("option %d repeats an earlier option", i+1)
		}
		seen[k] = true
		out.Options = append(out.Options, o)
	}
	d := out.Duration()
	if d < MinDuration || d > MaxDuration {
		return Spec{}, invalid("the duration must be %d to %d seconds", int(MinDuration/time.Second), int(MaxDuration/time.Second))
	}
	return out, nil
}

// ParseVote reads a chat message as a vote among n options and returns the
// option's index (0-based).
//
// Exactly two forms, and the WHOLE message: a bare number ("2") or "!vote 2",
// any case, any spacing. "2 please", "I vote 2" and "22" are chat, not votes --
// counting a number that happens to appear in a sentence would turn every
// "lol 2 funny" into a ballot. ASCII digits only, so a fullwidth or Arabic-Indic
// digit is not a lookalike route to a vote.
func ParseVote(text string, n int) (int, bool) {
	t := strings.TrimSpace(text)
	if len(t) > 5 && strings.EqualFold(t[:5], "!vote") {
		rest := t[5:]
		trimmed := strings.TrimLeft(rest, " \t")
		if trimmed == rest {
			// "!vote2" is not the documented form.
			return 0, false
		}
		t = trimmed
	}
	if len(t) != 1 || t[0] < '1' || t[0] > '9' {
		return 0, false
	}
	k := int(t[0] - '1')
	if k >= n {
		return 0, false
	}
	return k, true
}
