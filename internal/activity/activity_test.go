package activity

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 11, 7, 20, 0, 0, 0, time.UTC)

func now() time.Time { return fixedNow }

// TestAllKindsIsTheWholeConstBlock parses the declaration rather than trusting
// whoever last added a Kind to have remembered the list, as
// events.TestAllTypesIsTheWholeConstBlock does for bus types.
func TestAllKindsIsTheWholeConstBlock(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "activity.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{}
	for _, d := range f.Decls {
		gen, ok := d.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			if id, ok := vs.Type.(*ast.Ident); ok && id.Name == "Kind" {
				for _, n := range vs.Names {
					declared[n.Name] = true
				}
			}
		}
	}
	if len(declared) == 0 {
		t.Fatal("the AST walk found no `X Kind = \"y\"` constants; this guard is looking at the wrong thing")
	}
	listed := map[string]bool{}
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "AllKinds" && fn.Recv == nil {
			ast.Inspect(fn, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && declared[id.Name] {
					listed[id.Name] = true
				}
				return true
			})
		}
	}
	for name := range declared {
		if !listed[name] {
			t.Errorf("activity.%s is declared but not returned by AllKinds()", name)
		}
	}
	if len(listed) != len(declared) {
		t.Errorf("AllKinds lists %d kinds, the const block declares %d", len(listed), len(declared))
	}
}

func TestKindWireValuesAreFrozen(t *testing.T) {
	want := "follow subscribe resub gift tip raid redemption hypetrain poll prediction stream shoutout moderation reaction"
	var got []string
	for _, k := range AllKinds() {
		got = append(got, string(k))
	}
	if strings.Join(got, " ") != want {
		t.Fatalf("kind wire values changed:\n got %s\nwant %s", strings.Join(got, " "), want)
	}
}

func TestEverySampleIsValidAndNormalisesToItself(t *testing.T) {
	for _, k := range AllKinds() {
		for _, platform := range []string{"", "twitch", "kick", "youtube"} {
			e := Sample(k, platform, Overrides{}, fixedNow).Normalise(now)
			if err := e.Validate(); err != nil {
				t.Errorf("%s sample for %q does not validate: %v", k, platform, err)
			}
			if !e.Test || e.Source.Type != TestSourceType {
				t.Errorf("%s sample is not marked as a test: %+v", k, e.Source)
			}
			if !strings.HasPrefix(e.ID, "act_") {
				t.Errorf("%s sample id %q", k, e.ID)
			}
		}
	}
}

func TestSampleOfAnUnknownKindIsRefused(t *testing.T) {
	e := Sample("nope", "", Overrides{}, fixedNow).Normalise(now)
	if e.Validate() == nil {
		t.Fatal("an unknown kind validated")
	}
}

func TestSampleIDsAreDistinct(t *testing.T) {
	a := Sample(KindFollow, "twitch", Overrides{}, fixedNow)
	b := Sample(KindFollow, "twitch", Overrides{}, fixedNow)
	if a.Key() == b.Key() {
		t.Fatal("two test alerts share a dedupe key")
	}
}

func TestOverridesApplyOnlyWhereTheyMean(t *testing.T) {
	amt := &Amount{Currency: "JPY", Minor: 1000}
	tip := Sample(KindTip, "", Overrides{Name: "Mara", Message: "hi", Amount: amt, Count: 9, Viewers: 3, Months: 4, Tier: "2"}, fixedNow).Normalise(now)
	if tip.Actor.Name != "Mara" || tip.Message != "hi" || tip.Amount.Currency != "JPY" || tip.Tier != "2" {
		t.Errorf("tip overrides not applied: %+v", tip)
	}
	if tip.Count != 0 || tip.Viewers != 0 || tip.Months != 0 {
		t.Errorf("a tip took gift/raid/resub overrides: %+v", tip)
	}
	if tip.Actor.ID != "test-viewer" {
		t.Errorf("an override changed the actor's id: %q", tip.Actor.ID)
	}
	if err := tip.Validate(); err != nil {
		t.Error(err)
	}

	raid := Sample(KindRaid, "", Overrides{Name: "Big Channel", Viewers: 900}, fixedNow).Normalise(now)
	if raid.Other.Name != "Big Channel" || raid.Viewers != 900 {
		t.Errorf("raid overrides not applied: %+v", raid)
	}
	gift := Sample(KindGift, "", Overrides{Count: 2}, fixedNow).Normalise(now)
	if gift.Count != 2 || len(gift.Recipients) != 2 {
		t.Errorf("gift override: count %d recipients %d", gift.Count, len(gift.Recipients))
	}
	re := Sample(KindResub, "", Overrides{Months: 30}, fixedNow).Normalise(now)
	if re.Months != 30 {
		t.Errorf("resub months %d", re.Months)
	}
	rx := Sample(KindReaction, "", Overrides{Count: 99}, fixedNow).Normalise(now)
	if rx.Count != 99 {
		t.Errorf("reaction count %d", rx.Count)
	}
}

func TestValidateKindByFieldMatrix(t *testing.T) {
	base := func(k Kind) Event { return Sample(k, "twitch", Overrides{}, fixedNow).Normalise(now) }
	cases := []struct {
		name   string
		mutate func(*Event)
		kind   Kind
	}{
		{"follow without an actor", func(e *Event) { e.Actor = nil }, KindFollow},
		{"actor with no name", func(e *Event) { e.Actor = &Actor{ID: "1"} }, KindFollow},
		{"resub without months", func(e *Event) { e.Months = 0 }, KindResub},
		{"gift without count", func(e *Event) { e.Count = 0; e.Recipients = nil }, KindGift},
		{"tip without amount", func(e *Event) { e.Amount = nil }, KindTip},
		{"tip with money and unit", func(e *Event) { e.Amount = &Amount{Currency: "USD", Minor: 1, Unit: "bits", Units: 1} }, KindTip},
		{"tip with neither", func(e *Event) { e.Amount = &Amount{} }, KindTip},
		{"tip with a bad currency", func(e *Event) { e.Amount = &Amount{Currency: "DOLLARS", Minor: 1} }, KindTip},
		{"tip with a negative amount", func(e *Event) { e.Amount = &Amount{Currency: "USD", Minor: -1} }, KindTip},
		{"tip with units and no unit name", func(e *Event) { e.Amount = &Amount{Units: 5} }, KindTip},
		{"raid without other", func(e *Event) { e.Other = nil }, KindRaid},
		{"raid without direction", func(e *Event) { e.Direction = "" }, KindRaid},
		{"raid with a made-up direction", func(e *Event) { e.Direction = "sideways" }, KindRaid},
		{"redemption without a title", func(e *Event) { e.Title = "" }, KindRedemption},
		{"hypetrain with a made-up phase", func(e *Event) { e.Phase = "lock" }, KindHypeTrain},
		{"poll with lock", func(e *Event) { e.Phase = "lock" }, KindPoll},
		{"poll without a title", func(e *Event) { e.Title = "" }, KindPoll},
		{"prediction without a title", func(e *Event) { e.Title = "" }, KindPrediction},
		{"stream with begin", func(e *Event) { e.Phase = "begin" }, KindStream},
		{"moderation with a made-up action", func(e *Event) { e.Action = "smite" }, KindModeration},
		{"moderation without a target", func(e *Event) { e.Other = nil }, KindModeration},
		{"reaction with no count", func(e *Event) { e.Count = 0 }, KindReaction},
		{"no platform", func(e *Event) { e.Platform = "" }, KindFollow},
		{"no source type", func(e *Event) { e.Source.Type = "" }, KindFollow},
		{"no source id", func(e *Event) { e.Source.ID = "" }, KindFollow},
		{"unknown kind", func(e *Event) { e.Kind = "sneeze" }, KindFollow},
	}
	for _, c := range cases {
		e := base(c.kind)
		if err := e.Validate(); err != nil {
			t.Fatalf("%s: the unmutated sample is invalid: %v", c.name, err)
		}
		c.mutate(&e)
		if err := e.Validate(); err == nil {
			t.Errorf("%s: validated", c.name)
		}
	}

	// Phases and actions that ARE allowed.
	for phase, kind := range map[string]Kind{"lock": KindPrediction, "end": KindPoll, "offline": KindStream, "begin": KindHypeTrain} {
		e := base(kind)
		e.Phase = phase
		if err := e.Validate(); err != nil {
			t.Errorf("%s phase %q refused: %v", kind, phase, err)
		}
	}
	for _, act := range []string{"ban", "timeout", "unban", "automod_hold"} {
		e := base(KindModeration)
		e.Action = act
		if err := e.Validate(); err != nil {
			t.Errorf("moderation action %q refused: %v", act, err)
		}
	}
	// An anonymous gifter has no name and is fine.
	g := base(KindGift)
	g.Actor = &Actor{Anonymous: true}
	if err := g.Validate(); err != nil {
		t.Errorf("anonymous gifter refused: %v", err)
	}
}

func TestNormaliseFillsTrimsAndClamps(t *testing.T) {
	long := strings.Repeat("é", 900)
	e := Event{
		Kind: " TIP ", Platform: " Twitch ", Account: " 123 ",
		Actor:   &Actor{Login: " mara ", Name: ""},
		Amount:  &Amount{Currency: "usd", Minor: 12345, Exponent: 2},
		Message: "  " + long + "  ",
		Source:  Source{Type: " channel.cheer ", ID: " abc "},
		Count:   -4, Months: 99999,
	}.Normalise(now)
	if e.Kind != KindTip || e.Platform != "twitch" || e.Account != "123" {
		t.Errorf("identity fields: %q %q %q", e.Kind, e.Platform, e.Account)
	}
	if !strings.HasPrefix(e.ID, "act_") || !e.At.Equal(fixedNow) {
		t.Errorf("id/at not filled: %q %v", e.ID, e.At)
	}
	if e.Actor.Name != "mara" {
		t.Errorf("a login-only actor should be named by its login, got %q", e.Actor.Name)
	}
	if got := len([]rune(e.Message)); got != MaxMessageRunes {
		t.Errorf("message %d runes, want %d", got, MaxMessageRunes)
	}
	if e.Amount.Currency != "USD" || e.Amount.Display != "123.45 USD" {
		t.Errorf("amount: %+v", e.Amount)
	}
	if e.Count != 0 || e.Months != 10_000 {
		t.Errorf("clamps: count %d months %d", e.Count, e.Months)
	}
	if e.Source.Type != "channel.cheer" || e.Source.ID != "abc" {
		t.Errorf("source: %+v", e.Source)
	}
}

func TestNormaliseKeepsThePlatformsOwnDisplay(t *testing.T) {
	e := Event{Kind: KindTip, Platform: "youtube", Amount: &Amount{Currency: "USD", Minor: 500, Exponent: 2, Display: "$5.00"}}.Normalise(nil)
	if e.Amount.Display != "$5.00" {
		t.Errorf("display overwritten: %q", e.Amount.Display)
	}
	if e.At.IsZero() {
		t.Error("nil clock should fall back to time.Now")
	}
}

func TestAmountDisplay(t *testing.T) {
	cases := []struct {
		a    Amount
		want string
	}{
		{Amount{Currency: "USD", Minor: 500, Exponent: 2}, "5.00 USD"},
		{Amount{Currency: "USD", Minor: 5, Exponent: 2}, "0.05 USD"},
		{Amount{Currency: "JPY", Minor: 500, Exponent: 0}, "500 JPY"},
		{Amount{Currency: "BHD", Minor: 1234, Exponent: 3}, "1.234 BHD"},
		{Amount{Currency: "USD", Minor: -250, Exponent: 2}, "-2.50 USD"},
		{Amount{Unit: "bits", Units: 100}, "100 bits"},
	}
	for _, c := range cases {
		if got := c.a.build(); got != c.want {
			t.Errorf("%+v = %q, want %q", c.a, got, c.want)
		}
	}
}

func TestNormaliseCapsRecipientsOptionsAndBreakdown(t *testing.T) {
	var rs []Recipient
	for i := 0; i < 40; i++ {
		rs = append(rs, Recipient{Name: "r" + string(rune('a'+i%26))})
	}
	rs = append(rs, Recipient{Name: "   "})
	var opts []ProgressOption
	for i := 0; i < 20; i++ {
		opts = append(opts, ProgressOption{Label: "o"})
	}
	bd := map[string]int{}
	for i := 0; i < 30; i++ {
		bd[string(rune('a'+i%26))+string(rune('a'+i/26))] = i
	}
	e := Event{Kind: KindGift, Platform: "kick", Count: 1, Recipients: rs, Progress: &Progress{Options: opts}, Breakdown: bd}.Normalise(now)
	if len(e.Recipients) != MaxRecipients || e.Count != MaxRecipients {
		t.Errorf("recipients %d count %d", len(e.Recipients), e.Count)
	}
	if len(e.Progress.Options) != MaxOptions {
		t.Errorf("options %d", len(e.Progress.Options))
	}
	if len(e.Breakdown) != MaxBreakdown {
		t.Errorf("breakdown %d", len(e.Breakdown))
	}
	empty := Event{Kind: KindGift, Platform: "kick", Recipients: []Recipient{{Name: " "}}, Progress: &Progress{}}.Normalise(now)
	if empty.Recipients != nil || empty.Progress.Options != nil {
		t.Errorf("empty slices should be nil: %+v", empty)
	}
}

func TestNormaliseDoesNotShareSlicesWithTheInput(t *testing.T) {
	in := Event{Kind: KindGift, Platform: "kick", Count: 1, Actor: &Actor{Name: "a"},
		Recipients: []Recipient{{Name: "x"}}, Breakdown: map[string]int{"a": 1}}
	out := in.Normalise(now)
	out.Recipients[0].Name = "changed"
	out.Actor.Name = "changed"
	out.Breakdown["a"] = 9
	if in.Recipients[0].Name != "x" || in.Actor.Name != "a" || in.Breakdown["a"] != 1 {
		t.Fatal("Normalise aliased its input")
	}
}

func TestKeyIsStableAndScoped(t *testing.T) {
	a := Event{Platform: "twitch", Account: "1", Source: Source{Type: "channel.cheer", ID: "x"}}
	b := a
	b.Message = "different text, same fact"
	if a.Key() != b.Key() {
		t.Error("the same fact has two keys")
	}
	for name, mut := range map[string]func(*Event){
		"platform": func(e *Event) { e.Platform = "kick" },
		"account":  func(e *Event) { e.Account = "2" },
		"type":     func(e *Event) { e.Source.Type = "channel.subscribe" },
		"id":       func(e *Event) { e.Source.ID = "y" },
	} {
		c := a
		mut(&c)
		if c.Key() == a.Key() {
			t.Errorf("changing the %s did not change the key", name)
		}
	}
}

func TestOverlayVisibility(t *testing.T) {
	visible := map[Kind]bool{}
	for _, k := range OverlayKinds() {
		visible[k] = true
	}
	for _, k := range AllKinds() {
		e := Sample(k, "twitch", Overrides{}, fixedNow).Normalise(now)
		if got := e.OverlayVisible(); got != visible[k] {
			t.Errorf("%s: OverlayVisible %v, want %v", k, got, visible[k])
		}
	}
	for _, never := range []Kind{KindModeration, KindStream, KindPoll, KindPrediction, KindReaction} {
		if visible[never] {
			t.Errorf("%s must never be overlay-visible", never)
		}
	}
	out := Sample(KindRaid, "twitch", Overrides{}, fixedNow)
	out.Direction = "out"
	if out.OverlayVisible() {
		t.Error("a raid out reached the overlay")
	}
}

func TestDisplayName(t *testing.T) {
	if n := Sample(KindFollow, "", Overrides{}, fixedNow).DisplayName(); n != "Test Viewer" {
		t.Errorf("follow name %q", n)
	}
	if n := Sample(KindRaid, "", Overrides{}, fixedNow).DisplayName(); n != "Friend Channel" {
		t.Errorf("raid name %q", n)
	}
	g := Sample(KindGift, "", Overrides{}, fixedNow)
	g.Actor = &Actor{Anonymous: true}
	if n := g.DisplayName(); n != "" {
		t.Errorf("anonymous name %q", n)
	}
	if n := Sample(KindHypeTrain, "", Overrides{}, fixedNow).DisplayName(); n != "" {
		t.Errorf("hypetrain name %q", n)
	}
}

func TestParseKindAndKindState(t *testing.T) {
	if k, ok := ParseKind("tip"); !ok || k != KindTip {
		t.Error("tip should parse")
	}
	if _, ok := ParseKind("Tip"); ok {
		t.Error("kinds are case-sensitive on the wire")
	}
	for _, s := range []KindState{KindOn, KindOff, KindUnavailable, KindReconnect, KindUnknown} {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	if KindState("maybe").Valid() {
		t.Error("a made-up state is valid")
	}
}

// The wire shape is a contract: this pins the field names a consumer reads.
func TestWireShape(t *testing.T) {
	e := Event{
		ID: "act_1", Kind: KindTip, Platform: "youtube", Account: "UC1", At: fixedNow,
		Actor:   &Actor{ID: "u", Name: "Mara"},
		Amount:  &Amount{Currency: "USD", Minor: 500, Exponent: 2, Display: "$5.00"},
		Message: "great stream!", Tier: "2",
		Source: Source{Type: "superChatEvent", ID: "LCC.1"},
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"act_1","kind":"tip","platform":"youtube","account":"UC1","at":"2026-11-07T20:00:00Z",` +
		`"actor":{"id":"u","name":"Mara"},"amount":{"currency":"USD","minor":500,"exponent":2,"display":"$5.00"},` +
		`"message":"great stream!","tier":"2","source":{"type":"superChatEvent","id":"LCC.1"}}`
	if string(b) != want {
		t.Errorf("wire shape changed:\n got %s\nwant %s", b, want)
	}
	var back Event
	if err := json.Unmarshal(b, &back); err != nil || back.Key() != e.Key() {
		t.Errorf("round trip: %v", err)
	}
}

func TestClipKeepsValidUTF8(t *testing.T) {
	s := strings.Repeat("日本", 100)
	got := clip(s, 7)
	if len([]rune(got)) != 7 {
		t.Errorf("clip to 7 runes gave %d", len([]rune(got)))
	}
	if clip("abc", 0) != "abc" {
		t.Error("a zero limit should not clip")
	}
	if clip("a\xffb", 10) != "ab" {
		t.Errorf("invalid utf-8 survived: %q", clip("a\xffb", 10))
	}
}

func TestAmountExponentIsRefusedWhenOutOfRangeOrWrongForTheCurrency(t *testing.T) {
	bad := []Amount{
		{Currency: "USD", Minor: 1, Exponent: 9},
		{Currency: "USD", Minor: 1, Exponent: -1},
		{Currency: "USD", Minor: 500, Exponent: 0}, // a dollar amount with the exponent omitted
		{Currency: "JPY", Minor: 500, Exponent: 2}, // a hundredfold error
		{Currency: "BHD", Minor: 1, Exponent: 2},
		{Unit: "bits", Units: 1, Exponent: 2},
	}
	for _, a := range bad {
		e := Event{Kind: KindTip, Platform: "twitch", Actor: &Actor{Name: "a"}, Amount: &a, Source: Source{Type: "x", ID: "1"}}.Normalise(now)
		if e.Amount.Exponent != a.Exponent {
			t.Errorf("Normalise changed exponent %d to %d instead of leaving it for Validate", a.Exponent, e.Amount.Exponent)
		}
		if e.Validate() == nil {
			t.Errorf("%+v validated", a)
		}
	}
	for _, a := range []Amount{
		{Currency: "JPY", Minor: 500}, {Currency: "USD", Minor: 500, Exponent: 2}, {Currency: "BHD", Minor: 1234, Exponent: 3},
		{Currency: "XYZ", Minor: 5, Exponent: 4}, {Unit: "kicks", Units: 3},
	} {
		e := Event{Kind: KindTip, Platform: "twitch", Actor: &Actor{Name: "a"}, Amount: &a, Source: Source{Type: "x", ID: "1"}}.Normalise(now)
		if err := e.Validate(); err != nil {
			t.Errorf("%+v refused: %v", a, err)
		}
	}
	if x, ok := ExponentFor("jpy"); !ok || x != 0 {
		t.Errorf("ExponentFor(jpy) = %d %v", x, ok)
	}
	if _, ok := ExponentFor("XYZ"); ok {
		t.Error("an unlisted currency is known")
	}
}

func TestSampleOverrideAmountTakesTheCurrenciesOwnExponent(t *testing.T) {
	e := Sample(KindTip, "", Overrides{Amount: &Amount{Currency: "USD", Minor: 700}}, fixedNow).Normalise(now)
	if e.Amount.Exponent != 2 || e.Validate() != nil {
		t.Errorf("%+v %v", e.Amount, e.Validate())
	}
}

func TestAnOutgoingShoutoutIsNeverOverlayVisible(t *testing.T) {
	e := Sample(KindShoutout, "twitch", Overrides{}, fixedNow)
	if !e.OverlayVisible() {
		t.Fatal("a shoutout received should be drawn")
	}
	e.Direction = "out"
	if e.OverlayVisible() {
		t.Error("a shoutout the streamer gave was announced as one received")
	}
}
