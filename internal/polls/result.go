package polls

import (
	"strconv"
	"time"

	"github.com/rainmanjam/polyemesis/internal/eventstream"
)

// State is where a poll is in its life.
type State string

const (
	// StateOpen: votes are being counted.
	StateOpen State = "open"
	// StateClosing: the time is up and no new vote is accepted, but votes
	// automod was still judging, and a native mirror's final count, are being
	// waited for (at most PendingGrace, and mirrorGrace for the mirror).
	StateClosing State = "closing"
	// StateEnded: final.
	StateEnded State = "ended"
	// StateInterrupted: the server stopped while the poll was open, and the
	// votes it was holding were lost with it. Never a result.
	StateInterrupted State = "interrupted"
)

// OptionResult is one option's count.
type OptionResult struct {
	Label string `json:"label"`
	Votes int    `json:"votes"`
	// ByPlatform is how many of Votes came from each platform. Counts only.
	ByPlatform map[string]int `json:"byPlatform"`
}

// Announcement is how sending the poll into one chat went. No account, no
// channel: which platform, and whether it worked.
type Announcement struct {
	Platform string `json:"platform"`
	OK       bool   `json:"ok"`
	Skipped  bool   `json:"skipped,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// Native mirror statuses.
const (
	NativeMirrored = "mirrored"
	NativeSkipped  = "skipped"
	NativeFailed   = "failed"
	// NativeStopped: the platform's poll ended before the chat poll did. Its
	// votes so far stand, and that chat's votes count again from then on.
	NativeStopped = "stopped"
)

// NativeResult is one native poll the poll was mirrored to, or why it was
// not. Votes are the platform's own anonymous totals, one per option, and are
// already included in the option counts and the platform's breakdown.
type NativeResult struct {
	Platform string `json:"platform"`
	Status   string `json:"status"`
	Reason   string `json:"reason,omitempty"`
	Votes    []int  `json:"votes,omitempty"`
}

// Result is a poll as every surface sees it: the API, the dashboard, the
// poll.* events, the history and its export. AN ALLOWLIST OF COUNTS: there is
// no field here that could carry a voter.
type Result struct {
	ID              int64          `json:"id"`
	Question        string         `json:"question"`
	Options         []OptionResult `json:"options"`
	Total           int            `json:"total"`
	ByPlatform      map[string]int `json:"byPlatform"`
	State           State          `json:"state"`
	DurationSeconds int            `json:"durationSeconds"`
	StartedAt       time.Time      `json:"startedAt"`
	EndsAt          time.Time      `json:"endsAt"`
	EndedAt         *time.Time     `json:"endedAt,omitempty"`
	// EndedEarly: an operator closed it before its time.
	EndedEarly bool           `json:"endedEarly,omitempty"`
	Announced  []Announcement `json:"announced,omitempty"`
	Native     []NativeResult `json:"native,omitempty"`
}

// Winners returns the indexes of the options with the most votes, or none
// when nobody voted.
func (r Result) Winners() []int {
	best := 0
	for _, o := range r.Options {
		if o.Votes > best {
			best = o.Votes
		}
	}
	if best == 0 {
		return nil
	}
	var w []int
	for i, o := range r.Options {
		if o.Votes == best {
			w = append(w, i)
		}
	}
	return w
}

// Display is the overlay's projection: labels and totals, no platform.
func (r Result) Display() eventstream.PollDisplay {
	d := eventstream.PollDisplay{
		ID:       "poll-" + strconv.FormatInt(r.ID, 10),
		Question: r.Question,
		Total:    r.Total,
		State:    string(r.State),
		EndsAt:   r.EndsAt,
		EndedAt:  r.EndedAt,
	}
	for _, o := range r.Options {
		d.Options = append(d.Options, eventstream.PollDisplayOption{Label: o.Label, Votes: o.Votes})
	}
	return d
}
