package alerts

import (
	"sort"
	"time"
)

const (
	// maxGroupsPerRule bounds what one endpoint can accumulate while it is
	// unreachable. Past it the oldest pending group is dropped: an endpoint
	// that has been down for an hour does not get to hold the process's memory
	// hostage, and the drop is counted so it is visible.
	maxGroupsPerRule = 64
	// maxItemsPerDelivery bounds one message. Discord refuses more than ten
	// embeds outright, and a message with fifty sections is not read by anybody
	// anyway; the overflow is summarised rather than sent.
	maxItemsPerDelivery = 10
)

// Item is one subject's worth of a delivery: the latest event about it, and
// how many times it was raised inside the debounce window.
type Item struct {
	Event Event
	Count int
	First time.Time
	Last  time.Time
}

// Delivery is one HTTP POST waiting to happen.
type Delivery struct {
	Rule  Rule
	Items []Item
	// Overflow is how many subjects did not fit in Items, so the message can
	// say so instead of quietly losing them.
	Overflow int
	// prevSent restores the rule's rate-limit clock if this delivery has to be
	// put back. Without it, a delivery that could not be handed to the sender
	// would still have spent the rule's token and delayed the next one.
	prevSent time.Time
	// hadSent distinguishes "never sent" from "sent at the zero time".
	hadSent bool
}

// group is one subject accumulating inside its debounce window.
type group struct {
	item  Item
	dueAt time.Time
	// seq preserves arrival order across a flush, so a delivery reads in the
	// order things went wrong.
	seq int64
}

type groupKey struct {
	rule int64
	key  string
}

// coalescer is the debounce and rate-limit state machine.
//
// It owns no goroutine and reads no clock: every method takes the current time.
// That is what makes "a destination that flaps twelve times in ten seconds
// sends one message" a table test rather than a stopwatch.
type coalescer struct {
	groups map[groupKey]*group
	// lastSent is the rate-limit clock, per rule.
	lastSent map[int64]time.Time
	seq      int64
	dropped  int64
}

func newCoalescer() *coalescer {
	return &coalescer{groups: map[groupKey]*group{}, lastSent: map[int64]time.Time{}}
}

// Add folds an event into the pending set for every rule that wants it.
// Returns how many rules took it, so a caller can tell "nobody subscribed"
// from "delivered".
func (c *coalescer) Add(rules []Rule, ev Event, now time.Time) int {
	if ev.At.IsZero() {
		ev.At = now
	}
	taken := 0
	for _, r := range rules {
		if !r.Enabled || !r.Wants(ev) {
			continue
		}
		taken++
		k := groupKey{rule: r.ID, key: ev.Key}
		if g, ok := c.groups[k]; ok {
			// The newest event wins the message body: an operator wants the
			// current state, with the count telling them how noisy getting
			// there was.
			g.item.Event = ev
			g.item.Count++
			g.item.Last = ev.At
			continue
		}
		c.evictIfFull(r.ID)
		c.seq++
		c.groups[k] = &group{
			item:  Item{Event: ev, Count: 1, First: ev.At, Last: ev.At},
			dueAt: now.Add(r.Debounce()),
			seq:   c.seq,
		}
	}
	return taken
}

// evictIfFull drops this rule's oldest pending group when the cap is reached.
func (c *coalescer) evictIfFull(ruleID int64) {
	var (
		n       int
		oldest  groupKey
		oldSeq  int64
		haveOld bool
	)
	for k, g := range c.groups {
		if k.rule != ruleID {
			continue
		}
		n++
		if !haveOld || g.seq < oldSeq {
			oldest, oldSeq, haveOld = k, g.seq, true
		}
	}
	if n < maxGroupsPerRule || !haveOld {
		return
	}
	delete(c.groups, oldest)
	c.dropped++
}

// Due collects everything ready to send, one delivery per rule.
//
// A rule that is inside its rate-limit floor is skipped whole: its groups stay
// pending and keep absorbing events, so the floor delays a message rather than
// discarding what it would have said.
func (c *coalescer) Due(rules []Rule, now time.Time) []Delivery {
	byRule := map[int64][]*group{}
	for k, g := range c.groups {
		if now.Before(g.dueAt) {
			continue
		}
		byRule[k.rule] = append(byRule[k.rule], g)
	}
	if len(byRule) == 0 {
		return nil
	}

	var out []Delivery
	for _, r := range rules {
		pending := byRule[r.ID]
		if len(pending) == 0 {
			continue
		}
		if last, ok := c.lastSent[r.ID]; ok && now.Sub(last) < r.MinInterval() {
			continue
		}
		sort.Slice(pending, func(i, j int) bool { return pending[i].seq < pending[j].seq })

		d := Delivery{Rule: r}
		d.prevSent, d.hadSent = c.lastSent[r.ID], hasKey(c.lastSent, r.ID)
		for _, g := range pending {
			if len(d.Items) < maxItemsPerDelivery {
				d.Items = append(d.Items, g.item)
			} else {
				d.Overflow++
			}
			delete(c.groups, groupKey{rule: r.ID, key: g.item.Event.Key})
		}
		c.lastSent[r.ID] = now
		out = append(out, d)
	}
	return out
}

// Requeue puts a delivery back, for when the sender could not accept it. The
// rate-limit clock is rewound with it, so a delivery that never left does not
// count against the endpoint's budget.
func (c *coalescer) Requeue(d Delivery, now time.Time) {
	if d.hadSent {
		c.lastSent[d.Rule.ID] = d.prevSent
	} else {
		delete(c.lastSent, d.Rule.ID)
	}
	for _, it := range d.Items {
		k := groupKey{rule: d.Rule.ID, key: it.Event.Key}
		if g, ok := c.groups[k]; ok {
			// Something arrived about this subject while the delivery was in
			// flight; keep the newer event and add the counts.
			g.item.Count += it.Count
			if it.First.Before(g.item.First) {
				g.item.First = it.First
			}
			continue
		}
		c.seq++
		// Due immediately: this was already past its debounce window once.
		c.groups[k] = &group{item: it, dueAt: now, seq: c.seq}
	}
}

// Forget drops every group belonging to rules that no longer exist, so a
// deleted rule's backlog does not outlive it.
func (c *coalescer) Forget(live map[int64]bool) {
	for k := range c.groups {
		if !live[k.rule] {
			delete(c.groups, k)
		}
	}
	for id := range c.lastSent {
		if !live[id] {
			delete(c.lastSent, id)
		}
	}
}

// Pending is how many subjects are waiting, for the stats endpoint.
func (c *coalescer) Pending() int { return len(c.groups) }

func hasKey(m map[int64]time.Time, id int64) bool {
	_, ok := m[id]
	return ok
}
