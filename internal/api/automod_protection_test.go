package api

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/automod/pack"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/db/dbtest"
)

// setProtectionState stores the protection state directly, the way an upgrade
// or an earlier answer would have left it.
func setProtectionState(t *testing.T, store *db.DB, state string) {
	t.Helper()
	s, err := store.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	s.Automod.Protection.State = state
	if err := store.PutSettings(s); err != nil {
		t.Fatal(err)
	}
}

func severeTerm(t *testing.T) string {
	t.Helper()
	pk, err := pack.Default()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range pk.Entries {
		if e.Category == pack.CategoryHate && e.Tier == pack.TierSevere && !strings.Contains(e.Term, " ") {
			return e.Term
		}
	}
	t.Fatal("no severe term")
	return ""
}

func TestTheStoredPackNameAndVersionAreTheShippedOnes(t *testing.T) {
	pk, err := pack.Default()
	if err != nil {
		t.Fatal(err)
	}
	if db.DefaultProtectionPack != pk.ID || db.DefaultProtectionPackVersion != pk.Version {
		t.Fatalf("db defaults %s v%d drifted from the embedded pack %s v%d",
			db.DefaultProtectionPack, db.DefaultProtectionPackVersion, pk.ID, pk.Version)
	}
	if automod.DefaultLadderTimeoutSeconds != db.DefaultLadderSeconds || automod.DefaultStrikeWindow != time.Duration(db.DefaultLadderSeconds)*time.Second {
		t.Fatal("the ladder defaults in db and automod drifted")
	}
}

func TestSettingsOfferTheCardOnlyWhileUndecided(t *testing.T) {
	h, store, sign := sourceServer(t)
	get := func() (state string, offer bool) {
		var doc struct {
			Automod struct {
				Protection struct {
					State string `json:"state"`
					Offer bool   `json:"offer"`
				} `json:"protection"`
			} `json:"automod"`
		}
		decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &doc)
		return doc.Automod.Protection.State, doc.Automod.Protection.Offer
	}
	if st, offer := get(); st != "on" || offer {
		t.Fatalf("a fresh install: state %q offer %v, want on and no offer", st, offer)
	}
	setProtectionState(t, store, "")
	if st, offer := get(); st != "" || !offer {
		t.Fatalf("an undecided install: state %q offer %v, want an offer", st, offer)
	}
	setProtectionState(t, store, "off")
	if st, offer := get(); st != "off" || offer {
		t.Fatalf("a declined install: state %q offer %v, want no offer", st, offer)
	}
}

func TestASentOfferIsIgnoredAndNeverStored(t *testing.T) {
	h, store, sign := sourceServer(t)
	var doc map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &doc)
	doc["automod"].(map[string]any)["protection"].(map[string]any)["offer"] = true
	send(t, h, sign, http.MethodPut, "/api/v1/settings", doc, http.StatusOK)
	s, err := store.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.Automod.Protection.Offer {
		t.Fatal("a client-sent offer was stored")
	}
}

func TestAnUndecidedInstallStaysUndecidedThroughAnUnrelatedSave(t *testing.T) {
	h, store, sign := sourceServer(t)
	setProtectionState(t, store, "")
	var doc map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &doc)
	doc["automod"].(map[string]any)["enabled"] = true
	send(t, h, sign, http.MethodPut, "/api/v1/settings", doc, http.StatusOK)
	if s, _ := store.GetSettings(); s.Automod.Protection.State != "" {
		t.Fatalf("a save the operator made elsewhere answered the offer: %q", s.Automod.Protection.State)
	}
}

func TestProtectionBlockIsStrictAndVocabularyChecked(t *testing.T) {
	h, _, sign := sourceServer(t)
	try := func(edit func(p map[string]any)) []byte {
		var doc map[string]any
		decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &doc)
		edit(doc["automod"].(map[string]any)["protection"].(map[string]any))
		return send(t, h, sign, http.MethodPut, "/api/v1/settings", doc, http.StatusBadRequest)
	}
	cases := map[string]func(p map[string]any){
		"a typo":        func(p map[string]any) { p["stat"] = "on" },
		"unknown state": func(p map[string]any) { p["state"] = "maybe" },
		"unknown category": func(p map[string]any) {
			p["categories"] = map[string]any{"astrology": map[string]any{"enabled": false}}
		},
		"unknown detector": func(p map[string]any) {
			p["categories"] = map[string]any{"doxxing": map[string]any{"detectors": map[string]any{"zip": false}}}
		},
		"unknown ceiling key":   func(p map[string]any) { p["ceilings"] = map[string]any{"astrology": "ban"} },
		"unknown ceiling value": func(p map[string]any) { p["ceilings"] = map[string]any{"scam": "exile"} },
		"raised self_harm":      func(p map[string]any) { p["ceilings"] = map[string]any{"self_harm": "ban"} },
		"unknown hold key":      func(p map[string]any) { p["holdDetectors"] = map[string]any{"nothing": true} },
		"short term": func(p map[string]any) {
			p["addTerms"] = []map[string]any{{"term": "a", "category": "hate_speech", "tier": "severe"}}
		},
		"bad tier": func(p map[string]any) {
			p["addTerms"] = []map[string]any{{"term": "abcd", "category": "hate_speech", "tier": "mild"}}
		},
		"bad term category": func(p map[string]any) {
			p["addTerms"] = []map[string]any{{"term": "abcd", "category": "scam", "tier": "severe"}}
		},
		"zero timeout": func(p map[string]any) { p["ladderTimeoutSeconds"] = 0 },
		"unknown pack": func(p map[string]any) { p["pack"] = "fr" },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) { try(edit) })
	}
}

func TestAValidProtectionDiffIsStoredAndReplaces(t *testing.T) {
	h, store, sign := sourceServer(t)
	save := func(edit func(p map[string]any)) {
		var doc map[string]any
		decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &doc)
		edit(doc["automod"].(map[string]any)["protection"].(map[string]any))
		send(t, h, sign, http.MethodPut, "/api/v1/settings", doc, http.StatusOK)
	}
	save(func(p map[string]any) {
		p["categories"] = map[string]any{"doxxing": map[string]any{"enabled": false}, "scam": map[string]any{"detectors": map[string]any{"bio": false}}}
		p["ceilings"] = map[string]any{"scam": "timeout"}
		p["holdDetectors"] = map[string]any{"caps": true, "doxxing/phone": false}
		p["allowDomains"] = []string{"sponsor.example"}
	})
	s, _ := store.GetSettings()
	if len(s.Automod.Protection.Categories) != 2 || s.Automod.Protection.Ceilings["scam"] != "timeout" {
		t.Fatalf("diff not stored: %+v", s.Automod.Protection)
	}
	// Unticking the last category and clearing the rest must stick.
	save(func(p map[string]any) {
		p["categories"] = map[string]any{"scam": map[string]any{"enabled": true}}
		p["ceilings"] = nil
		p["holdDetectors"] = map[string]any{}
		p["allowDomains"] = []string{}
	})
	s, _ = store.GetSettings()
	pr := s.Automod.Protection
	if len(pr.Categories) != 1 || pr.Ceilings != nil || len(pr.HoldDetectors) != 0 || len(pr.AllowDomains) != 0 {
		t.Fatalf("a save merged into the stored diff instead of replacing it: %+v", pr)
	}
}

func TestProtectionCellsArmOnlyWhileProtectionIsOn(t *testing.T) {
	h, store, sign := sourceServer(t)
	arm := func(want int) []byte {
		var doc map[string]any
		decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &doc)
		am := doc["automod"].(map[string]any)
		on, _ := am["on"].(map[string]any)
		if on == nil {
			on = map[string]any{}
			am["on"] = on
		}
		on["twitch/ban/protection"] = true
		return send(t, h, sign, http.MethodPut, "/api/v1/settings", doc, want)
	}
	// A fresh install has the pack on: the cell may be armed.
	arm(http.StatusOK)
	// And on a declined one it may not.
	var doc map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &doc)
	delete(doc["automod"].(map[string]any)["on"].(map[string]any), "twitch/ban/protection")
	send(t, h, sign, http.MethodPut, "/api/v1/settings", doc, http.StatusOK)
	setProtectionState(t, store, "off")
	body := arm(http.StatusBadRequest)
	if !strings.Contains(string(body), "recommended protection is not on") {
		t.Errorf("refusal: %s", body)
	}
}

func TestCheckerConfiguredForProtection(t *testing.T) {
	for state, want := range map[string]bool{"": false, "off": false, "on": true} {
		var a db.AutomodSettings
		a.Protection.State = state
		got, why := checkerConfigured(a, automod.CheckerProtection)
		if got != want || (!got && why != "recommended protection is not on") {
			t.Errorf("state %q: %v %q", state, got, why)
		}
	}
}

func TestPackConfigIsTheOperatorsDiffOverThePackDefaults(t *testing.T) {
	off := false
	on := true
	p := db.AutomodProtection{
		State: db.ProtectionOn,
		Categories: map[string]db.ProtectionCategory{
			"doxxing":     {Enabled: &off},
			"scam":        {Detectors: map[string]bool{"bio": false}},
			"flood":       {Detectors: map[string]bool{"near_duplicate": false}},
			"profanity":   {Enabled: &on},
			"hate_speech": {Enabled: &on},
		},
		HoldDetectors: map[string]bool{"caps": true, "phone": false, "doxxing/email": false, "repeats": true},
		AllowDomains:  []string{"sponsor.example"},
		AddTerms:      []db.ProtectionTerm{{Term: "zzyzx", Category: "hate_speech", Tier: "severe"}},
		AllowTerms:    []string{"qqxq"},
	}
	cfg, hold := packConfigFromSettings(p)
	for _, g := range []string{pack.GroupPhone, pack.GroupEmail, pack.GroupIPv4, pack.GroupIPv6, pack.GroupAddress} {
		if v, ok := cfg.Detectors[g]; !ok || v {
			t.Errorf("doxxing disabled but %s = %v/%v", g, v, ok)
		}
	}
	if v, ok := cfg.Detectors[pack.GroupBio]; !ok || v {
		t.Errorf("scam/bio not off: %v/%v", v, ok)
	}
	if _, ok := cfg.Detectors[pack.GroupGiveaway]; ok {
		t.Error("an untouched scam detector was written into the config")
	}
	if v, ok := cfg.Detectors[pack.GroupNearDuplicate]; !ok || v {
		t.Errorf("near-duplicate not off: %v/%v", v, ok)
	}
	if !cfg.Categories[pack.CategoryProfanity] || !cfg.Categories[pack.CategoryHate] {
		t.Errorf("term categories: %v", cfg.Categories)
	}
	if _, ok := cfg.Detectors[pack.GroupProfanityMild]; ok {
		t.Error("a profanity group was switched by detector instead of by category")
	}
	if hold[pack.GroupCaps] != true || hold[pack.GroupPhone] != false || hold[pack.GroupEmail] != false || hold["repeats"] != true {
		t.Errorf("hold overrides: %v", hold)
	}
	if len(cfg.AddTerms) != 1 || cfg.AllowTerms[0] != "qqxq" || cfg.AllowDomains[0] != "sponsor.example" {
		t.Errorf("term diff: %+v %+v %+v", cfg.AddTerms, cfg.AllowTerms, cfg.AllowDomains)
	}
}

// What the operator's diff does to real messages, end to end through the
// checker the engine would build.
func TestTheBuiltPackFollowsTheOperatorsDiff(t *testing.T) {
	hl := automod.DefaultHistoryLimits()
	build := func(p db.AutomodProtection) *automod.Protection {
		p.State = db.ProtectionOn
		a := db.AutomodSettings{Protection: p}
		prot, err := protectionFromSettings(a, hl, nil)
		if err != nil || prot == nil {
			t.Fatalf("build: %v %v", prot, err)
		}
		return prot
	}
	hits := func(p *automod.Protection, text string) map[string]bool {
		out := map[string]bool{}
		for _, h := range p.Checker().Inspect(pack.Message{Platform: "twitch", Text: text}) {
			out[h.Group] = true
		}
		return out
	}
	const mail = "mail me at someone@gmail.com"
	if !hits(build(db.AutomodProtection{}), mail)[pack.GroupEmail] {
		t.Fatal("the default pack missed an email")
	}
	off := false
	if hits(build(db.AutomodProtection{Categories: map[string]db.ProtectionCategory{"doxxing": {Enabled: &off}}}), mail)[pack.GroupEmail] {
		t.Fatal("a disabled category still fired")
	}
	if hits(build(db.AutomodProtection{Categories: map[string]db.ProtectionCategory{"doxxing": {Detectors: map[string]bool{"email": false}}}}), mail)[pack.GroupEmail] {
		t.Fatal("a disabled detector still fired")
	}
	term := severeTerm(t)
	if !hits(build(db.AutomodProtection{}), "you "+term)[pack.GroupHateSevere] {
		t.Fatal("the default pack missed a severe term")
	}
	if hits(build(db.AutomodProtection{AllowTerms: []string{term}}), "you "+term)[pack.GroupHateSevere] {
		t.Fatal("an allowed term still fired")
	}
	if !hits(build(db.AutomodProtection{AddTerms: []db.ProtectionTerm{{Term: "zzyzxq", Category: "hate_speech", Tier: "severe"}}}), "a zzyzxq b")[pack.GroupHateSevere] {
		t.Fatal("an added term did not fire")
	}
	// An undecided or declined install builds no pack at all.
	for _, st := range []string{"", "off"} {
		p, err := protectionFromSettings(db.AutomodSettings{Protection: db.AutomodProtection{State: st}}, hl, nil)
		if p != nil || err != nil {
			t.Fatalf("state %q built a pack: %v %v", st, p, err)
		}
	}
}

func TestVocabularyProblemsAreSortedAndNamed(t *testing.T) {
	probs := protectionVocabProblems(db.AutomodProtection{
		Pack: "fr", PackVersion: -1,
		Categories: map[string]db.ProtectionCategory{"zeta": {}, "alpha": {}, "doxxing": {Detectors: map[string]bool{"zip": true}}},
		Ceilings:   map[string]string{"zeta": "ban"}, HoldDetectors: map[string]bool{"nope": true},
		AddTerms: []db.ProtectionTerm{{Term: "abcd", Category: "scam"}},
	})
	if len(probs) != 8 {
		t.Fatalf("%d problems: %v", len(probs), probs)
	}
	if !strings.Contains(probs[2], "alpha") || !strings.Contains(probs[4], "zeta") {
		t.Fatalf("category problems are not in a stable order: %v", probs)
	}
	for _, g := range pack.Groups() {
		if _, ok := resolveHoldKey(g); !ok {
			t.Errorf("group %s cannot be named in holdDetectors", g)
		}
		if groupCategory(g) == "" || groupShort(g) == "" {
			t.Errorf("group %s has no category or short name", g)
		}
	}
}

// A settings save rebuilds the engine. Strikes, the learned moderator names and
// the raid window are the server's, so a save in the middle of a raid keeps
// every one of them.
func TestApplyAutomodAttachesThePackOnlyWhenOnAndKeepsStrikesAcrossSaves(t *testing.T) {
	store := dbtest.OpenCheap(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	doc := db.DefaultSettings()
	if err := store.PutSettings(doc); err != nil {
		t.Fatal(err)
	}
	a := doc.Automod
	// Delete, timeout and ban all armed for the pack on Twitch.
	a.On = map[string]bool{
		"twitch/delete/protection": true, "twitch/timeout/protection": true, "twitch/ban/protection": true,
	}
	ps := automod.NewPersistent()
	pb, sb := automod.NewBudget(), automod.NewBudget()
	slur := "you " + severeTerm(t)

	// Undecided: no pack, no flag, nothing held.
	hub := newTestHub(t)
	und := a
	und.Protection.State = ""
	ApplyAutomod(AutomodApply{Hub: hub, Store: store, Log: log, Settings: und, Budget: pb, SecondaryBudget: sb, Persist: ps})
	if v := hub.Moderator().CheckFast(db.PlatformTwitch, automod.Subject{AuthorID: "u1", AuthorName: "v", Text: slur}); v.Flagged() || v.Hold {
		t.Fatalf("an undecided install ran the pack: %+v", v)
	}
	if ps.Strikes.Tracked() != 0 {
		t.Fatal("an undecided install counted a strike")
	}

	// On: the first hit is a delete (strike 1).
	ApplyAutomod(AutomodApply{Hub: hub, Store: store, Log: log, Settings: a, Budget: pb, SecondaryBudget: sb, Persist: ps})
	check := func() automod.Finding {
		v := hub.Moderator().CheckFast(db.PlatformTwitch, automod.Subject{AuthorID: "u1", AuthorName: "v", Text: slur})
		if !v.Hold || len(v.Findings) == 0 {
			t.Fatalf("the pack did not flag and hold: %+v", v)
		}
		return v.Findings[0]
	}
	if f := check(); f.Action != automod.ActionDelete {
		t.Fatalf("strike 1: %q", f.Action)
	}
	// A settings save in the middle: a new engine, the same strikes.
	ApplyAutomod(AutomodApply{Hub: hub, Store: store, Log: log, Settings: a, Budget: pb, SecondaryBudget: sb, Persist: ps})
	if f := check(); f.Action != automod.ActionTimeout || f.TimeoutSeconds != 600 {
		t.Fatalf("strike 2 after a save: %q/%d, want timeout/600", f.Action, f.TimeoutSeconds)
	}
	// Arming a rung is a different matter: it starts the count again.
	b := a
	b.On = map[string]bool{"twitch/ban/protection": true}
	ApplyAutomod(AutomodApply{Hub: hub, Store: store, Log: log, Settings: b, Budget: pb, SecondaryBudget: sb, Persist: ps})
	if f := check(); f.Action != automod.ActionFlag {
		t.Fatalf("first hit after the armed set changed: %q, want flag", f.Action)
	}
	// The learned names outlive the rebuild and the operator's list replaces
	// only its own half.
	ps.Names.Learn("zorblax_main")
	c := b
	c.Protection.Impersonation.Protect = []string{"qwertyuiop"}
	ApplyAutomod(AutomodApply{Hub: hub, Store: store, Log: log, Settings: c, Budget: pb, SecondaryBudget: sb, Persist: ps})
	if got := ps.Names.Len(); got < 2 {
		t.Fatalf("learned names were dropped by a save: %d held", got)
	}
}

func TestLadderFromSettingsReadsCeilingsAndNeverRaisesSelfHarm(t *testing.T) {
	st := automod.NewStrikes()
	l := ladderFromSettings(db.AutomodProtection{
		StrikeWindowSeconds: 120, LadderTimeoutSeconds: 90,
		Ceilings: map[string]string{"scam": "timeout", "doxxing": "hold", "self_harm": "ban", "flood": "kick"},
	}, st)
	if l.Strikes != st || l.Window != 2*time.Minute || l.TimeoutSeconds != 90 {
		t.Fatalf("ladder: %+v", l)
	}
	if l.Ceilings["scam"] != automod.RungTimeout || l.Ceilings["doxxing"] != automod.RungHold {
		t.Fatalf("ceilings: %v", l.Ceilings)
	}
	if _, ok := l.Ceilings["self_harm"]; ok {
		t.Fatal("a stored self_harm ceiling reached the ladder")
	}
	if _, ok := l.Ceilings["flood"]; ok {
		t.Fatal("an unparseable ceiling reached the ladder")
	}
}

// A block already stored with a name this build does not know (written by a
// newer build, or by a pack that later dropped the detector) must not make
// every unrelated save fail; only editing the block is checked.
func TestAnUnknownStoredProtectionNameDoesNotBlockUnrelatedSaves(t *testing.T) {
	h, store, sign := sourceServer(t)
	s, err := store.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	s.Automod.Protection.Categories = map[string]db.ProtectionCategory{"astrology": {}}
	if err := store.PutSettings(s); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &doc)
	doc["automod"].(map[string]any)["enabled"] = true // the echoed offer and block are unchanged
	send(t, h, sign, http.MethodPut, "/api/v1/settings", doc, http.StatusOK)
	// Editing the block is checked again.
	doc["automod"].(map[string]any)["protection"].(map[string]any)["ceilings"] = map[string]any{"astrology": "ban"}
	send(t, h, sign, http.MethodPut, "/api/v1/settings", doc, http.StatusBadRequest)
}

func TestSameProtectionIgnoresTheDerivedOffer(t *testing.T) {
	a := db.AutomodProtection{State: "on", Offer: true}
	b := db.AutomodProtection{State: "on"}
	if !sameProtection(a, b) {
		t.Fatal("the derived offer made an unchanged block look edited")
	}
	b.AllowDomains = []string{"x.example"}
	if sameProtection(a, b) {
		t.Fatal("an edit was not noticed")
	}
	if err := protectionChangeProblems(a, a); err != nil {
		t.Fatal(err)
	}
}

// EffectiveAutomodMatrix is what the acceptance driver's gate reads: it must be
// the engine's conversion (unknown keys dropped), not the raw stored map.
func TestEffectiveAutomodMatrixIsTheEnginesConversion(t *testing.T) {
	a := db.DefaultSettings().Automod
	if n := len(EffectiveAutomodMatrix(a).On); n != 0 {
		t.Fatalf("a fresh install has %d cells armed, want 0", n)
	}
	a.On = map[string]bool{
		"twitch/delete/protection":  true,
		"twitch/delete/not_a_check": true, // dropped by matrixFromSettings
		"twitch/ban/protection":     false,
	}
	got := EffectiveAutomodMatrix(a).On
	if len(got) != 1 || !got["twitch/delete/protection"] {
		t.Fatalf("effective On = %v, want only twitch/delete/protection", got)
	}
}
