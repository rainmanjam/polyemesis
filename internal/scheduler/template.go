package scheduler

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Recurring broadcast templates (#18).
//
// A template is the description of a show that is attached to a START schedule:
// the title pattern, description, privacy, category and thumbnail that every
// occurrence of that schedule is created with. It is owned here, in the
// scheduler package, because the schedule is what it hangs off. The planned
// rollover lane (#19) reads the same type and must not redefine it.
//
// Nothing in this file talks to a platform or to the database. It is the
// vocabulary: how a template is validated, how its title is rendered for one
// occurrence, and which instants a look-ahead asks for. The sweep that acts on
// those instants lives in internal/api, beside the OAuth tokens, for the same
// reason preannounce.go does.

// Bounds a template is checked against. YouTube's own limits, stated here so a
// refusal happens before a platform call and not inside one.
const (
	// DefaultLookahead is how many occurrences are kept created ahead of time
	// when a template does not say otherwise.
	DefaultLookahead = 2
	// MaxLookahead bounds how many upcoming platform events one template may
	// hold open. Each one is a public event page, so the bound is a safety
	// rail, not a tuning knob.
	MaxLookahead = 10
	// MaxTitleLen is YouTube's title limit, applied to the RENDERED title.
	MaxTitleLen = 100
	// MaxDescriptionLen is YouTube's description limit.
	MaxDescriptionLen = 5000
	// MaxThumbnailBytes is YouTube's thumbnail upload limit (2 MB).
	MaxThumbnailBytes = 2 << 20
)

// The privacy values a template may carry. These are YouTube's privacyStatus
// words; a template is a YouTube-shaped description even when other platforms
// read the same row.
var templatePrivacy = []string{"public", "unlisted", "private"}

// tokenPattern finds every {name} in a title pattern. Anything that is not one
// of the known tokens is refused, so a typo cannot reach a viewer as brace text.
var tokenPattern = regexp.MustCompile(`\{([^{}]*)\}`)

// knownTokens are the substitutions RenderTitle performs.
var knownTokens = map[string]bool{"date": true, "time": true}

// BroadcastTemplate is the per-schedule description of an occurrence's
// platform event.
type BroadcastTemplate struct {
	// ScheduleID is the start schedule this template is attached to. One
	// template per schedule.
	ScheduleID int64 `json:"scheduleId"`
	// TitlePattern is the title, with {date} (YYYY-MM-DD) and {time} (HH:MM)
	// replaced by the occurrence's own wall clock in the schedule's zone.
	TitlePattern string `json:"titlePattern"`
	// Description is the event's description, the same on every occurrence.
	Description string `json:"description"`
	// Privacy is public, unlisted or private. Empty means public.
	Privacy string `json:"privacy"`
	// Category is the platform's category name. Carried for the platforms that
	// set it at go-live; YouTube's category is applied to the video at create.
	Category string `json:"category"`
	// Lookahead is how many upcoming occurrences are kept created. Zero means
	// DefaultLookahead.
	Lookahead int `json:"lookahead"`
	// HasThumbnail reports whether a thumbnail is stored. The bytes themselves
	// are not part of the JSON shape, because a 2 MB field on every read is
	// not what a settings screen needs.
	HasThumbnail bool `json:"hasThumbnail"`
	// Thumbnail is the image bytes, as stored. It travels to the platform write
	// and nowhere else.
	Thumbnail     []byte `json:"-"`
	ThumbnailMIME string `json:"-"`
}

// Normalized fills the defaults and trims the text. Two templates that mean the
// same thing store the same bytes, as Schedule.Normalized arranges for schedules.
func (t BroadcastTemplate) Normalized() BroadcastTemplate {
	t.TitlePattern = strings.TrimSpace(t.TitlePattern)
	t.Description = strings.TrimSpace(t.Description)
	t.Category = strings.TrimSpace(t.Category)
	t.Privacy = strings.ToLower(strings.TrimSpace(t.Privacy))
	if t.Privacy == "" {
		t.Privacy = "public"
	}
	if t.Lookahead <= 0 {
		t.Lookahead = DefaultLookahead
	}
	return t
}

// Validate rejects a template that could not produce a valid broadcast. It
// renders the title against a fixed sample instant, so a pattern that is valid
// as a pattern but too long once the date is filled in is caught here.
func (t BroadcastTemplate) Validate() error {
	n := t.Normalized()
	if n.ScheduleID <= 0 {
		return fmt.Errorf("a broadcast template belongs to a schedule, and this one names none")
	}
	if n.TitlePattern == "" {
		return fmt.Errorf("a broadcast template needs a title pattern")
	}
	sample, err := RenderTitle(n.TitlePattern, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.UTC)
	if err != nil {
		return err
	}
	if len([]rune(sample)) > MaxTitleLen {
		return fmt.Errorf("the title pattern renders to more than %d characters once the date is "+
			"filled in; shorten it", MaxTitleLen)
	}
	if len(n.Description) > MaxDescriptionLen {
		return fmt.Errorf("the description is longer than %d characters", MaxDescriptionLen)
	}
	if !contains(templatePrivacy, n.Privacy) {
		return fmt.Errorf("privacy %q is not one of public, unlisted or private", n.Privacy)
	}
	if n.Lookahead < 1 || n.Lookahead > MaxLookahead {
		return fmt.Errorf("the look-ahead must be between 1 and %d occurrences", MaxLookahead)
	}
	if len(n.Thumbnail) > MaxThumbnailBytes {
		return fmt.Errorf("the thumbnail is larger than %d bytes", MaxThumbnailBytes)
	}
	return nil
}

// RenderTitle substitutes {date} and {time} for one occurrence. The instant is
// read in loc, the schedule's zone, so the title and the start time on the
// event page agree. An unknown token is an error, naming the token.
func RenderTitle(pattern string, at time.Time, loc *time.Location) (string, error) {
	if loc == nil {
		loc = time.UTC
	}
	local := at.In(loc)
	var bad string
	out := tokenPattern.ReplaceAllStringFunc(pattern, func(m string) string {
		name := m[1 : len(m)-1]
		if !knownTokens[name] {
			if bad == "" {
				bad = m
			}
			return m
		}
		switch name {
		case "date":
			return local.Format("2006-01-02")
		default:
			return local.Format("15:04")
		}
	})
	if bad != "" {
		return "", fmt.Errorf("the title pattern uses %s, which is not a token; the tokens are {date} and {time}", bad)
	}
	return strings.TrimSpace(out), nil
}

// Occurrences is the next n instants the schedule will fire at, strictly after
// now, in order. It is Next applied repeatedly, so every zone and daylight-
// saving rule Next honours is honoured here too.
//
// A schedule that cannot be evaluated plans nothing. n is bounded by
// MaxLookahead, so a caller that passes something larger gets the bound and not
// a runaway loop.
func Occurrences(s Schedule, now time.Time, n int) []time.Time {
	if n <= 0 {
		return nil
	}
	if n > MaxLookahead {
		n = MaxLookahead
	}
	s = s.Normalized()
	if _, err := s.Location(); err != nil {
		return nil
	}
	out := make([]time.Time, 0, n)
	cursor := now
	for len(out) < n {
		at, ok := Next(s, cursor)
		if !ok {
			break
		}
		out = append(out, at)
		cursor = at
	}
	return out
}

// Occurrence is one planned instance of a template on one destination: the
// platform event that was created for it, or the record that the operator
// skipped it.
type Occurrence struct {
	ID            int64     `json:"id"`
	ScheduleID    int64     `json:"scheduleId"`
	DestinationID int64     `json:"destinationId"`
	At            time.Time `json:"at"`
	// State is one of the OccurrenceState values below.
	State OccurrenceState `json:"state"`
	// PlatformEventID is the broadcast id the platform returned. Empty while a
	// create is in flight, and for a skipped occurrence.
	PlatformEventID string `json:"platformEventId"`
	// Title is the title that was written to the event, after rendering.
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// OccurrenceState is where one occurrence's platform event stands.
type OccurrenceState string

const (
	// OccurrencePending is a claim: the row exists before the platform call, so
	// two sweeps cannot both create the event for the same occurrence. It is
	// resolved to upcoming on success and removed on failure.
	OccurrencePending OccurrenceState = "pending"
	// OccurrenceUpcoming means the platform event exists and is waiting for the
	// show.
	OccurrenceUpcoming OccurrenceState = "upcoming"
	// OccurrenceSkipped means the operator removed this occurrence. The row is
	// kept so the look-ahead does not create it again.
	OccurrenceSkipped OccurrenceState = "skipped"
	// OccurrenceStranded means the platform event exists but carries no title the operator chose: its
	// title write was refused and its cancel was refused too. The event id is kept, so the sweep keeps
	// retrying the cancel. It is never a show, so it is not listed as upcoming and does not count
	// toward the look-ahead.
	OccurrenceStranded OccurrenceState = "stranded"
)

// contains reports whether want is one of values.
func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
