package chat

// The recommended-protection pack on Twitch's own filters: the pack's severe
// terms as Twitch blocked terms, and the AutoMod level.
//
// This is SyncBlockedTerms with a memory. That function adds and never removes
// because it cannot tell its own terms from the operator's. Here the caller
// keeps the id of every term THIS code created (and nothing else), so a later
// sync or a reversal deletes exactly those and no others.
//
// HOW "OURS" IS DECIDED. Twitch answers a duplicate add with the EXISTING term,
// and private terms (the ones AutoMod keeps to itself) are invisible to the
// list read, so an add can come back carrying somebody else's term. A term
// counts as ours only when it was absent from the list this sync just read AND
// its created_at falls within ownedWithin of Twitch's own clock (the Date
// header of the add's response; the local clock only when Twitch sent none).
// Twitch stamps created_at in whole seconds on its own clock, so comparing it
// with the local time.Now() would misread every add as old whenever the local
// clock runs ahead. Every doubtful case errs toward "not ours", which leaves a
// term behind rather than deleting one the operator wrote.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// twitchSyncMargin is how long before the call's deadline a sync stops starting
// new Helix calls, so the answer reaches the caller instead of being cut off
// with the work half-recorded.
const twitchSyncMargin = 4 * time.Second

// TwitchPackBatch is how many terms one sync call adds. The caller re-runs until
// Remaining is zero; a sync is idempotent, so a re-run costs only the list
// read. A variable so a test can make the resumable loop take more than a pass.
var TwitchPackBatch = 250

// ownedWithin is how far before the add's response a term's created_at may be
// and still be the add's own work. created_at is whole seconds, so the add
// itself can read up to a second old; the rest is slack for the request.
const ownedWithin = 5 * time.Second

// TermKey is what the record keeps in place of a term: a short hash of its
// lower-cased text. Reconciliation only needs to match a recorded id with a
// wanted or listed term, and a stored settings document must not hold the slur
// list it is guarding against.
func TermKey(term string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(term))))
	return hex.EncodeToString(sum[:8])
}

// TrackedTermSync reports one pass of the pack's term sync. It carries counts,
// never the terms: they are a slur list, and the console shows how many.
type TrackedTermSync struct {
	Added   int `json:"added"`
	Present int `json:"present"`
	Removed int `json:"removed"`
	// Restored counts terms polyemesis had added, the operator removed on
	// Twitch, and this pass put back (they are in Added too).
	Restored  int            `json:"restored"`
	Skipped   int            `json:"skipped"`
	SkipWhy   map[string]int `json:"skipWhy,omitempty"`
	Remaining int            `json:"remaining"`
	Done      bool           `json:"done"`

	// AddedIDs and RemovedIDs are the bookkeeping for the caller to store. They
	// are never put on the wire.
	AddedIDs   map[string]string `json:"-"`
	RemovedIDs []string          `json:"-"`
}

func (r *TrackedTermSync) skip(why string) {
	r.Skipped++
	if r.SkipWhy == nil {
		r.SkipWhy = map[string]int{}
	}
	r.SkipWhy[why]++
}

// ours reports whether a term Twitch returned from an add was created by that
// add: it carries an id and was created no earlier than ownedWithin before ref,
// the moment Twitch answered. ref is Twitch's clock where known, so the local
// clock's offset cannot matter, and both sides are whole seconds.
func ours(b BlockedTerm, ref time.Time) bool {
	if b.ID == "" {
		return false
	}
	at, err := time.Parse(time.RFC3339Nano, b.CreatedAt)
	return err == nil && !at.Before(ref.Add(-ownedWithin))
}

// nearDeadline reports the context has less than the margin left.
func nearDeadline(ctx context.Context) bool {
	if dl, ok := ctx.Deadline(); ok {
		return time.Until(dl) < twitchSyncMargin
	}
	return false
}

// SyncTrackedTerms reconciles Twitch's blocked terms with want, touching only
// what polyemesis added.
//
//   - A recorded term no longer wanted is deleted (a category turned off, a
//     pack update that dropped it, an operator allow).
//   - A wanted term not on Twitch is added, at most maxAdds per call, and
//     recorded when it is ours. That includes one polyemesis added earlier and
//     the operator removed on Twitch: it comes back, and Restored says how many
//     did (to keep a term off Twitch, allow it in polyemesis).
//   - A wanted term already on Twitch is left alone and, unless it is already
//     recorded, never recorded: it is somebody else's.
//
// The result is useful even when the error is not nil: AddedIDs and RemovedIDs
// say what really happened before the stop.
//
// recorded maps a Twitch term id to the TermKey of its term.
func (t *TwitchAdapter) SyncTrackedTerms(ctx context.Context, want []string, recorded map[string]string, maxAdds int) (TrackedTermSync, error) {
	res := TrackedTermSync{AddedIDs: map[string]string{}}
	if maxAdds <= 0 {
		maxAdds = TwitchPackBatch
	}
	existing, err := t.ListBlockedTerms(ctx)
	if err != nil {
		return res, err
	}
	have := make(map[string]bool, len(existing))
	listed := make(map[string]bool, len(existing))
	for _, e := range existing {
		have[TermKey(e.Text)] = true
		listed[e.ID] = true
	}
	wanted := make(map[string]bool, len(want))
	for _, w := range want {
		wanted[TermKey(w)] = true
	}
	forgotten := map[string]bool{}
	// Recorded ids that are still wanted stay recorded, and count as present.
	stale := map[string]string{}
	for id, key := range recorded {
		switch {
		case !wanted[key]:
			stale[id] = key
		case !have[key]:
			// Ours once, gone from Twitch since (the operator removed it).
			// Forgotten, not deleted; the add below puts it back under a
			// new id, and Restored says so.
			res.RemovedIDs = append(res.RemovedIDs, id)
			forgotten[key] = true
		}
	}
	if err := t.removeTracked(ctx, stale, &res); err != nil {
		return res, err
	}
	res.Remaining = len(stale) - res.Removed

	seen := map[string]bool{}
	adds := 0
	var pending []string
	for _, raw := range want {
		term := strings.TrimSpace(raw)
		key := TermKey(term)
		switch {
		case seen[key]:
			continue
		case strings.Contains(term, "*"):
			res.skip("wildcard")
		case termProblem(term) != "":
			res.skip("length")
		case have[key]:
			res.Present++
		default:
			pending = append(pending, term)
		}
		seen[key] = true
	}
	for i, term := range pending {
		if adds >= maxAdds || nearDeadline(ctx) {
			res.Remaining += len(pending) - i
			break
		}
		got, ref, err := t.addBlockedTerm(ctx, term)
		if err != nil {
			res.Remaining += len(pending) - i
			return res, fmt.Errorf("stopped after adding %d of %d: %w", adds, len(pending), err)
		}
		adds++
		if !listed[got.ID] && ours(got, ref) {
			res.AddedIDs[got.ID] = TermKey(term)
			res.Added++
			if forgotten[TermKey(term)] {
				res.Restored++
			}
		} else {
			// Twitch already had it (a private term, or one added since the
			// list read): present, and not ours to remove later.
			res.Present++
		}
	}
	res.Done = res.Remaining == 0
	return res, nil
}

// RemoveTrackedTerms deletes the recorded terms: the reversal. Only ids the
// caller recorded are touched. It stops near the deadline with Remaining set,
// and the caller re-runs.
func (t *TwitchAdapter) RemoveTrackedTerms(ctx context.Context, recorded map[string]string) (TrackedTermSync, error) {
	res := TrackedTermSync{AddedIDs: map[string]string{}}
	err := t.removeTracked(ctx, recorded, &res)
	res.Remaining = len(recorded) - res.Removed
	res.Done = err == nil && res.Remaining == 0
	return res, err
}

// removeTracked deletes each id, in a stable order, stopping near the deadline.
// Twitch answers 204 for an id it does not have, so a term the operator already
// removed is simply forgotten.
func (t *TwitchAdapter) removeTracked(ctx context.Context, ids map[string]string, res *TrackedTermSync) error {
	order := make([]string, 0, len(ids))
	for id := range ids {
		order = append(order, id)
	}
	sort.Strings(order)
	for _, id := range order {
		if nearDeadline(ctx) {
			return nil
		}
		if err := t.DeleteBlockedTerm(ctx, id); err != nil {
			return fmt.Errorf("stopped after removing %d of %d: %w", res.Removed, len(ids), err)
		}
		res.RemovedIDs = append(res.RemovedIDs, id)
		res.Removed++
	}
	return nil
}

// RaiseAutoModLevel makes every Twitch AutoMod category at least level, except
// swearing, which is left as the channel has it (the pack's profanity ships
// off, and a channel's swearing level is its own choice). It never lowers a
// category an operator set stricter. The PUT replaces Twitch's settings, so all
// eight categories are sent with the current value for the ones kept; the
// overall level alone would reset swearing. before and after are the levels
// either side.
func (t *TwitchAdapter) RaiseAutoModLevel(ctx context.Context, level int) (before, after AutoModSettings, err error) {
	if level < 1 || level > 2 {
		return before, after, fmt.Errorf("the one-click AutoMod level is 1 or 2, not %d", level)
	}
	if before, err = t.GetAutoMod(ctx); err != nil {
		return before, after, err
	}
	next := before
	next.OverallLevel = nil // never both, and never the overall level alone: it would reset swearing
	for _, p := range []*int{&next.Aggression, &next.Bullying, &next.Disability, &next.Misogyny,
		&next.RaceEthnicityReligion, &next.SexBasedTerms, &next.SexualitySexGender} {
		if *p < level {
			*p = level
		}
	}
	after, err = t.SetAutoMod(ctx, next)
	return before, after, err
}
