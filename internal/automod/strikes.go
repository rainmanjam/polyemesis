package automod

import (
	"sort"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/automod/pack"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// Strikes and the action ladder (spec section 2.3).
//
// A pack hit is a strike against its author, per platform. The ladder turns the
// strike number into the most severe rung the operator has ARMED on that
// platform: strike 1 asks for a delete, 2 for a timeout, 3 and later for a ban,
// capped by the category's ceiling, and lowered to the highest rung actually
// armed at or below that. Nothing is armed by default, so on a fresh install the
// ladder resolves every hit to a plain flag and the only effect of a hit is the
// console flag and the overlay hold.

// Rung is one step of the ladder, and the number is the strike that asks for it.
type Rung int

const (
	// RungHold is "no platform action": the hit is flagged and held, nothing more.
	RungHold Rung = iota
	RungDelete
	RungTimeout
	RungBan
	maxRung = RungBan
)

// Rungs lists the platform actions in the order the ladder climbs them.
var rungActions = [...]Action{RungDelete: ActionDelete, RungTimeout: ActionTimeout, RungBan: ActionBan}

// ParseRung reads a ceiling as stored: hold, delete, timeout or ban.
func ParseRung(s string) (Rung, bool) {
	switch s {
	case "hold":
		return RungHold, true
	case "delete":
		return RungDelete, true
	case "timeout":
		return RungTimeout, true
	case "ban":
		return RungBan, true
	}
	return RungHold, false
}

// Action is the platform action a rung performs, or "" for RungHold.
func (r Rung) Action() Action {
	if r <= RungHold || r > maxRung {
		return ""
	}
	return rungActions[r]
}

// DefaultLadderTimeoutSeconds and DefaultStrikeWindow are the ladder's
// defaults: DefaultTimeoutSeconds (rules.go) already argues for ten minutes.
const (
	DefaultLadderTimeoutSeconds = 600
	DefaultStrikeWindow         = 10 * time.Minute
	// MaxStrikeAuthors is Strikes' hard ceiling, History's reasoning: idle
	// eviction alone is no bound while a raid keeps every author recent.
	MaxStrikeAuthors = 20000
)

// defaultCeilings is spec section 2.3's table, by policy key (policyKey).
// Impersonation and self-harm stay at hold: the first might be a fan, the
// second is a person in crisis and never automatic.
var defaultCeilings = map[string]Rung{
	string(CategoryHateSpeech):    RungBan,
	string(CategoryScam):          RungBan,
	string(CategoryDoxxing):       RungTimeout,
	string(CategoryFlood):         RungTimeout,
	string(CategoryImpersonation): RungHold,
	string(CategorySelfHarm):      RungHold,
	// Not in the spec's table: a raid is many accounts saying the same thing,
	// so the first rung (delete the line) is the proportionate one, and a human
	// raising it is a deliberate act.
	string(CategoryRaid): RungDelete,
	// Profanity ships off; when an operator turns it on its ceiling is delete
	// (spec 3.6).
	policyProfanity: RungDelete,
}

const policyProfanity = "profanity"

// CeilingKeys is the closed set of keys a stored ceiling may be given for.
// self_harm is in the table but not here: it cannot be raised.
func CeilingKeys() []string {
	return []string{
		string(CategoryHateSpeech), string(CategoryScam), string(CategoryDoxxing),
		string(CategoryFlood), string(CategoryImpersonation), string(CategoryRaid), policyProfanity,
	}
}

// policyKey names the ceiling a finding is subject to. Profanity arrives as
// CategoryOther, so its group names it.
func policyKey(f Finding) string {
	if len(f.Detector) > len(policyProfanity) && f.Detector[:len(policyProfanity)+1] == policyProfanity+"/" {
		return policyProfanity
	}
	return string(f.Category)
}

// Ladder is the engine's strike policy.
type Ladder struct {
	// Strikes counts hits. Owned by the caller and created once at startup, so
	// a settings save in the middle of a raid does not forget every strike.
	Strikes *Strikes
	// Window is how far back a strike counts.
	Window time.Duration
	// TimeoutSeconds is the timeout rung's duration.
	TimeoutSeconds int
	// Ceilings overrides the default ceilings, keyed by CeilingKeys.
	Ceilings map[string]Rung
}

func (l *Ladder) window() time.Duration {
	if l.Window <= 0 {
		return DefaultStrikeWindow
	}
	return l.Window
}

func (l *Ladder) timeoutSeconds() int {
	// Never zero: a zero timeout is a permanent ban on some platforms (rules.go).
	if l.TimeoutSeconds <= 0 {
		return DefaultLadderTimeoutSeconds
	}
	return l.TimeoutSeconds
}

// ceilingOf is the highest rung a finding may climb to.
func (l *Ladder) ceilingOf(f Finding) Rung {
	key := policyKey(f)
	// Contextual hate is the ambiguous tier: it is flag-only by design (D4), so
	// no ceiling lifts it onto the ladder.
	if f.Detector == pack.GroupHateContextual || key == string(CategorySelfHarm) {
		return RungHold
	}
	if r, ok := l.Ceilings[key]; ok {
		return r
	}
	return defaultCeilings[key]
}

// armedRungs is the set of rungs the matrix lets the pack perform on p, as bits.
func armedRungs(m Matrix, caps Capabilities, p db.Platform) uint8 {
	var mask uint8
	for r := RungDelete; r <= maxRung; r++ {
		if m.Allows(caps, Key{Platform: p, Action: r.Action(), Checker: CheckerProtection}) {
			mask |= 1 << uint(r)
		}
	}
	return mask
}

// WithLadder attaches the strike ladder and returns the engine. A nil ladder or
// one without Strikes leaves the engine as it was. Like WithProtection it is
// called before the engine is shared.
func (e *Engine) WithLadder(l *Ladder) *Engine {
	if e != nil && l != nil && l.Strikes != nil {
		e.ladder = l
		e.syncArming()
	}
	return e
}

// syncArming tells Strikes which rungs are armed per platform, which starts a
// new epoch on any platform where that set changed.
func (e *Engine) syncArming() {
	if e.ladder == nil {
		return
	}
	for _, p := range Platforms {
		e.ladder.Strikes.Arm(p, armedRungs(e.matrix, e.caps, p))
	}
}

// climb resolves the strike to ONE platform action for the message: the most
// severe rung any pack finding in it is entitled to, given its ceiling and the
// rungs armed on this platform. The Hub performs every entry of Verdict.Act, so
// raising each finding would turn one message into several actions, a lesser
// one issued after a greater (a timeout landing on a ban). The winning finding
// carries the action; the others stay the flags they were. A finding no rung is
// armed for stays a flag.
//
// Only a finding that HOLDS is on the ladder. The pack ships the flood
// detectors that are culture on Twitch (emote walls, shouting) and near
// duplicates flag-only: a detector not even trusted to hide a line from the
// overlay must not be able to remove it from the platform. An operator who
// turns holdDetectors on for one has opted that detector in. A hold the
// overlay does not honour (the engine or platform switched off) does not matter
// here: Hold is the pack's verdict on the detector, not the overlay's state.
//
// The ladder also yields to the other checkers. Their findings keep the action
// the operator gave them, and decide puts every armed one in Act, so a rung no
// more severe than one already armed there (a history timeout, a rule's
// delete) adds nothing but a second action. It stays a flag. A rung MORE severe
// than theirs does win, and then both reach the Hub, worst first: the ban, then
// the history timeout, the same shape that rules and history already had.
func (e *Engine) climb(p db.Platform, fs []Finding, strike int) []Finding {
	if strike > int(maxRung) {
		strike = int(maxRung)
	}
	floor := -1 // actionRank of the worst armed action another checker already asks for
	for _, f := range fs {
		if f.Checker == CheckerProtection {
			continue
		}
		if r := actionRank(f.Action); r > floor && e.matrix.Allows(e.caps, Key{Platform: p, Action: f.Action, Checker: f.Checker}) {
			floor = r
		}
	}
	best, bestRung := -1, RungHold
	for i := range fs {
		f := &fs[i]
		if f.Checker != CheckerProtection || !f.Hold {
			continue
		}
		want := Rung(strike)
		if c := e.ladder.ceilingOf(*f); c < want {
			want = c
		}
		for r := want; r > RungHold; r-- {
			if e.matrix.Allows(e.caps, Key{Platform: p, Action: r.Action(), Checker: CheckerProtection}) {
				if r > bestRung && actionRank(r.Action()) > floor {
					best, bestRung = i, r
				}
				break
			}
		}
	}
	if best >= 0 {
		fs[best].Action = bestRung.Action()
		if bestRung == RungTimeout {
			fs[best].TimeoutSeconds = e.ladder.timeoutSeconds()
		}
	}
	return e.keepDelete(p, fs)
}

// keepDelete restores the message removal a sanction would otherwise cost.
//
// The ladder gives the USER the highest rung; a timeout or ban is a sanction on
// the author and does not remove the message on every platform. A finding that
// is entitled to a delete (its ceiling reaches the delete rung, and the
// operator armed delete for the pack on this platform) must therefore still
// delete the line when what is about to be performed is a timeout or a ban and
// nothing already deletes it. The delete is a separate finding, not a rung
// swap, so the user-level sanction is unchanged. Worst first, as every verdict
// is ordered: the sanction, then the delete.
func (e *Engine) keepDelete(p db.Platform, fs []Finding) []Finding {
	if !e.matrix.Allows(e.caps, Key{Platform: p, Action: ActionDelete, Checker: CheckerProtection}) {
		return fs
	}
	var src *Finding
	sanction := false
	for i := range fs {
		f := &fs[i]
		if !e.matrix.Allows(e.caps, Key{Platform: p, Action: f.Action, Checker: f.Checker}) {
			continue
		}
		switch f.Action {
		case ActionDelete:
			return fs // already removed
		case ActionTimeout, ActionBan:
			sanction = true
		}
	}
	if !sanction {
		return fs
	}
	for i := range fs {
		f := &fs[i]
		if f.Checker == CheckerProtection && f.Hold && e.ladder.ceilingOf(*f) >= RungDelete {
			src = f
			break
		}
	}
	if src == nil {
		return fs
	}
	d := *src
	d.Action, d.TimeoutSeconds = ActionDelete, 0
	return append(fs, d)
}

type strikeHit struct {
	at    time.Time
	epoch uint64
}

type strikeLog struct {
	hits []strikeHit // at most maxRung entries: only 3 strikes are ever distinguished
	last time.Time
}

// Strikes counts pack hits per platform and author.
//
// Every hit counts, whether or not anything was performed for it: counting only
// performed actions leaves a platform with only Ban armed forever at strike 1,
// which resolves to a flag, which is never counted. What stops an old count
// from becoming a ban the moment a rung is armed is the EPOCH: each platform's
// epoch is bumped whenever its armed rung set changes, and a strike recorded
// under an older epoch no longer counts. Arming affects only what happens
// afterwards.
type Strikes struct {
	mu      sync.Mutex
	authors map[string]*strikeLog
	epoch   map[db.Platform]uint64
	armed   map[db.Platform]uint8
	max     int
	sweepAt time.Time
}

// NewStrikes returns an empty, bounded counter.
func NewStrikes() *Strikes {
	return &Strikes{
		authors: map[string]*strikeLog{},
		epoch:   map[db.Platform]uint64{},
		armed:   map[db.Platform]uint8{},
		max:     MaxStrikeAuthors,
	}
}

// Arm records the set of rungs armed on p, and starts a new epoch if it differs
// from the last one recorded. Every settings save calls it for every platform;
// an unchanged set is a no-op, which is what lets a save mid-raid keep counts.
func (s *Strikes) Arm(p db.Platform, mask uint8) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.armed[p] != mask {
		s.armed[p] = mask
		s.epoch[p]++
	}
}

// Record counts one strike against the author, and returns how many strikes in
// the same epoch and inside the window they now have, this one included. At
// most 3 (a strike beyond the third asks for nothing more).
func (s *Strikes) Record(p db.Platform, authorID string, at time.Time, window time.Duration) int {
	if s == nil || authorID == "" {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(at, window)
	key := authorKey(p, authorID)
	log := s.authors[key]
	if log == nil {
		log = &strikeLog{}
		s.authors[key] = log
	}
	cur := s.epoch[p]
	kept := log.hits[:0]
	for _, h := range log.hits {
		if h.epoch == cur && at.Sub(h.at) < window {
			kept = append(kept, h)
		}
	}
	kept = append(kept, strikeHit{at: at, epoch: cur})
	if len(kept) > int(maxRung) {
		kept = kept[len(kept)-int(maxRung):]
	}
	log.hits, log.last = kept, at
	s.capLocked()
	return len(kept)
}

// sweepLocked drops authors whose last strike is outside the window: they count
// for nothing, so idle eviction loses no information. Amortised, like History.
func (s *Strikes) sweepLocked(now time.Time, window time.Duration) {
	if now.Before(s.sweepAt) {
		return
	}
	s.sweepAt = now.Add(sweepInterval)
	for k, l := range s.authors {
		if now.Sub(l.last) >= window {
			delete(s.authors, k)
		}
	}
}

// capLocked enforces the author ceiling by dropping the least recent, to a low
// water mark so a raid does not pay for a scan on every message.
func (s *Strikes) capLocked() {
	if s.max <= 0 || len(s.authors) <= s.max {
		return
	}
	type aged struct {
		key  string
		last time.Time
	}
	all := make([]aged, 0, len(s.authors))
	for k, l := range s.authors {
		all = append(all, aged{k, l.last})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].last.Before(all[j].last) })
	for i := 0; i < len(all)-pack.LowWater(s.max); i++ {
		delete(s.authors, all[i].key)
	}
}

// Tracked reports how many authors are held.
func (s *Strikes) Tracked() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.authors)
}

// Epoch reports a platform's arming epoch, for tests and diagnostics.
func (s *Strikes) Epoch(p db.Platform) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.epoch[p]
}

// Persistent is the state that must outlive a settings save. ApplyAutomod
// rebuilds the engine and a fresh History on every save; without this a save in
// the middle of a raid would wipe every strike, the raid window and the learned
// moderator names. Created once at startup and passed in, like Budget.
type Persistent struct {
	Strikes *Strikes
	Raid    *pack.Raid
	NearDup *pack.NearDup
	Names   *pack.NameSet
	// Gate is the hard-case gate's per-author table, verdict cache and raid
	// brake (gate.go): a save in the middle of a raid must not refill them.
	Gate *ModelGate
}

// NewPersistent returns the long-lived state with its default bounds.
func NewPersistent() *Persistent {
	return &Persistent{
		Strikes: NewStrikes(),
		Raid:    pack.NewRaid(pack.DefaultRaidConfig()),
		NearDup: pack.NewNearDup(pack.DefaultNearDupConfig()),
		Names:   pack.NewNameSet(nil),
		Gate:    NewModelGate(GateConfig{}),
	}
}
