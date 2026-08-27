package automod

import (
	"sync"
	"time"
)

// Budget is the hourly spend ceiling, and it deliberately outlives the config.
//
// WHY IT IS NOT A FIELD ON Model. It was, and that put the only spend bound in
// the product inside an object rebuilt from scratch by ApplyAutomod on every
// settings save. Saving any automod setting -- or the API-key PUT -- refilled
// the allowance, so the ceiling was reset by the "tweak a setting mid-incident"
// reflex, which is precisely the moment someone is most likely to be saving
// settings and least likely to notice the bill. CallsThisHour went with it, so
// the evidence of the spend disappeared at the same instant as the limit.
//
// The window survives too. Restarting the hour on every save would let a save
// loop spend without bound while each individual window looked untouched.
//
// PASSED TO NewModel BY SIGNATURE, never defaulted, so a future constructor
// cannot quietly go back to owning its own counter: the compiler asks every
// caller where the budget came from, and there is exactly one answer per
// install.
type Budget struct {
	mu         sync.Mutex
	windowFrom time.Time
	calls      int

	// now is a seam for tests. Nil means time.Now, so the zero value works.
	now func() time.Time
}

// NewBudget returns a spend counter for one install. Create it once, where the
// process starts, and hand the same one to every model built afterwards.
func NewBudget() *Budget { return &Budget{} }

func (b *Budget) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

// reserve takes one call from the current hour, or reports that the ceiling is
// reached. A ceiling of zero or less means unlimited, which is the config's
// own convention.
//
// The ceiling is a PARAMETER rather than state: it lives in the config, which
// an operator may legitimately change, while the spend it bounds does not
// restart when they do. Lowering the ceiling below what has already been spent
// therefore stops the calls immediately, which is what someone lowering it
// mid-incident is asking for.
func (b *Budget) reserve(ceiling int) bool {
	if ceiling <= 0 {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clock()
	if now.Sub(b.windowFrom) >= time.Hour {
		b.windowFrom = now
		b.calls = 0
	}
	if b.calls >= ceiling {
		return false
	}
	b.calls++
	return true
}

// Spent is the number of calls taken in the current hour.
func (b *Budget) Spent() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}
