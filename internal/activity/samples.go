package activity

import (
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// DefaultSamplePlatform is the platform a test alert is attributed to when the
// caller names none.
const DefaultSamplePlatform = "twitch"

// testSeq makes every test alert's Source.ID distinct, so the hub's dedupe
// (which test events bypass anyway) and the console's keys never see two alike.
var testSeq atomic.Uint64

// Overrides is the part of a sample a caller may change: enough to preview a
// long name, a large amount or a message, nothing that could forge a source.
type Overrides struct {
	Name    string  `json:"name,omitempty"`
	Message string  `json:"message,omitempty"`
	Title   string  `json:"title,omitempty"`
	Amount  *Amount `json:"amount,omitempty"`
	Count   int     `json:"count,omitempty"`
	Viewers int     `json:"viewers,omitempty"`
	Months  int     `json:"months,omitempty"`
	Tier    string  `json:"tier,omitempty"`
}

// Sample builds a canned event of the given kind for platform, marked Test and
// carrying source type "polyemesis.test". It is what "Send test alert" sends
// through the hub like a real event, so the filter, the overlay gate, /ws, the
// events stream and hooks are all exercised with no account connected and no
// tier. It is also the producer the catalogue's emission tests drive.
//
// An unknown kind returns the zero Event, which Validate refuses.
func Sample(kind Kind, platform string, o Overrides, now time.Time) Event {
	platform = strings.ToLower(strings.TrimSpace(platform))
	if platform == "" {
		platform = DefaultSamplePlatform
	}
	n := testSeq.Add(1)
	e := Event{
		Kind:     kind,
		Platform: platform,
		At:       now,
		Source:   Source{Type: TestSourceType, ID: kindID(kind) + "-" + strconv.FormatUint(n, 10)},
		Test:     true,
	}
	viewer := &Actor{ID: "test-viewer", Login: "test_viewer", Name: "Test Viewer"}
	switch kind {
	case KindFollow:
		e.Actor = viewer
	case KindSubscribe:
		e.Actor, e.Tier, e.Product = viewer, "1000", "sub"
		e.Message = "Happy to be here!"
	case KindResub:
		e.Actor, e.Tier, e.Product = viewer, "1000", "sub"
		e.Months, e.Streak = 12, 12
		e.Message = "A year already!"
	case KindGift:
		e.Actor, e.Tier, e.Count = viewer, "1000", 5
		e.Recipients = []Recipient{{Name: "Guest One"}, {Name: "Guest Two"}, {Name: "Guest Three"}, {Name: "Guest Four"}, {Name: "Guest Five"}}
	case KindTip:
		e.Actor = viewer
		e.Amount = &Amount{Currency: "USD", Minor: 500, Exponent: 2}
		e.Message = "Great stream!"
	case KindRaid:
		e.Direction, e.Viewers = "in", 42
		e.Other = &Actor{ID: "test-channel", Login: "friend_channel", Name: "Friend Channel"}
	case KindRedemption:
		e.Actor, e.Title, e.Status = viewer, "Hydrate", "unfulfilled"
		e.Message = "Drink some water"
	case KindHypeTrain:
		e.Phase, e.Level = "progress", 3
		e.Progress = &Progress{Total: 1200, Goal: 2000}
	case KindPoll:
		e.Phase, e.Title = "begin", "What next?"
		e.Progress = &Progress{Options: []ProgressOption{{Label: "Yes"}, {Label: "No"}}}
	case KindPrediction:
		e.Phase, e.Title = "begin", "Will we win?"
		e.Progress = &Progress{Options: []ProgressOption{{Label: "Win"}, {Label: "Lose"}}}
	case KindStream:
		e.Phase, e.Title = "online", "Test stream"
	case KindShoutout:
		e.Direction, e.Viewers = "in", 120
		e.Other = &Actor{ID: "test-channel", Login: "friend_channel", Name: "Friend Channel"}
	case KindModeration:
		e.Action, e.Message = "timeout", "test reason"
		e.Actor = &Actor{Name: "Test Moderator"}
		e.Other = &Actor{ID: "test-target", Login: "test_target", Name: "Test Target"}
	case KindReaction:
		e.Count = 24
		e.Breakdown = map[string]int{"like": 18, "love": 6}
	default:
		return Event{}
	}
	return applyOverrides(e, o)
}

func kindID(k Kind) string { return string(k) }

func applyOverrides(e Event, o Overrides) Event {
	if o.Name != "" {
		switch {
		case e.Kind == KindRaid || e.Kind == KindShoutout:
			e.Other = &Actor{ID: e.Other.ID, Login: e.Other.Login, Name: o.Name}
		case e.Actor != nil:
			e.Actor = &Actor{ID: e.Actor.ID, Login: e.Actor.Login, Name: o.Name}
		}
	}
	if o.Message != "" {
		e.Message = o.Message
	}
	if o.Title != "" {
		e.Title = o.Title
	}
	if o.Amount != nil && e.Kind == KindTip {
		a := *o.Amount
		e.Amount = &a
	}
	if o.Count > 0 && (e.Kind == KindGift || e.Kind == KindReaction) {
		e.Count = o.Count
		if e.Kind == KindGift && len(e.Recipients) > o.Count {
			e.Recipients = e.Recipients[:o.Count]
		}
	}
	if o.Viewers > 0 && (e.Kind == KindRaid || e.Kind == KindShoutout) {
		e.Viewers = o.Viewers
	}
	if o.Months > 0 && e.Kind == KindResub {
		e.Months = o.Months
	}
	if o.Tier != "" {
		e.Tier = o.Tier
	}
	return e
}
