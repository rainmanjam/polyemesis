package automod

import (
	"fmt"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/automod/pack"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// The recommended-protection pack as a checker.
//
// internal/automod/pack holds every detector and returns pack-local Hits; this
// file is the boundary that turns a Hit into a Finding. Nothing here decides
// what a finding may DO: a pack finding asks only to be flagged, and to be held
// off the overlay where its detector says so. The platform ladder (delete, then
// timeout, then ban) is a later change, armed per platform by the operator, and
// it resolves through the matrix like every other action.

// historyHoldDefaults is the pack's policy for the detectors the history
// checker owns (spec section 3.4). Repeats, caps and message rate flag only,
// because hype copypasta, shouting and chatty bursts are normal on stream and a
// hold hides the room's energy. Mentions and links hold: a mass-ping on an
// overlay is an attack surface.
var historyHoldDefaults = map[string]bool{
	DetectorRepeats:     false,
	DetectorCaps:        false,
	DetectorMessageRate: false,
	DetectorMentions:    true,
	DetectorLinks:       true,
}

// ProtectionConfig configures a Protection.
type ProtectionConfig struct {
	// Pack configures the detectors. Its Shape limits for mentions, links and
	// caps are overridden by History when the engine has one, so those three
	// are counted once, with the operator's own numbers.
	Pack pack.Config
	// Hold overrides the hold policy per detector. A flood detector may be named
	// by its short name ("caps") or its group ("flood/caps"), and the override
	// applies to both the history checker's detector and the pack's.
	Hold map[string]bool
	// History, when set, is the limits of the history checker the engine holds.
	History *HistoryLimits
}

// Protection is the pack checker behind the engine's protection column.
type Protection struct {
	chk   *pack.Checker
	names *pack.NameSet
	hold  map[string]bool
}

// NewProtection builds the pack checker. The Raid and NearDup trackers inside
// cfg.Pack are the caller's to create once and keep: a settings save rebuilds a
// Protection, and a raid in progress must survive it.
func NewProtection(cfg ProtectionConfig) (*Protection, error) {
	pc := cfg.Pack
	if pc.Shape == (pack.ShapeLimits{}) {
		pc.Shape = pack.DefaultShapeLimits()
	}
	if pc.Names == nil {
		pc.Names = pack.NewNameSet(nil)
	}
	if cfg.History != nil {
		// The history checker counts mentions, links and shouting with the
		// operator's limits and an operator-chosen action. Counting them again
		// here would flag every one twice.
		pc.Shape.MaxMentions, pc.Shape.MaxLinks, pc.Shape.MaxCapsRatio = 0, 0, 0
	}
	// One override map serves both checkers. A flood detector is named by its
	// short name ("caps") or its group ("flood/caps"); either sets both the
	// history checker's detector and the pack's group.
	hold := map[string]bool{}
	packHold := map[string]bool{}
	for k, v := range pc.Hold {
		packHold[k] = v
	}
	for k, v := range cfg.Hold {
		short := strings.TrimPrefix(k, "flood/")
		hold[short] = v
		if strings.Contains(k, "/") {
			packHold[k] = v
		} else {
			packHold["flood/"+k] = v
		}
	}
	pc.Hold = packHold
	chk, err := pack.New(pc)
	if err != nil {
		return nil, err
	}
	return &Protection{chk: chk, names: pc.Names, hold: hold}, nil
}

// Checker returns the underlying pack checker, for tests and the explain route.
func (p *Protection) Checker() *pack.Checker {
	if p == nil {
		return nil
	}
	return p.chk
}

// LearnProtectedName records a name seen on a broadcaster or moderator, so an
// impersonator of them is caught. Kept in memory only and bounded.
func (p *Protection) LearnProtectedName(name string) {
	if p != nil && p.names != nil {
		p.names.Learn(name)
	}
}

// historyHold reports whether a history detector's hit also holds the message.
func (p *Protection) historyHold(detector string) bool {
	if v, ok := p.hold[detector]; ok {
		return v
	}
	return historyHoldDefaults[detector]
}

// markHistory applies the pack's per-detector hold policy to the history
// checker's findings. History itself never sets Hold: on an install without the
// pack its findings are exactly what they were.
func (p *Protection) markHistory(fs []Finding) []Finding {
	if p == nil {
		return fs
	}
	for i := range fs {
		if fs[i].Detector != "" {
			fs[i].Hold = p.historyHold(fs[i].Detector)
		}
	}
	return fs
}

// categoryOf maps a pack category onto the closed Category set.
func categoryOf(c string) Category {
	switch c {
	case pack.CategoryHate:
		return CategoryHateSpeech
	case pack.CategoryDoxxing:
		return CategoryDoxxing
	case pack.CategoryScam:
		return CategoryScam
	case pack.CategoryFlood:
		return CategoryFlood
	case pack.CategoryImpersonation:
		return CategoryImpersonation
	case pack.CategoryRaid:
		return CategoryRaid
	}
	// Profanity, and anything a later pack adds: the generic sentence, which is
	// a fixed one, rather than a category invented here.
	return CategoryOther
}

func findingOf(h pack.Hit) Finding {
	reason := h.Reason
	if len(h.Authors) > 0 {
		reason += " (" + strings.Join(h.Authors, ", ") + ")"
	}
	return Finding{
		Checker:  CheckerProtection,
		Action:   ActionFlag,
		Category: categoryOf(h.Category),
		Reason:   reason,
		Hold:     h.Hold,
		Detector: h.Group,
	}
}

func messageOf(plat db.Platform, s Subject, at time.Time) pack.Message {
	return pack.Message{
		Platform: string(plat), AuthorID: s.AuthorID, AuthorName: s.AuthorName,
		Text: s.Text, EmoteCount: s.EmoteCount, At: at,
	}
}

// check runs the pack on one message and records it in the windowed detectors.
// hist is what the history checker found for the same message: a near-duplicate
// is dropped when history already reported the exact repeat.
func (p *Protection) check(plat db.Platform, s Subject, at time.Time, hist []Finding) []Finding {
	if p == nil {
		return nil
	}
	hits := p.chk.Observe(messageOf(plat, s, at))
	exact := false
	for _, f := range hist {
		exact = exact || f.Detector == DetectorRepeats
	}
	out := make([]Finding, 0, len(hits))
	for _, h := range hits {
		if exact && h.Group == pack.GroupNearDuplicate {
			continue
		}
		out = append(out, findingOf(h))
	}
	return out
}

// checkText runs only the stateless detectors, on text with no author.
func (p *Protection) checkText(plat db.Platform, text string) []Finding {
	if p == nil {
		return nil
	}
	hits := p.chk.Inspect(messageOf(plat, Subject{Text: text}, time.Time{}))
	out := make([]Finding, 0, len(hits))
	for _, h := range hits {
		out = append(out, findingOf(h))
	}
	return out
}

// Detection is one detector's result in an Explanation.
type Detection struct {
	Checker  Checker  `json:"checker"`
	Category Category `json:"category"`
	// Detector is the pack group ("doxxing/phone") or the history detector.
	Detector string `json:"detector"`
	// Tier is the term tier for hate hits.
	Tier string `json:"tier,omitempty"`
	// Form is the form of the message the evidence was found on.
	Form string `json:"form,omitempty"`
	// Reason names the detector, never the matched text.
	Reason string `json:"reason"`
	// Hold is whether the hit keeps the message off every overlay.
	Hold bool `json:"hold"`
	// Action is what the finding asks for, and Act whether the current matrix
	// would let it happen on the platform asked about.
	Action Action `json:"action"`
	Act    bool   `json:"act"`
	// Ladder is what the strike ladder would ask a platform to do at strikes 1,
	// 2 and 3, given the current arming and the category's ceiling. Present only
	// for a pack finding that holds (the only kind on the ladder) and only when
	// the engine has a ladder. Action is "flag" where nothing is armed.
	Ladder []LadderStep `json:"ladder,omitempty"`
	// Live is whether the engine running in chat would produce this finding. A
	// pack detection is not live while the pack is off for the install: it is
	// what the pack WOULD say, shown so the operator can see what turning it on
	// does.
	Live bool `json:"live"`
}

// LadderStep is one strike's outcome in a Detection's Ladder preview.
type LadderStep struct {
	Strike int    `json:"strike"`
	Action Action `json:"action"`
}

// ladderPreview resolves a pack finding's rung for strikes 1 to 3 exactly as
// climb does for a lone finding (ceiling, then the highest armed rung at or
// below it), without touching Strikes. It returns nil for a finding that is
// not on the ladder.
func (e *Engine) ladderPreview(plat db.Platform, f Finding) []LadderStep {
	if e.ladder == nil || f.Checker != CheckerProtection || !f.Hold {
		return nil
	}
	steps := make([]LadderStep, 0, int(maxRung))
	for strike := 1; strike <= int(maxRung); strike++ {
		want := Rung(strike)
		if c := e.ladder.ceilingOf(f); c < want {
			want = c
		}
		act := ActionFlag
		for r := want; r > RungHold; r-- {
			if e.matrix.Allows(e.caps, Key{Platform: plat, Action: r.Action(), Checker: CheckerProtection}) {
				act = r.Action()
				break
			}
		}
		steps = append(steps, LadderStep{Strike: strike, Action: act})
	}
	return steps
}

// Explanation is what the test box shows for one pretend message.
type Explanation struct {
	// Protection is whether the pack is on in the live engine, which is the
	// question a test box has to answer truthfully: detections it lists with
	// live=false are the pack's, shown for what turning it on would do.
	Protection bool        `json:"protection"`
	Detections []Detection `json:"detections"`
	// Held is whether the LIVE engine would hold the message off the overlay,
	// given the global and platform switches: pack detections count only while
	// the pack is on.
	Held bool `json:"held"`
	// WouldHoldIfOn is Held with the pack counted. It differs from Held only
	// while the pack is off.
	WouldHoldIfOn bool `json:"wouldHoldIfOn"`
	// Windowed lists the detectors that depend on what the author sent recently
	// and so cannot be answered for one pretend message.
	Windowed []string `json:"windowed"`
}

// Explain judges one pretend message the way CheckFast would, and records
// nothing: it never calls History.Observe, the windowed detectors or any
// strike counter, which keep per-author state, and it never touches the network.
// The text is not logged by this function or retained by it.
func (e *Engine) Explain(plat db.Platform, s Subject) Explanation {
	return e.ExplainAs(plat, s, true)
}

// ExplainAs is Explain for a caller that knows whether the pack is on in the
// live engine. The pack still runs when it is off, so the operator can see what
// it would say, but its findings are marked not live and do not count toward
// Held: the answer is what chat would do, with WouldHoldIfOn for the other case.
func (e *Engine) ExplainAs(plat db.Platform, s Subject, packOn bool) Explanation {
	ex := Explanation{Detections: []Detection{}, Windowed: []string{
		DetectorMessageRate, DetectorRepeats, pack.GroupNearDuplicate, pack.GroupRaid,
	}}
	if e == nil {
		return ex
	}
	ex.Protection = e.protection != nil && packOn
	ruleF := e.rules.Check(s.Text)
	histRaw := e.history.CheckText(s.Text)
	hits := map[string]pack.Hit{} // keyed by group, for tier and form
	var packF []Finding
	if e.protection != nil {
		for _, h := range e.protection.chk.Inspect(messageOf(plat, s, time.Time{})) {
			hits[h.Group] = h
			packF = append(packF, findingOf(h))
		}
	}
	// With the pack on, history findings carry the pack's hold policy
	// (markHistory); with it off they do not, which is what the live engine
	// reports, so the live verdict is built from the unmarked ones.
	histMarked := e.protection.markHistory(append([]Finding(nil), histRaw...))
	withPack := append(append(append([]Finding(nil), ruleF...), histMarked...), packF...)
	ex.WouldHoldIfOn = e.decide(plat, sortByConsequence(withPack)).Hold
	shown := withPack
	if packOn {
		ex.Held = ex.WouldHoldIfOn
	} else {
		shown = append(append(append([]Finding(nil), ruleF...), histRaw...), packF...)
		live := append(append([]Finding(nil), ruleF...), histRaw...)
		ex.Held = e.decide(plat, sortByConsequence(live)).Hold
	}
	for _, f := range shown {
		d := Detection{
			Checker: f.Checker, Category: f.Category, Detector: f.Detector, Reason: f.Reason,
			Hold: f.Hold, Action: f.Action,
			Act:  e.matrix.Allows(e.caps, Key{Platform: plat, Action: f.Action, Checker: f.Checker}),
			Live: packOn || f.Checker != CheckerProtection,
		}
		d.Ladder = e.ladderPreview(plat, f)
		if h, ok := hits[f.Detector]; ok && f.Checker == CheckerProtection {
			d.Tier, d.Form = h.Tier, h.Form
		}
		ex.Detections = append(ex.Detections, d)
	}
	return ex
}

// String is for logs: the counts, never the text.
func (x Explanation) String() string {
	return fmt.Sprintf("%d detections, held=%v", len(x.Detections), x.Held)
}
