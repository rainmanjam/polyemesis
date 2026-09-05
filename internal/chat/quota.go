package chat

// The YouTube quota pacer.
//
// This exists because the obvious implementation of YouTube chat — poll
// liveChatMessages.list as fast as the API says you may — exhausts a default
// project's 10,000 units per day within a few hours, after which chat is
// silently dead until midnight Pacific. That outcome is treated here as a bug
// to be designed out, not as an operating condition to be reported.
//
// The design is a budget rather than a rate limit. At any moment the pacer
// knows how many units remain and how long until they reset, so the minimum
// sustainable spacing is arithmetic: time-until-reset divided by
// calls-still-affordable. Poll faster than that and chat dies early; poll at it
// and chat lasts exactly as long as the broadcast does. The API's own
// pollingIntervalMillis is then a floor on top, never a licence to ignore the
// budget.

import (
	"sync"
	"time"
)

// Quota unit costs. These are Google's published costs for the YouTube Data
// API at the time of writing and are ESTIMATES as far as this code is
// concerned — Google can change them, and a project on a raised quota has a
// different denominator entirely.
//
// Being wrong here changes only the pacing and the number shown to the
// operator, never whether polling is attempted: the API's own 403 remains the
// authority on when we are actually out. That is the deliberate choice. A
// budget that refused to poll because its arithmetic said so would be a check
// wrong in the restrictive direction, and this codebase has learned what that
// costs.
const (
	QuotaCostListMessages   = 5
	QuotaCostListBroadcasts = 1
	QuotaCostSendMessage    = 50
	// Moderation writes. Charged like any other write, but NEVER refused for
	// want of budget -- see the reserve note below and the check in Delete.
	// Declining to remove a message because arithmetic said the quota was low
	// would leave that message on stream, which is the one outcome worse than
	// spending the units.
	QuotaCostDeleteMessage = 50
	QuotaCostBan           = 50

	// DefaultQuotaUnits is a Google Cloud project's default daily allowance.
	DefaultQuotaUnits = 10000
	// DefaultQuotaReserve is held back so that sending never becomes
	// impossible because reading spent everything. Two hundred units is four
	// messages — enough to say "we are moving to Twitch" when it matters.
	DefaultQuotaReserve = 200

	// MinPollInterval floors the poll rate however generous the budget looks.
	// YouTube's own minimum is around five seconds and going below it earns a
	// rate-limit error rather than fresher chat.
	MinPollInterval = 5 * time.Second
	// MaxPollInterval caps the idle backoff. Beyond this the pane feels broken
	// even when it is working, so a budget that would justify a slower poll
	// pauses visibly instead of crawling invisibly.
	MaxPollInterval = 5 * time.Minute
	// IdleBackoffStep multiplies the interval for each consecutive empty poll,
	// and IdleBackoffMax caps it. An idle chat is where a whole day's quota
	// gets spent for nothing.
	IdleBackoffStep = 1.5
	IdleBackoffMax  = 8.0
)

// QuotaStatus is the budget as the UI shows it. It is deliberately explicit
// about being an estimate: an operator who sees "4,800 of 10,000 used" and
// then hits a limit at 9,000 because of a cost we got wrong should have been
// told which number was measured and which was inferred.
type QuotaStatus struct {
	// Used and Remaining are polyemesis's own tally of what it spent since the
	// last reset. They do not include anything else using the same Google
	// project.
	Used      int       `json:"used"`
	Limit     int       `json:"limit"`
	Remaining int       `json:"remaining"`
	ResetAt   time.Time `json:"resetAt"`
	// IntervalMS is the poll spacing currently in force, so "chat is a bit
	// behind" has a visible cause.
	IntervalMS int64 `json:"intervalMs"`
	// Paused reports that polling has stopped until the reset. It is DERIVED
	// from the other fields every time this struct is built rather than stored
	// on the budget, so it cannot outlive the condition that caused it — see
	// the note on budget.
	Paused bool `json:"paused,omitempty"`
	// Estimated is always true, and is in the payload so the UI can say so
	// rather than presenting an inference as a measurement.
	Estimated bool `json:"estimated"`
}

// QuotaUnits is a whole daily allowance and QuotaReserve is the slice of it
// held back for sends. They are two named types rather than two ints because
// they are the same shape, they travel together, and getting them the wrong way
// round is silent.
//
// poka-yoke: makes passing the reserve where the allowance belongs a compile
// error rather than a hundredfold pacing error [control]
//
// The mistake this blocks is concrete. #732 was an install with a raised quota
// that polled as though it had the default; the fix routed the operator's two
// numbers from settings into YouTubeConfig, where they sit as adjacent fields
// of identical type. Swapping them there — 10,000 units and a 200 reserve
// becoming a 200 allowance with a 10,000 reserve — compiles, passes every test
// that does not read the pacing, and then loses the reserve to the clamp below
// and paces against 200 units a day. That is #732 again, silently, from a
// two-line edit. With distinct types the swapped literal does not build.
//
// They are ints under the hood, and clampQuota is deliberately where they stop
// being distinct: inside the budget both are just units of the same currency
// and arithmetic between them is the point, so keeping the types past the
// boundary would buy conversions rather than safety.
type (
	QuotaUnits   int
	QuotaReserve int
)

// budget tracks unit spend against a daily allowance that resets at midnight
// Pacific.
//
// THERE IS NO paused FIELD, and that is the design rather than an omission.
// Whether polling has stopped is not a fact of its own: it is entirely decided
// by whether anything is still affordable, so storing it would create a second
// copy of an answer the numbers already give — one that can be set true by the
// path that noticed and then never cleared by the path that fixed it. That is
// exactly what happened: setLimits raised the allowance, left the flag standing,
// and chat stayed paused until midnight Pacific with 990,000 units to spend.
// status derives it instead, and a derived answer cannot disagree with itself.
type budget struct {
	mu      sync.Mutex
	limit   int
	reserve int
	used    int
	resetAt time.Time
	now     func() time.Time
	// interval is the last computed spacing, kept only for reporting.
	interval time.Duration
}

// clampQuota is the ONE spelling of what an acceptable allowance is, shared by
// construction and by setLimits.
//
// poka-yoke: keeps construction and a settings save from reading the same two
// stored numbers differently [control]
//
// It is a function rather than two copies because the two callers must agree:
// an operator who saves a reserve larger than their allowance would otherwise
// get "no reserve" at boot and "chat paused forever" after a save, from the
// same two numbers. The refusals below are each a decision, not a default.
//
// EVERY rule about an acceptable pair belongs in here, including the two that
// used to sit upstream in NewYouTube. Those were the same defect one level up:
// NewYouTube mapped a zero reserve to DefaultQuotaReserve before calling
// newBudget, and setLimits went straight to this function, which kept the zero
// — so the stored pair (10,000, 0) meant a 200-unit reserve at boot and no
// reserve at all after a save, and the operator's ability to reply to chat
// quietly disappeared the first time they pressed Save. A rule that lives
// outside the one function named for the rule is a rule with a second, silent
// spelling.
//
// It takes the two distinct types and returns plain ints because this is the
// boundary: past here they are the same currency and are added and subtracted
// against each other.
func clampQuota(limit QuotaUnits, reserve QuotaReserve) (int, int) {
	l, r := int(limit), int(reserve)
	if l <= 0 {
		l = DefaultQuotaUnits
	}
	if r <= 0 {
		// Zero means "unset", not "spend everything on reading". A row written
		// before the reserve field existed reads as zero, and honouring that
		// literally would take away the operator's ability to say anything on
		// stream at the exact moment they most need to.
		r = DefaultQuotaReserve
	}
	if r >= l {
		// A reserve that swallows the whole allowance would pause reading
		// permanently. Something is misconfigured; behave as if there is no
		// reserve rather than as if there is no chat. This is checked AFTER the
		// default above so that a small allowance is not handed a 200-unit
		// reserve it cannot afford.
		r = 0
	}
	return l, r
}

func newBudget(limit QuotaUnits, reserve QuotaReserve, now func() time.Time) *budget {
	if now == nil {
		now = time.Now
	}
	l, r := clampQuota(limit, reserve)
	b := &budget{limit: l, reserve: r, now: now}
	b.resetAt = quotaResetAfter(now())
	return b
}

// setLimits replaces the allowance on a budget that is already spending against
// it, so a settings save reaches a chat connection that has been up for hours.
//
// USED IS DELIBERATELY NOT RESET. The units already spent today were spent
// against YouTube's counter, not against this struct, and YouTube will not
// forget them because polyemesis was reconfigured. Clearing it here would let
// an operator mint themselves a fresh allowance by saving the settings page,
// and the first thing they would learn is that the real quota ran out anyway --
// at which point reads stop with no warning, which is the failure the reserve
// exists to prevent.
//
// NOTHING HERE CLEARS A PAUSE, because there is nothing to clear: status derives
// the pause from what is affordable, so raising the allowance on a budget that
// stopped at 4pm resumes it by arithmetic. The version of this function that
// shipped with a stored flag did not, and the operator who raised their quota
// and pressed Save got a chat that stayed dark until midnight Pacific.
func (b *budget) setLimits(limit QuotaUnits, reserve QuotaReserve) {
	l, r := clampQuota(limit, reserve)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.limit = l
	b.reserve = r
}

// spend records units and rolls the day over when the reset has passed.
func (b *budget) spend(units int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollLocked()
	b.used += units
}

// rollLocked resets the tally once the reset time has passed.
func (b *budget) rollLocked() {
	now := b.now()
	if now.Before(b.resetAt) {
		return
	}
	b.used = 0
	b.resetAt = quotaResetAfter(now)
}

// affordable reports how many more calls of the given cost fit in the budget,
// after the send reserve.
func (b *budget) affordable(cost int) int {
	if cost <= 0 {
		return 0
	}
	left := b.limit - b.reserve - b.used
	if left <= 0 {
		return 0
	}
	return left / cost
}

// interval computes the poll spacing to use next.
//
// apiInterval is what the API asked for (its pollingIntervalMillis), idle is
// the consecutive-empty-poll multiplier. The result is the slowest of: the
// floor, what the API asked, what idleness suggests, and what the budget can
// sustain until reset — because any one of those being ignored is a way for
// chat to stop working before the broadcast does.
func (b *budget) intervalFor(apiInterval time.Duration, idle float64) (time.Duration, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollLocked()

	calls := b.affordable(QuotaCostListMessages)
	if calls <= 0 {
		// The zero interval is a report, not a state: status says "paused" by
		// asking the same question this line just asked, so there is nothing
		// here to set and nothing anywhere else to remember to clear.
		b.interval = 0
		return 0, false
	}

	d := MinPollInterval
	if apiInterval > d {
		d = apiInterval
	}
	if idle > 1 {
		if idle > IdleBackoffMax {
			idle = IdleBackoffMax
		}
		if scaled := time.Duration(float64(d) * idle); scaled > d {
			d = scaled
		}
	}
	if d > MaxPollInterval {
		d = MaxPollInterval
	}

	// The sustainable spacing is allowed to exceed MaxPollInterval: when the
	// choice is between slow chat and no chat after 4pm, slow chat wins.
	if left := b.resetAt.Sub(b.now()); left > 0 {
		if sustainable := left / time.Duration(calls); sustainable > d {
			d = sustainable
		}
	}
	b.interval = d
	return d, true
}

// allow reports whether a call of this cost still fits, ignoring the read
// reserve. Sending is what the reserve exists for, so it spends into it.
func (b *budget) allow(cost int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollLocked()
	return b.limit-b.used >= cost
}

// pause stops polling until the reset, which is what a quotaExceeded response
// means whatever our own tally said. The platform is the authority; this is
// where our estimate gets corrected by it.
//
// It pauses by SPENDING rather than by raising a flag: writing the whole
// allowance to used is what "the platform says there is nothing left" actually
// means, and it is the same fact status reads. A flag alongside it would be a
// second copy that setLimits could not fix, which is the bug this shape exists
// to make unwritable.
func (b *budget) pause() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.used = b.limit
}

func (b *budget) status() QuotaStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollLocked()
	remaining := b.limit - b.used
	if remaining < 0 {
		remaining = 0
	}
	return QuotaStatus{
		Used:       b.used,
		Limit:      b.limit,
		Remaining:  remaining,
		ResetAt:    b.resetAt,
		IntervalMS: b.interval.Milliseconds(),
		// DERIVED, NEVER STORED. "Paused" is precisely "there is not one more
		// read left in the budget", so it is computed from the numbers in the
		// same breath they are reported. The version that stored it could
		// answer Paused=true and Remaining=990,000 at the same time, which is
		// how an operator who raised their quota at 4pm and pressed Save got a
		// chat that stayed dark until midnight Pacific.
		Paused:    b.affordable(QuotaCostListMessages) <= 0,
		Estimated: true,
	}
}

// pacificOnce guards the timezone lookup, which touches the filesystem.
var (
	pacificOnce sync.Once
	pacificLoc  *time.Location
)

// pacific is the zone YouTube's quota resets in.
//
// A machine with no timezone database — which on Windows is the normal case —
// falls back to a fixed −08:00 rather than failing. During daylight time that
// puts the computed reset an hour late, which makes the pacer slightly more
// conservative than it needs to be for one hour a day. That is the right
// direction to be wrong in, and it is a great deal better than refusing to run
// chat because a tzdata file is missing.
func pacific() *time.Location {
	pacificOnce.Do(func() {
		if loc, err := time.LoadLocation("America/Los_Angeles"); err == nil {
			pacificLoc = loc
			return
		}
		pacificLoc = time.FixedZone("PT", -8*60*60)
	})
	return pacificLoc
}

// quotaResetAfter is the next midnight Pacific strictly after t.
func quotaResetAfter(t time.Time) time.Time {
	loc := pacific()
	lt := t.In(loc)
	return time.Date(lt.Year(), lt.Month(), lt.Day()+1, 0, 0, 0, 0, loc)
}
