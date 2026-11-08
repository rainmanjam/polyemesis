package automod

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/automod/pack"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// fakeChecker is a ModelChecker that counts its calls.
type fakeChecker struct {
	calls atomic.Int32
	out   []Finding
	err   error
}

func (f *fakeChecker) Check(context.Context, string) ([]Finding, error) {
	f.calls.Add(1)
	return f.out, f.err
}
func (f *fakeChecker) Stats() ModelStats { return ModelStats{} }
func (f *fakeChecker) Enabled() bool     { return true }

const hardLine = "i will find you and kill you" // address + violence: a threat signal

func hardEngine(t *testing.T, mc ModelChecker, g *ModelGate) *Engine {
	t.Helper()
	e := New(DefaultMatrix(), PlatformCaps{}, nil, nil, mc)
	return e.WithModelGate(ScopeHard, g)
}

func TestParseModelScopeDefaultsToAll(t *testing.T) {
	for in, want := range map[string]ModelScope{"": ScopeAll, "all": ScopeAll, "hard": ScopeHard, "HARD": ScopeAll, "x": ScopeAll} {
		if got := ParseModelScope(in); got != want {
			t.Errorf("ParseModelScope(%q) = %q, want %q", in, got, want)
		}
	}
}

// Scope "all", no gate, and a nil engine all ask as before (or, for nil, never).
func TestWantsModelAllAsksAboutEverything(t *testing.T) {
	e := New(DefaultMatrix(), PlatformCaps{}, nil, nil, &fakeChecker{})
	if !e.WantsModel(db.PlatformTwitch, Subject{AuthorID: "a", Text: "hello"}, Verdict{}) {
		t.Fatal("scope all declined a message")
	}
	if e.Scope() != ScopeAll {
		t.Fatalf("scope = %q", e.Scope())
	}
	var nilE *Engine
	if nilE.WantsModel(db.PlatformTwitch, Subject{}, Verdict{}) || nilE.Scope() != ScopeAll || nilE.gateStats() != nil {
		t.Fatal("a nil engine asked, or reported a scope")
	}
	if e.gateStats() != nil {
		t.Fatal("scope all reports gate counters")
	}
}

func TestWantsModelHardNeedsASignalAndAnUnsettledMessage(t *testing.T) {
	g := NewModelGate(GateConfig{})
	e := hardEngine(t, &fakeChecker{}, g)
	p := db.PlatformTwitch

	if e.WantsModel(p, Subject{AuthorID: "a1", Text: "gg that was close"}, Verdict{}) {
		t.Fatal("an ordinary line was a hard case")
	}
	if !e.WantsModel(p, Subject{AuthorID: "a2", Text: hardLine}, Verdict{}) {
		t.Fatal("a threat line was not asked about")
	}
	for _, f := range []Finding{
		{Checker: CheckerProtection, Category: CategoryDoxxing},
		{Checker: CheckerProtection, Category: CategoryScam},
		{Checker: CheckerProtection, Category: CategoryHateSpeech, Detector: pack.GroupHateSevere},
	} {
		if e.WantsModel(p, Subject{AuthorID: "a3", Text: hardLine}, Verdict{Findings: []Finding{f}}) {
			t.Errorf("%s was asked about although the fast pass settled it", f.Category)
		}
	}
	st := e.gateStats()
	if st == nil || st.Scope != ScopeHard || st.Asked != 1 || st.NoSignal != 1 || st.Settled != 3 {
		t.Fatalf("gate stats = %+v", st)
	}
}

// A contextual-tier hate hit is itself a signal, with nothing else in the text.
func TestWantsModelContextualHateIsASignal(t *testing.T) {
	e := hardEngine(t, &fakeChecker{}, NewModelGate(GateConfig{}))
	v := Verdict{Findings: []Finding{{Checker: CheckerProtection, Category: CategoryHateSpeech, Detector: pack.GroupHateContextual}}}
	if !e.WantsModel(db.PlatformTwitch, Subject{AuthorID: "a", Text: "hello"}, v) {
		t.Fatal("a contextual hate hit was not worth a question")
	}
}

func TestPerAuthorCapAllowsOneCallPerTwentySeconds(t *testing.T) {
	g := NewModelGate(GateConfig{})
	now := time.Now()
	g.now = func() time.Time { return now }
	e := hardEngine(t, &fakeChecker{}, g)
	ask := func(author, text string) bool {
		return e.WantsModel(db.PlatformTwitch, Subject{AuthorID: author, Text: text}, Verdict{})
	}
	if !ask("a", hardLine) {
		t.Fatal("first message declined")
	}
	if ask("a", "i will hurt you tonight") {
		t.Fatal("a second call inside 20 s was allowed")
	}
	if !ask("b", "i will hurt you tonight") {
		t.Fatal("another author was capped")
	}
	now = now.Add(21 * time.Second)
	if !ask("a", "i will hurt you tonight") {
		t.Fatal("the cap did not lift after 20 s")
	}
	if got := e.gateStats().Capped; got != 1 {
		t.Fatalf("capped = %d, want 1", got)
	}
}

func TestAuthorTableIsBounded(t *testing.T) {
	g := NewModelGate(GateConfig{MaxAuthors: 3, RaidMax: 1000})
	now := time.Now()
	g.now = func() time.Time { return now }
	e := hardEngine(t, &fakeChecker{}, g)
	for i, a := range []string{"a", "b", "c"} {
		if !e.WantsModel(db.PlatformTwitch, Subject{AuthorID: a, Text: hardLine + string(rune('0'+i))}, Verdict{}) {
			t.Fatalf("author %s declined", a)
		}
	}
	// Full of authors who all spoke in the last 20 s: decline, do not grow.
	if e.WantsModel(db.PlatformTwitch, Subject{AuthorID: "d", Text: "i will find you tonight"}, Verdict{}) {
		t.Fatal("a fourth author was admitted past the bound")
	}
	if len(g.authors) > 3 {
		t.Fatalf("table grew to %d", len(g.authors))
	}
	now = now.Add(time.Minute)
	if !e.WantsModel(db.PlatformTwitch, Subject{AuthorID: "d", Text: "i will find you tonight"}, Verdict{}) {
		t.Fatal("expired entries were not swept")
	}
}

func TestRaidBrakeStopsAskingAndLifts(t *testing.T) {
	g := NewModelGate(GateConfig{RaidMax: 5})
	now := time.Now()
	g.now = func() time.Time { return now }
	e := hardEngine(t, &fakeChecker{}, g)
	asked := 0
	for i := 0; i < 20; i++ {
		// Distinct authors, so only the brake can decline.
		if e.WantsModel(db.PlatformTwitch, Subject{AuthorID: string(rune('A' + i)), Text: hardLine}, Verdict{}) {
			asked++
		}
	}
	if asked != 5 {
		t.Fatalf("asked %d before the brake, want 5", asked)
	}
	st := e.gateStats()
	if st.BrakeUntil.IsZero() || st.Braked == 0 {
		t.Fatalf("brake not reported: %+v", st)
	}
	now = now.Add(61 * time.Second)
	if !e.WantsModel(db.PlatformTwitch, Subject{AuthorID: "z", Text: hardLine}, Verdict{}) {
		t.Fatal("the brake did not lift after 60 s")
	}
	if !e.gateStats().BrakeUntil.IsZero() {
		t.Fatal("the lifted brake is still reported")
	}
}

// A copypasta raid costs one call: the answer is cached by NFKC text (case and spacing folded), and the
// cached answer needs neither the author allowance nor a second request.
func TestCopypastaCostsOneCallAndErrorsAreNotCached(t *testing.T) {
	fc := &fakeChecker{out: []Finding{{Checker: CheckerModel, Action: ActionFlag, Category: CategoryThreat, Hold: true}}}
	chain := NewChain(fc, nil)
	g := NewModelGate(GateConfig{RaidMax: 1000})
	e := hardEngine(t, chain, g)
	p := db.PlatformTwitch
	ctx := context.Background()

	v1, err := e.CheckModel(ctx, p, "I  WILL find you and kill you")
	if err != nil || !v1.Hold {
		t.Fatalf("first answer: %+v %v", v1, err)
	}
	// Same NFKC text modulo case and spacing, a different author.
	if !e.WantsModel(p, Subject{AuthorID: "x", Text: "i will find you and kill you"}, Verdict{}) {
		t.Fatal("the cached text was declined")
	}
	v2, err := e.CheckModel(ctx, p, "i will find you and kill you")
	if err != nil || !v2.Hold || v2.DecidedBy != SlotPrimary {
		t.Fatalf("cached answer: %+v %v", v2, err)
	}
	if n := fc.calls.Load(); n != 1 {
		t.Fatalf("model calls = %d, want 1", n)
	}
	if e.gateStats().Cached != 1 {
		t.Fatalf("cached = %d", e.gateStats().Cached)
	}
	// The cache holds a copy: editing a returned slice must not corrupt it.
	v2.Findings[0].Hold = false
	if v3, _ := e.CheckModel(ctx, p, "i will find you and kill you"); !v3.Hold {
		t.Fatal("a caller's edit reached the cache")
	}

	// A failed call is never remembered.
	bad := &fakeChecker{err: errors.New("503")}
	e2 := hardEngine(t, NewChain(bad, nil), NewModelGate(GateConfig{}))
	for i := 0; i < 2; i++ {
		if _, err := e2.CheckModel(ctx, p, hardLine); err == nil {
			t.Fatal("no error")
		}
	}
	if bad.calls.Load() != 2 {
		t.Fatalf("an error was served from the cache: %d calls", bad.calls.Load())
	}
}

func TestCacheExpiresAndEvictsLeastRecent(t *testing.T) {
	g := NewModelGate(GateConfig{CacheSize: 2, CacheTTL: time.Minute})
	now := time.Now()
	g.now = func() time.Time { return now }
	g.store(1, nil, SlotPrimary)
	g.store(2, nil, SlotPrimary)
	g.store(1, nil, SlotPrimary) // refresh 1
	g.store(3, nil, SlotPrimary) // evicts 2
	if _, _, ok := g.lookup(2); ok {
		t.Fatal("the least recently used entry survived")
	}
	if _, _, ok := g.lookup(1); !ok {
		t.Fatal("a recently used entry was evicted")
	}
	now = now.Add(2 * time.Minute)
	if _, _, ok := g.lookup(1); ok {
		t.Fatal("an expired entry was served")
	}
}

// The gate state outlives the engine: a save mid-raid builds a new engine on
// the same gate and the brake and the author table are still in force.
func TestGateSurvivesAnEngineRebuild(t *testing.T) {
	g := NewModelGate(GateConfig{})
	ask := func(e *Engine, author string) bool {
		return e.WantsModel(db.PlatformTwitch, Subject{AuthorID: author, Text: hardLine}, Verdict{})
	}
	if !ask(hardEngine(t, &fakeChecker{}, g), "a") {
		t.Fatal("first declined")
	}
	if ask(hardEngine(t, &fakeChecker{}, g), "a") {
		t.Fatal("a rebuilt engine forgot the author")
	}
}

func TestWithModelGateFallsBackToAllWithoutAGate(t *testing.T) {
	e := New(DefaultMatrix(), PlatformCaps{}, nil, nil, &fakeChecker{}).WithModelGate(ScopeHard, nil)
	if e.Scope() != ScopeAll {
		t.Fatalf("scope = %q, want all when there is no gate to hold the limits", e.Scope())
	}
	var nilE *Engine
	if nilE.WithModelGate(ScopeHard, nil) != nil || nilE.WithHoldOnModelError(true) != nil || nilE.HoldOnModelError() {
		t.Fatal("nil engine misbehaved")
	}
	if !New(DefaultMatrix(), PlatformCaps{}, nil, nil, nil).WithHoldOnModelError(true).HoldOnModelError() {
		t.Fatal("hold on error not set")
	}
	var nilG *ModelGate
	if nilG.Stats() != (GateStats{}) {
		t.Fatal("nil gate stats")
	}
}

func TestModelStatsCarriesTheGateOnlyUnderHard(t *testing.T) {
	g := NewModelGate(GateConfig{})
	if hardEngine(t, &fakeChecker{}, g).ModelStats().Gate == nil {
		t.Fatal("hard scope hides its counters")
	}
	if New(DefaultMatrix(), PlatformCaps{}, nil, nil, &fakeChecker{}).ModelStats().Gate != nil {
		t.Fatal("scope all reports a gate")
	}
}

// ONE VIEWER CANNOT SWITCH THE MODEL OFF FOR THE ROOM. A message the per-author
// cap declines is not a pass, so an author pasting threat-shaped lines (the same
// one over and over, or different ones) never trips the raid brake, and another
// author is still asked about.
func TestOneAuthorSpammingCannotTripTheRaidBrake(t *testing.T) {
	g := NewModelGate(GateConfig{RaidMax: 5})
	now := time.Now()
	g.now = func() time.Time { return now }
	e := hardEngine(t, &fakeChecker{}, g)
	asked := 0
	for i := 0; i < 40; i++ {
		text := hardLine
		if i%2 == 1 {
			text = "i will hurt you tonight"
		}
		if e.WantsModel(db.PlatformTwitch, Subject{AuthorID: "pest", Text: text}, Verdict{}) {
			asked++
		}
		now = now.Add(100 * time.Millisecond)
	}
	if asked != 1 {
		t.Fatalf("the author was asked about %d times, want once", asked)
	}
	st := e.gateStats()
	if !st.BrakeUntil.IsZero() || st.Braked != 0 {
		t.Fatalf("one author tripped the brake: %+v", st)
	}
	if !e.WantsModel(db.PlatformTwitch, Subject{AuthorID: "bystander", Text: hardLine}, Verdict{}) {
		t.Fatal("another author was declined after one author spammed")
	}
}

// A save rebuilds the engine and its checkers; a cached finding carries the
// action the OLD config gave it, so it must not outlive the save. The author
// table and the brake do (TestGateSurvivesAnEngineRebuild).
func TestSavingSettingsDropsCachedVerdicts(t *testing.T) {
	g := NewModelGate(GateConfig{RaidMax: 1000})
	p := db.PlatformTwitch
	ctx := context.Background()
	text := "i will find you and kill you"

	armed := &fakeChecker{out: []Finding{{Checker: CheckerModel, Action: ActionTimeout, TimeoutSeconds: 600, Category: CategoryHarassment, Hold: true}}}
	m := DefaultMatrix()
	m.Enabled = true
	e1 := New(m, PlatformCaps{}, nil, nil, NewChain(armed, nil)).WithModelGate(ScopeHard, g)
	v, err := e1.CheckModel(ctx, p, text)
	if err != nil || len(v.Findings) != 1 || v.Findings[0].Action != ActionTimeout || !v.Hold {
		t.Fatalf("first answer: %+v %v", v, err)
	}
	// A repeat on the same engine is a hit.
	if _, _ = e1.CheckModel(ctx, p, text); armed.calls.Load() != 1 {
		t.Fatalf("setup: the cache did not serve a repeat (%d calls)", armed.calls.Load())
	}

	// The operator sets harassment back to flag and turns protection off; the
	// rebuilt checker now answers with a flag and no hold.
	relaxed := &fakeChecker{out: []Finding{{Checker: CheckerModel, Action: ActionFlag, Category: CategoryHarassment}}}
	e2 := New(m, PlatformCaps{}, nil, nil, NewChain(relaxed, nil)).WithModelGate(ScopeHard, g)
	v, err = e2.CheckModel(ctx, p, text)
	if err != nil {
		t.Fatal(err)
	}
	if relaxed.calls.Load() != 1 {
		t.Fatalf("the new config was not asked: a stale verdict was served (%d calls)", relaxed.calls.Load())
	}
	if len(v.Findings) != 1 || v.Findings[0].Action != ActionFlag || v.Hold {
		t.Fatalf("stale verdict after a save: %+v", v)
	}
}

func TestClearVerdictsKeepsTheAuthorTableAndBrake(t *testing.T) {
	g := NewModelGate(GateConfig{})
	if !g.admit("twitch:a") {
		t.Fatal("first admit declined")
	}
	g.store(1, nil, SlotPrimary)
	g.ClearVerdicts()
	if _, _, ok := g.lookup(1); ok {
		t.Fatal("a verdict survived ClearVerdicts")
	}
	if g.admit("twitch:a") {
		t.Fatal("ClearVerdicts refilled the author allowance")
	}
	(*ModelGate)(nil).ClearVerdicts() // a nil gate is a no-op
}

// Under scope "all" every message is still asked about, and the gate counts
// what "hard" would have skipped: the saving the card offers it with (D11).
func TestScopeAllCountsWhatHardWouldHaveSkipped(t *testing.T) {
	g := NewModelGate(GateConfig{})
	e := New(DefaultMatrix(), PlatformCaps{}, nil, nil, &fakeChecker{}).WithModelGate(ScopeAll, g)
	if e.gateStats() != nil {
		t.Fatal("an engine that has seen nothing reports a dry run")
	}
	for _, text := range []string{"hello chat", "gg", "nice play", hardLine} {
		if !e.WantsModel(db.PlatformTwitch, Subject{AuthorID: "a", Text: text}, Verdict{}) {
			t.Fatalf("scope all declined %q", text)
		}
	}
	st := e.gateStats()
	if st == nil || st.Scope != ScopeAll || st.Considered != 4 || st.WouldSkip != 3 {
		t.Fatalf("dry run = %+v, want 4 considered and 3 skipped", st)
	}
	// Counting is not limiting: no author allowance was spent, no brake tripped.
	if st.Asked != 0 || st.Capped != 0 || st.Braked != 0 {
		t.Fatalf("the dry run limited something: %+v", st)
	}
	for i := 0; i < 100; i++ {
		if !e.WantsModel(db.PlatformTwitch, Subject{AuthorID: "a", Text: hardLine}, Verdict{}) {
			t.Fatal("scope all declined a repeat from one author")
		}
	}
	// A settled message is a skip too.
	settled := Verdict{Findings: []Finding{{Checker: CheckerProtection, Category: CategoryDoxxing}}}
	e.WantsModel(db.PlatformTwitch, Subject{AuthorID: "b", Text: hardLine}, settled)
	if st := e.gateStats(); st.WouldSkip != 4 {
		t.Fatalf("a settled message was not counted: %+v", st)
	}
	// And under "hard" the real counters take over, with no dry run in them.
	h := hardEngine(t, &fakeChecker{}, g)
	h.WantsModel(db.PlatformTwitch, Subject{AuthorID: "c", Text: "hello"}, Verdict{})
	if st := h.gateStats(); st.Considered != 0 || st.WouldSkip != 0 || st.NoSignal != 1 {
		t.Fatalf("hard stats = %+v", st)
	}
}

// A question still in flight when the settings were saved comes back after the
// cache was cleared. It belongs to the OLD config and must not be remembered.
func TestAnInFlightAnswerFromBeforeASaveIsNotCached(t *testing.T) {
	g := NewModelGate(GateConfig{RaidMax: 1000})
	p := db.PlatformTwitch
	ctx := context.Background()
	text := "i will find you and kill you"

	gate := make(chan struct{})
	entered := make(chan struct{})
	slow := &blockingChecker{gate: gate, entered: entered,
		out: []Finding{{Checker: CheckerModel, Action: ActionTimeout, TimeoutSeconds: 600, Category: CategoryHarassment, Hold: true}}}
	m := DefaultMatrix()
	m.Enabled = true
	e1 := New(m, PlatformCaps{}, nil, nil, NewChain(slow, nil)).WithModelGate(ScopeHard, g)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = e1.CheckModel(ctx, p, text)
	}()
	<-entered // e1 is mid-call

	// The save: a new engine on the same gate, with the relaxed config.
	relaxed := &fakeChecker{out: []Finding{{Checker: CheckerModel, Action: ActionFlag, Category: CategoryHarassment}}}
	e2 := New(m, PlatformCaps{}, nil, nil, NewChain(relaxed, nil)).WithModelGate(ScopeHard, g)

	close(gate) // the old answer lands now
	<-done

	v, err := e2.CheckModel(ctx, p, text)
	if err != nil {
		t.Fatal(err)
	}
	if relaxed.calls.Load() != 1 {
		t.Fatalf("the new config was not asked: the old engine's answer was cached (%d calls)", relaxed.calls.Load())
	}
	if len(v.Findings) != 1 || v.Findings[0].Action != ActionFlag || v.Hold {
		t.Fatalf("stale verdict: %+v", v)
	}
}

// blockingChecker answers only once gate is closed, and says when it was asked.
type blockingChecker struct {
	gate    chan struct{}
	entered chan struct{}
	out     []Finding
}

func (b *blockingChecker) Check(context.Context, string) ([]Finding, error) {
	close(b.entered)
	<-b.gate
	return append([]Finding(nil), b.out...), nil
}
func (b *blockingChecker) Stats() ModelStats { return ModelStats{} }
func (b *blockingChecker) Enabled() bool     { return true }

// Two texts that fold to one pack form but differ in NFKC must not share a
// cached verdict, or whichever was judged first decides the other for ten
// minutes. Case and spacing differences still do share one.
func TestVerdictCacheIsKeyedOnNFKCNotThePackForm(t *testing.T) {
	benign, variant := "i will kill you", "i will k1ll you"
	a, b := pack.NewForms(benign), pack.NewForms(variant)
	if a.Pack != b.Pack || a.NFKC == b.NFKC {
		t.Fatalf("setup: need the same pack form and different NFKC, got %q/%q and %q/%q", a.Pack, b.Pack, a.NFKC, b.NFKC)
	}
	fc := &fakeChecker{}
	e := hardEngine(t, fc, NewModelGate(GateConfig{}))
	ctx := context.Background()
	if _, err := e.CheckModel(ctx, db.PlatformTwitch, benign); err != nil {
		t.Fatal(err)
	}
	if _, err := e.CheckModel(ctx, db.PlatformTwitch, variant); err != nil {
		t.Fatal(err)
	}
	if got := fc.calls.Load(); got != 2 {
		t.Fatalf("the model was asked %d times, want 2: the variant was answered from the benign line's cached verdict", got)
	}
	// Case and whitespace are the only folds that cannot change a meaning.
	for _, same := range []string{"I  WILL kill you", "  i will kill\tyou "} {
		if _, err := e.CheckModel(ctx, db.PlatformTwitch, same); err != nil {
			t.Fatal(err)
		}
	}
	if got := fc.calls.Load(); got != 2 {
		t.Fatalf("a case/whitespace variant cost a call: %d", got)
	}
}
