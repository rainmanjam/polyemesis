package automod

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ModelChecker is one model connector: *Model (OpenAI-compatible) or *Jev.
//
// Check's contract is the one model.go states: on any error the findings are
// empty and the message passes, and the error is for the operator's stats.
type ModelChecker interface {
	Check(ctx context.Context, text string) ([]Finding, error)
	Stats() ModelStats
	Enabled() bool
}

// Chain is the two model checkers and the rule between them: the secondary is
// asked about a message ONLY when the primary could not answer it.
//
// "Could not answer" is any error from the primary: a timeout, a transport
// error, a non-200, a reply that is not the requested verdict, an unknown
// category, its own hourly budget spent, or a 429/529 pause. All of those
// already mean "the message passes" on a single checker; with two, they mean
// "ask the other one" first, and the message passes only when BOTH fail.
//
// An ANSWER is never a reason to ask again, and that includes a clean one. A
// primary that looked and found nothing has decided; asking the secondary as
// well would double the spend for a second opinion the matrix has no way to
// weigh, and turn the fallback into a tie-break nobody configured.
//
// A disabled primary with an enabled secondary leaves the secondary as the
// only checker. That is what an operator who switches a misbehaving primary
// off mid-incident is asking for, and a secondary that silently stopped
// because its primary was off would be a second, invisible outage.
type Chain struct {
	primary, secondary ModelChecker

	mu            sync.Mutex
	decisions     map[Slot]int
	fallbacks     int
	failedOpen    int
	lastDecidedBy Slot
}

// NewChain returns a chain. Either checker may be nil.
func NewChain(primary, secondary ModelChecker) *Chain {
	return &Chain{primary: primary, secondary: secondary, decisions: map[Slot]int{}}
}

func enabled(c ModelChecker) bool { return c != nil && c.Enabled() }

// Enabled reports whether either checker will make calls.
func (c *Chain) Enabled() bool {
	return c != nil && (enabled(c.primary) || enabled(c.secondary))
}

// Decide asks the primary, and the secondary only if the primary failed. It
// returns the findings, which checker answered, and -- only when nobody did --
// an error naming both failures.
func (c *Chain) Decide(ctx context.Context, text string) ([]Finding, Slot, error) {
	if c == nil {
		return nil, "", nil
	}
	var errs []error
	asked := 0
	for _, s := range []struct {
		slot Slot
		mc   ModelChecker
	}{{SlotPrimary, c.primary}, {SlotSecondary, c.secondary}} {
		if !enabled(s.mc) {
			continue
		}
		asked++
		findings, err := s.mc.Check(ctx, text)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s checker: %w", s.slot, err))
			continue
		}
		for i := range findings {
			findings[i].DecidedBy = s.slot
		}
		c.mu.Lock()
		c.decisions[s.slot]++
		c.lastDecidedBy = s.slot
		// A fallback is the secondary answering for a primary that was asked
		// and failed -- not the secondary answering because there is no
		// primary.
		if s.slot == SlotSecondary && len(errs) > 0 {
			c.fallbacks++
		}
		c.mu.Unlock()
		return findings, s.slot, nil
	}
	if asked == 0 {
		return nil, "", nil
	}
	c.mu.Lock()
	c.failedOpen++
	c.mu.Unlock()
	return nil, "", errors.Join(errs...)
}

// Check is Decide without the slot, so a Chain is itself a ModelChecker and
// can be handed to New like a single connector.
func (c *Chain) Check(ctx context.Context, text string) ([]Finding, error) {
	f, _, err := c.Decide(ctx, text)
	return f, err
}

// Stats is the primary's counters at the top level -- what a client written
// before there were two checkers reads -- plus both checkers' own counters and
// the chain's.
func (c *Chain) Stats() ModelStats {
	if c == nil {
		return ModelStats{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var out ModelStats
	if c.primary != nil {
		p := c.primary.Stats()
		p.Decisions = c.decisions[SlotPrimary]
		out = p
		out.Primary = &p
	}
	if c.secondary != nil {
		s := c.secondary.Stats()
		s.Decisions = c.decisions[SlotSecondary]
		out.Secondary = &s
	}
	out.Fallbacks = c.fallbacks
	out.FailedOpen = c.failedOpen
	out.LastDecidedBy = c.lastDecidedBy
	return out
}
