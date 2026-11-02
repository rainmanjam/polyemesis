// Package analytics is the cross-platform audience picture (Wave 4 #4): how
// many people are watching, on which platform, sampled while the stream is on
// air and kept for a per-stream report.
//
// THE ONE RULE THIS PACKAGE EXISTS TO HOLD: a count a platform did not report
// is not zero. oauth.LiveStats.ViewerCount is a pointer for that reason -- a
// hidden YouTube count, a Kick streamer who opted out, a Twitch channel that
// answered no stream -- and every number built here keeps the distinction. A
// reading with no count is "not reported", is shown as that, and is left out
// of every total and every average. It is never added as 0, and a total over
// readings none of which reported is absent, not 0.
//
// The package has no import of internal/api or internal/engine: the sampler
// reads the world through the Source interface and writes through Store, so
// the math and the state machine are tested without a server.
package analytics

import (
	"math"
	"sort"
	"time"
)

// PlatformPlayer is the self-hosted public player's row. Its count is the
// playout origin's own session table (playout.Analytics.Viewers), which is a
// separate audience from every platform's -- a viewer on the public page is
// not also counted by YouTube -- so it is a row of its own, added to the total
// once, and never folded into a platform.
const PlatformPlayer = "player"

// Reasons a reading carries no count. Closed set: the UI translates each one.
const (
	// ReasonOffline: the platform says this channel is not live.
	ReasonOffline = "offline"
	// ReasonHidden: the channel is live and the platform sent no count.
	ReasonHidden = "hidden"
	// ReasonQuota: the YouTube quota pacer refused the read.
	ReasonQuota = "quota"
	// ReasonError: the read failed.
	ReasonError = "error"
)

// Reading is one account's answer at one tick.
type Reading struct {
	Platform string `json:"platform"`
	// Account is platform_accounts.account_ref, empty for the player.
	Account string `json:"account,omitempty"`
	// Name is the account's display name, for the row label.
	Name string `json:"name,omitempty"`
	// Live is the platform's own verdict on whether this channel is on air.
	Live bool `json:"live"`
	// Viewers is nil when the platform did not report a count. omitempty so
	// the wire says the same thing the platform said: nothing.
	Viewers *int `json:"viewers,omitempty"`
	// Reason says why Viewers is nil. Empty when it is not.
	Reason string `json:"reason,omitempty"`
	// Title is the broadcast title the platform reported, if any. Not
	// persisted per sample; the stream keeps the first one seen.
	Title string `json:"-"`
}

// Reported reports whether the reading carries a count.
func (r Reading) Reported() bool { return r.Viewers != nil }

// Total sums the readings that carry a count and says how many did.
//
// The total is nil when none did: "nobody reported" is not "nobody is
// watching", and a dashboard showing 0 there is the false zero this package
// exists to prevent.
func Total(rs []Reading) (*int, int) {
	sum, n := 0, 0
	for _, r := range rs {
		if r.Viewers == nil {
			continue
		}
		sum += *r.Viewers
		n++
	}
	if n == 0 {
		return nil, 0
	}
	return &sum, n
}

// Sample is one stored reading.
type Sample struct {
	At       time.Time
	Platform string
	Account  string
	Name     string
	Viewers  *int
}

// Row is one platform account's line in a report.
type Row struct {
	Platform string `json:"platform"`
	Account  string `json:"account,omitempty"`
	Name     string `json:"name,omitempty"`
	// Samples is how many ticks asked; Reported how many of them got a count.
	Samples  int `json:"samples"`
	Reported int `json:"reported"`
	// Peak and Average are over the reported samples only, and nil when there
	// were none. An average that counted a not-reported tick as 0 would halve
	// a hidden-count channel's audience for every tick it was hidden.
	Peak    *int     `json:"peak,omitempty"`
	Average *float64 `json:"average,omitempty"`
}

// Point is one tick's total, for the sparkline. Total is nil for a tick at
// which nothing reported, so the line has a gap there rather than a dip to 0.
type Point struct {
	At    time.Time `json:"at"`
	Total *int      `json:"total,omitempty"`
}

// Summary is the audience half of a per-stream report.
type Summary struct {
	Rows []Row `json:"rows"`
	// PeakTotal is the highest TICK total: the most people watching at one
	// moment across every platform. Deliberately not the sum of per-platform
	// peaks, which adds moments that never happened together.
	PeakTotal *int `json:"peakTotal,omitempty"`
	// AverageTotal is the mean of tick totals over ticks where something
	// reported.
	AverageTotal *float64 `json:"averageTotal,omitempty"`
	// Points is every tick's total in time order.
	Points []Point `json:"points"`
}

type rowKey struct{ platform, account string }

// Summarise folds a stream's samples into its report rows and tick totals.
//
// Samples taken in one tick share an At, which is how ticks are told apart;
// the sampler stamps every reading of a tick with the tick's own time.
func Summarise(samples []Sample) Summary {
	type acc struct {
		row  Row
		sum  int
		peak int
	}
	rows := map[rowKey]*acc{}
	order := []rowKey{}
	ticks := map[int64]*Point{}
	tickOrder := []int64{}

	for _, s := range samples {
		k := rowKey{s.Platform, s.Account}
		a, ok := rows[k]
		if !ok {
			a = &acc{row: Row{Platform: s.Platform, Account: s.Account}}
			rows[k] = a
			order = append(order, k)
		}
		if s.Name != "" {
			a.row.Name = s.Name
		}
		a.row.Samples++

		at := s.At.UnixMilli()
		p, ok := ticks[at]
		if !ok {
			p = &Point{At: s.At}
			ticks[at] = p
			tickOrder = append(tickOrder, at)
		}
		if s.Viewers == nil {
			continue
		}
		v := *s.Viewers
		if a.row.Reported == 0 || v > a.peak {
			a.peak = v
		}
		a.row.Reported++
		a.sum += v
		if p.Total == nil {
			z := 0
			p.Total = &z
		}
		*p.Total += v
	}

	out := Summary{Rows: make([]Row, 0, len(order)), Points: make([]Point, 0, len(tickOrder))}
	for _, k := range order {
		a := rows[k]
		if a.row.Reported > 0 {
			peak := a.peak
			avg := round1(float64(a.sum) / float64(a.row.Reported))
			a.row.Peak, a.row.Average = &peak, &avg
		}
		out.Rows = append(out.Rows, a.row)
	}
	sort.SliceStable(out.Rows, func(i, j int) bool {
		// The player last: it is the self-hosted row, read after the platforms.
		pi, pj := out.Rows[i].Platform == PlatformPlayer, out.Rows[j].Platform == PlatformPlayer
		if pi != pj {
			return pj
		}
		if out.Rows[i].Platform != out.Rows[j].Platform {
			return out.Rows[i].Platform < out.Rows[j].Platform
		}
		return out.Rows[i].Account < out.Rows[j].Account
	})

	sort.Slice(tickOrder, func(i, j int) bool { return tickOrder[i] < tickOrder[j] })
	sum, n := 0, 0
	for _, at := range tickOrder {
		p := *ticks[at]
		out.Points = append(out.Points, p)
		if p.Total == nil {
			continue
		}
		if out.PeakTotal == nil || *p.Total > *out.PeakTotal {
			v := *p.Total
			out.PeakTotal = &v
		}
		sum += *p.Total
		n++
	}
	if n > 0 {
		avg := round1(float64(sum) / float64(n))
		out.AverageTotal = &avg
	}
	return out
}

// round1 keeps one decimal: an average of viewers to more precision than that
// is noise presented as a measurement.
func round1(v float64) float64 { return math.Round(v*10) / 10 }

// Chatter is one author's message count in one stream.
type Chatter struct {
	Platform string `json:"platform"`
	Name     string `json:"name"`
	Messages int    `json:"messages"`
}

// PlayerQoE is the public player's QoE counters over one stream: the delta
// between the counters at the stream's start and at its last tick.
type PlayerQoE struct {
	Sessions           int64 `json:"sessions"`
	Startups           int64 `json:"startups"`
	StartupMs          int64 `json:"startupMs"`
	Rebuffers          int64 `json:"rebuffers"`
	RebufferMs         int64 `json:"rebufferMs"`
	SessionsWithErrors int64 `json:"sessionsWithErrors"`
	// StartupHistogram is cumulative counts at or under each bound in
	// playout.StartupBucketsMs, then +Inf.
	StartupHistogram []int64 `json:"startupHistogram,omitempty"`
}

// Delta is end minus start, or false when any counter went DOWN -- the
// operator reset the player's analytics mid-stream, so the counters no longer
// share a baseline and any subtraction would be an invented number.
func (end PlayerQoE) Delta(start PlayerQoE) (PlayerQoE, bool) {
	d := PlayerQoE{
		Sessions:           end.Sessions - start.Sessions,
		Startups:           end.Startups - start.Startups,
		StartupMs:          end.StartupMs - start.StartupMs,
		Rebuffers:          end.Rebuffers - start.Rebuffers,
		RebufferMs:         end.RebufferMs - start.RebufferMs,
		SessionsWithErrors: end.SessionsWithErrors - start.SessionsWithErrors,
	}
	if d.Sessions < 0 || d.Startups < 0 || d.StartupMs < 0 || d.Rebuffers < 0 ||
		d.RebufferMs < 0 || d.SessionsWithErrors < 0 {
		return PlayerQoE{}, false
	}
	if len(end.StartupHistogram) == len(start.StartupHistogram) && len(end.StartupHistogram) > 0 {
		d.StartupHistogram = make([]int64, len(end.StartupHistogram))
		for i := range end.StartupHistogram {
			d.StartupHistogram[i] = end.StartupHistogram[i] - start.StartupHistogram[i]
			if d.StartupHistogram[i] < 0 {
				return PlayerQoE{}, false
			}
		}
	}
	return d, true
}
