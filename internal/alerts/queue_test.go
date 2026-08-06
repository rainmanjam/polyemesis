package alerts

import (
	"strconv"
	"testing"
	"time"
)

var base = time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)

func testRule(mut ...func(*Rule)) Rule {
	r := Rule{
		ID: 1, Name: "ops", Enabled: true, Format: FormatJSON,
		URL: "https://example.test/hook", DebounceSeconds: 10, MinIntervalSeconds: 30,
	}
	for _, m := range mut {
		m(&r)
	}
	return r.Normalized()
}

func downEvent(id string, at time.Time) Event {
	return Event{
		Type: TypeDestinationDown, Severity: SeverityCritical, Key: "destination:" + id,
		Title: "Destination down: " + id, At: at,
	}
}

// Eviction is oldest-first, and takes no account of severity.
//
// This is written down because destination.falling_behind made it matter more.
// Each destination now holds up to TWO pending groups rather than one --
// "destination:<id>" and "destination:<id>:speed" -- so an endpoint that is
// unreachable starts evicting at roughly 32 destinations instead of 64.
//
// The loss itself is by design: the cap exists so an endpoint that has been
// down for an hour cannot hold the process's memory, and the drop is counted.
// What is NOT obviously right is that a critical destination.down can be
// dropped in favour of a newer warning-level falling_behind purely because it
// is older. Making eviction severity-aware is a change to shared alerting
// behaviour and did not belong in the feature that surfaced it, so the current
// behaviour is pinned here instead: the day somebody changes it should be a
// day they meant to.
func TestCoalescerEvictionIsOldestFirstNotSeverityAware(t *testing.T) {
	c := newCoalescer()
	rules := []Rule{testRule()}

	// A critical event about the first subject, before everything else.
	c.Add(rules, downEvent("first", base), base)

	// Fill past the cap with newer, quieter groups -- the shape of many
	// destinations each holding a speed group alongside their down group.
	for i := 0; i < maxGroupsPerRule; i++ {
		at := base.Add(time.Duration(i+1) * time.Second)
		c.Add(rules, Event{
			Type: TypeDestinationFallingBehind, Severity: SeverityWarning,
			Key: "destination:" + strconv.Itoa(i) + ":speed", At: at,
		}, at)
	}

	if c.dropped == 0 {
		t.Fatal("nothing was evicted past the cap; the bound is not doing its job")
	}
	// The oldest went, and it was the critical one. If this ever starts
	// passing with the critical group retained, eviction has been made
	// severity-aware -- update the design note in
	// docs/DESIGN-DESTINATION-HEALTH.md rather than deleting this test.
	if _, kept := c.groups[groupKey{rule: rules[0].ID, key: "destination:first"}]; kept {
		t.Error("the oldest critical group survived; eviction is no longer oldest-first")
	}
}

// The headline behaviour: a destination that flaps must not send a message per
// flap.
func TestCoalescerFoldsAFlappingSubjectIntoOneDelivery(t *testing.T) {
	c := newCoalescer()
	rules := []Rule{testRule()}

	for i := 0; i < 200; i++ {
		at := base.Add(time.Duration(i) * 50 * time.Millisecond)
		c.Add(rules, downEvent("3", at), at)
	}
	if got := c.Due(rules, base.Add(5*time.Second)); len(got) != 0 {
		t.Fatalf("Due before the debounce window elapsed = %d deliveries, want 0", len(got))
	}

	got := c.Due(rules, base.Add(11*time.Second))
	if len(got) != 1 {
		t.Fatalf("Due after the window = %d deliveries, want 1", len(got))
	}
	if n := len(got[0].Items); n != 1 {
		t.Fatalf("delivery carried %d items, want 1 subject", n)
	}
	if got[0].Items[0].Count != 200 {
		t.Errorf("Count = %d, want all 200 occurrences folded in", got[0].Items[0].Count)
	}
	if c.Pending() != 0 {
		t.Errorf("Pending = %d after the flush, want 0", c.Pending())
	}
}

func TestCoalescerRateLimitDelaysRatherThanDiscards(t *testing.T) {
	c := newCoalescer()
	rules := []Rule{testRule(func(r *Rule) { r.DebounceSeconds = 1; r.MinIntervalSeconds = 60 })}

	c.Add(rules, downEvent("1", base), base)
	first := c.Due(rules, base.Add(2*time.Second))
	if len(first) != 1 {
		t.Fatalf("first Due = %d deliveries, want 1", len(first))
	}

	// A second subject inside the floor. It must be held, not dropped.
	at := base.Add(5 * time.Second)
	c.Add(rules, downEvent("2", at), at)
	if got := c.Due(rules, base.Add(10*time.Second)); len(got) != 0 {
		t.Fatalf("Due inside the rate-limit floor = %d deliveries, want 0", len(got))
	}
	if c.Pending() != 1 {
		t.Fatalf("Pending = %d inside the floor, want the event held", c.Pending())
	}

	got := c.Due(rules, base.Add(70*time.Second))
	if len(got) != 1 || len(got[0].Items) != 1 {
		t.Fatalf("Due past the floor = %+v, want the held event", got)
	}
	if got[0].Items[0].Event.Key != "destination:2" {
		t.Errorf("held event = %q, want destination:2", got[0].Items[0].Event.Key)
	}
}

func TestCoalescerBatchesDistinctSubjectsIntoOneMessage(t *testing.T) {
	c := newCoalescer()
	rules := []Rule{testRule(func(r *Rule) { r.DebounceSeconds = 5 })}

	for _, id := range []string{"1", "2", "3"} {
		c.Add(rules, downEvent(id, base), base)
	}
	got := c.Due(rules, base.Add(6*time.Second))
	if len(got) != 1 {
		t.Fatalf("Due = %d deliveries, want one message for the rule", len(got))
	}
	if len(got[0].Items) != 3 {
		t.Fatalf("delivery carried %d items, want 3 subjects", len(got[0].Items))
	}
	// Arrival order, so the message reads the way things went wrong.
	want := []string{"destination:1", "destination:2", "destination:3"}
	for i, w := range want {
		if got[0].Items[i].Event.Key != w {
			t.Errorf("item %d = %q, want %q", i, got[0].Items[i].Event.Key, w)
		}
	}
}

func TestCoalescerRespectsSubscriptionAndSeverity(t *testing.T) {
	tests := []struct {
		name string
		rule Rule
		ev   Event
		want int
	}{
		{
			name: "an empty subscription list takes everything",
			rule: testRule(),
			ev:   downEvent("1", base),
			want: 1,
		},
		{
			name: "a subscription the event is not in",
			rule: testRule(func(r *Rule) { r.Events = []Type{TypeDiskLow} }),
			ev:   downEvent("1", base),
			want: 0,
		},
		{
			name: "a severity floor above the event",
			rule: testRule(func(r *Rule) { r.MinSeverity = SeverityCritical }),
			ev:   Event{Type: TypeClipping, Severity: SeverityWarning, Key: "clip", At: base},
			want: 0,
		},
		{
			name: "a disabled rule hears nothing",
			rule: testRule(func(r *Rule) { r.Enabled = false }),
			ev:   downEvent("1", base),
			want: 0,
		},
		{
			name: "a test event bypasses both filters",
			rule: testRule(func(r *Rule) { r.Events = []Type{TypeDiskLow}; r.MinSeverity = SeverityCritical }),
			ev:   Event{Type: TypeTest, Severity: SeverityInfo, Key: "test", At: base},
			want: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newCoalescer()
			if got := c.Add([]Rule{tt.rule}, tt.ev, base); got != tt.want {
				t.Errorf("Add took %d rules, want %d", got, tt.want)
			}
		})
	}
}

func TestCoalescerCapsWhatOneRuleCanAccumulate(t *testing.T) {
	c := newCoalescer()
	rules := []Rule{testRule(func(r *Rule) { r.DebounceSeconds = 3600 })}

	for i := 0; i < maxGroupsPerRule+20; i++ {
		c.Add(rules, downEvent(string(rune('a'+i%26))+string(rune('a'+i/26)), base), base)
	}
	if c.Pending() > maxGroupsPerRule {
		t.Errorf("Pending = %d, want it capped at %d", c.Pending(), maxGroupsPerRule)
	}
	if c.dropped == 0 {
		t.Error("evictions were not counted, so an endpoint that is down would drop alerts silently")
	}
}

func TestCoalescerSplitsAnOversizedDeliveryIntoAnOverflowCount(t *testing.T) {
	c := newCoalescer()
	rules := []Rule{testRule(func(r *Rule) { r.DebounceSeconds = 1 })}

	for i := 0; i < maxItemsPerDelivery+5; i++ {
		c.Add(rules, downEvent(string(rune('a'+i)), base), base)
	}
	got := c.Due(rules, base.Add(2*time.Second))
	if len(got) != 1 {
		t.Fatalf("Due = %d deliveries, want 1", len(got))
	}
	if len(got[0].Items) != maxItemsPerDelivery {
		t.Errorf("Items = %d, want %d", len(got[0].Items), maxItemsPerDelivery)
	}
	if got[0].Overflow != 5 {
		t.Errorf("Overflow = %d, want 5", got[0].Overflow)
	}
}

func TestCoalescerRequeueRewindsTheRateLimit(t *testing.T) {
	c := newCoalescer()
	rules := []Rule{testRule(func(r *Rule) { r.DebounceSeconds = 1; r.MinIntervalSeconds = 60 })}

	c.Add(rules, downEvent("1", base), base)
	got := c.Due(rules, base.Add(2*time.Second))
	if len(got) != 1 {
		t.Fatalf("Due = %d, want 1", len(got))
	}
	c.Requeue(got[0], base.Add(2*time.Second))

	// The delivery never left, so the endpoint's budget must be untouched.
	again := c.Due(rules, base.Add(3*time.Second))
	if len(again) != 1 {
		t.Fatalf("Due after Requeue = %d deliveries, want the delivery back", len(again))
	}
	if again[0].Items[0].Count != 1 {
		t.Errorf("Count = %d after a round trip, want 1", again[0].Items[0].Count)
	}
}

func TestCoalescerForgetsDeletedRules(t *testing.T) {
	c := newCoalescer()
	rules := []Rule{testRule(func(r *Rule) { r.DebounceSeconds = 3600 })}
	c.Add(rules, downEvent("1", base), base)

	c.Forget(map[int64]bool{})
	if c.Pending() != 0 {
		t.Errorf("Pending = %d after the rule was deleted, want 0", c.Pending())
	}
}
