package pack

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Hate terms are taken from the embedded pack at run time, never written here:
// a test file that spelled a slur would copy it into every diff and CI log.

func firstTerm(t *testing.T, category, tier string, ok func(string) bool) string {
	t.Helper()
	p, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range p.Entries {
		if e.Category == category && e.Tier == tier && !strings.Contains(e.Term, " ") && ok(e.Term) {
			return e.Term
		}
	}
	t.Fatalf("no %s/%s term in the pack", category, tier)
	return ""
}

func newChecker(t *testing.T, cfg Config) *Checker {
	t.Helper()
	if cfg.Shape == (ShapeLimits{}) {
		cfg.Shape = DefaultShapeLimits()
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func groupsOf(hs []Hit) map[string]Hit {
	m := map[string]Hit{}
	for _, h := range hs {
		m[h.Group] = h
	}
	return m
}

func TestEveryDetectorFiresOnItsOwnShape(t *testing.T) {
	c := newChecker(t, Config{
		Names:      NewNameSet([]string{"kestrel_demo"}),
		Detectors:  map[string]bool{GroupAddress: true},
		Categories: map[string]bool{CategoryProfanity: true},
	})
	sev := firstTerm(t, CategoryHate, TierSevere, func(string) bool { return true })
	ctx := firstTerm(t, CategoryHate, TierContextual, func(string) bool { return true })
	prof := firstTerm(t, CategoryProfanity, TierMild, func(s string) bool {
		return len(s) >= 5
	})
	cases := []struct {
		group string
		msg   Message
	}{
		{GroupHateSevere, Message{Text: "hello " + sev + " there"}},
		{GroupHateContextual, Message{Text: "hello " + ctx + " there"}},
		{GroupProfanityMild, Message{Text: "oh " + prof}},
		{GroupPhone, Message{Text: "call +44 20 7946 0958 now"}},
		{GroupEmail, Message{Text: "mail me at someone@example.com"}},
		{GroupIPv4, Message{Text: "his ip is 8.8.4.4"}},
		{GroupIPv6, Message{Text: "ddos 2606:4700:4700:0:0:0:0:1111"}},
		{GroupAddress, Message{Text: "he lives at 1600 Pennsylvania Avenue"}},
		{GroupGiveaway, Message{Text: "free nitro for everyone"}},
		{GroupBio, Message{Text: "check out my bio"}},
		{GroupSupport, Message{Text: "official twitch support: verify your account"}},
		{GroupShortener, Message{Text: "look https://bit.ly/abc123"}},
		{GroupLookalike, Message{Text: "see twіtch.tv/emotes"}},
		{GroupZalgo, Message{Text: "h" + strings.Repeat("̖̗̘̙", 5) + "i"}},
		{GroupChar, Message{Text: "wow " + strings.Repeat("#", 40)}},
		{GroupEmote, Message{Text: strings.TrimSpace(strings.Repeat("Kappa ", 14))}},
		{GroupMentions, Message{Text: "@a1 @b2 @c3 @d4 @e5 @f6 hi"}},
		{GroupLinks, Message{Text: "https://a.example https://b.example https://c.example https://d.example"}},
		{GroupCaps, Message{Text: "THIS IS A VERY LOUD MESSAGE"}},
		{GroupImpersonation, Message{AuthorName: "kestrel_demo_official", Text: "hi chat"}},
	}
	for _, tc := range cases {
		tc.msg.Platform = "twitch"
		got := groupsOf(c.Inspect(tc.msg))
		h, ok := got[tc.group]
		if !ok {
			t.Errorf("%s did not fire (fired %v)", tc.group, keys(got))
			continue
		}
		if h.Hold != DefaultHold(tc.group) {
			t.Errorf("%s: Hold = %v, the pack's policy is %v", tc.group, h.Hold, DefaultHold(tc.group))
		}
		if h.Reason == "" || h.Category == "" || h.Detector == "" {
			t.Errorf("%s: an empty field in %+v", tc.group, h)
		}
	}
}

func keys(m map[string]Hit) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestAHitNeverEchoesTheMatchedValue(t *testing.T) {
	c := newChecker(t, Config{Names: NewNameSet([]string{"kestrel_demo"})})
	sev := firstTerm(t, CategoryHate, TierSevere, func(string) bool { return true })
	secrets := []string{"someone.private@example.com", "8.8.4.4", "+44 20 7946 0958", "(202) 555-0175", sev}
	for _, s := range secrets {
		for _, h := range c.Inspect(Message{Text: "x " + s + " y", AuthorName: "viewer"}) {
			if strings.Contains(strings.ToLower(h.Reason), strings.ToLower(s)) {
				t.Errorf("%s: reason echoes the matched value", h.Group)
			}
		}
	}
}

func TestOnlyTheStrongestTierIsReported(t *testing.T) {
	c := newChecker(t, Config{})
	sev := firstTerm(t, CategoryHate, TierSevere, func(string) bool { return true })
	ctx := firstTerm(t, CategoryHate, TierContextual, func(string) bool { return true })
	got := groupsOf(c.Inspect(Message{Text: sev + " and " + ctx}))
	if _, ok := got[GroupHateSevere]; !ok {
		t.Fatalf("severe missing: %v", keys(got))
	}
	if _, ok := got[GroupHateContextual]; ok {
		t.Error("a severe hit also scored as contextual")
	}
}

func TestDocumentationRangesAreRejectedInProductionOnly(t *testing.T) {
	msg := Message{Text: "his ip is 203.0.113.45 and 2001:db8:85a3:0:0:8a2e:370:7334"}
	prod := newChecker(t, Config{}) // the zero value is production
	if DefaultReservedPolicy() != (ReservedPolicy{}) {
		t.Fatal("the default policy is not the zero value")
	}
	if got := groupsOf(prod.Inspect(msg)); len(got) != 0 {
		t.Errorf("production reported a documentation address: %v", keys(got))
	}
	test := newChecker(t, Config{Reserved: ReservedPolicy{AllowDocumentation: true}})
	got := groupsOf(test.Inspect(msg))
	if _, ok := got[GroupIPv4]; !ok {
		t.Error("the harness policy missed the documentation IPv4")
	}
	if _, ok := got[GroupIPv6]; !ok {
		t.Error("the harness policy missed the documentation IPv6")
	}
	// Private, loopback and link-local stay rejected under BOTH policies.
	for _, ip := range []string{"192.168.1.20", "10.0.0.7", "127.0.0.1", "169.254.1.1", "172.16.5.5"} {
		for name, c := range map[string]*Checker{"prod": prod, "test": test} {
			if len(c.Inspect(Message{Text: "ip " + ip})) != 0 {
				t.Errorf("%s reported %s", name, ip)
			}
		}
	}
}

func TestNumberTrapsAreNotDoxxing(t *testing.T) {
	c := newChecker(t, Config{})
	for _, s := range []string{
		"final score 3-2", "see 12:30:45 on the vod", "1920x1080 at 6000 kbps", "on 2024-01-15 we shipped",
		"running v1.2.3.4 here", "version 2.0.1.4 is out", "jev-1.14.0 works", "price is $1,299.99",
		"123-456-7890 is a fake", "order 1234567890", "call 000-000-0000", "see 3:2:1 go",
	} {
		for _, h := range c.Inspect(Message{Text: s}) {
			if strings.HasPrefix(h.Group, "doxxing/") {
				t.Errorf("%q fired %s", s, h.Group)
			}
		}
	}
}

func TestPhoneShapes(t *testing.T) {
	c := newChecker(t, Config{})
	for s, want := range map[string]bool{
		"+1 (202) 555-0175":      true,
		"202-555-0175":           true,
		"(202) 555-0175":         true,
		"202.555.0175":           true,
		"202/555/0175":           true,
		"+442079460958":          true,
		"1 202 555 0175":         true,
		"20255501":               false, // eight digits, no plus
		"2025550175":             false, // ten digits, no separators
		"2024-01-15":             false,
		"3-2":                    false,
		"call me at 202-555-017": false, // too short
	} {
		_, got := groupsOf(c.Inspect(Message{Text: s}))[GroupPhone]
		if got != want {
			t.Errorf("%q: phone = %v, want %v", s, got, want)
		}
	}
	// A channel whose game posts scores can ask for more separators.
	strict := newChecker(t, Config{PhoneMinSeps: 3})
	if _, ok := groupsOf(strict.Inspect(Message{Text: "202 555 0175"}))[GroupPhone]; ok {
		t.Error("two separators should not fire under the strict setting")
	}
	if _, ok := groupsOf(strict.Inspect(Message{Text: "1 202 555 0175"}))[GroupPhone]; !ok {
		t.Error("three separators should still fire under the strict setting")
	}
}

func TestEmailDetectionAndAllowDomains(t *testing.T) {
	c := newChecker(t, Config{AllowDomains: []string{"mysponsor.com"}})
	for s, want := range map[string]bool{
		"a.b@example.com":                   true,
		"riley [at] mail.example [dot] com": true,
		"x@sub.example.co.uk":               true,
		"@dusk_runner thanks":               false,
		"see you @ 5pm":                     false,
		"a@b":                               false,
		"me@mysponsor.com":                  false,
		"me@shop.mysponsor.com":             false,
	} {
		_, got := groupsOf(c.Inspect(Message{Text: s}))[GroupEmail]
		if got != want {
			t.Errorf("%q: email = %v, want %v", s, got, want)
		}
	}
}

func TestScamDetectorsAndTheirTraps(t *testing.T) {
	c := newChecker(t, Config{AllowDomains: []string{"bit.ly"}})
	cases := map[string]string{
		"free skins drop tonight?":                     "",
		"when is the next giveaway?":                   "",
		"send it!":                                     "",
		"dm me for ideas on the overlay":               "",
		"i'll add you on snap later":                   "",
		"twitch support helped me yesterday":           "",
		"free robux, claim at https://claim.example/x": GroupGiveaway,
		"airdrop wallet usdt @crypto_drops_now":        GroupGiveaway,
		"link in my bio":                               GroupBio,
		"dm me for collab":                             GroupBio,
		"add me on telegram":                           GroupBio,
		"youtube team: dm us to verify":                GroupSupport,
		"your channel will be banned, click now":       GroupSupport,
		"go to https://tinyurl.com/x":                  GroupShortener,
		"go to https://bit.ly/x":                       "", // allow-listed
		"https://discоrd.com/new":                      GroupLookalike,
		"https://www.twitch.tv/directory":              "",
		"https://emotes.twіtch.tv/x":                   GroupLookalike,
	}
	for s, want := range cases {
		got := ""
		for g := range groupsOf(c.Inspect(Message{Text: s})) {
			if strings.HasPrefix(g, "scam/") {
				got = g
			}
		}
		if got != want {
			t.Errorf("%q: scam group %q, want %q", s, got, want)
		}
	}
}

func TestStretchedLettersFallBackToTheCollapsedTerm(t *testing.T) {
	// A severe single-token term with no doubled letter and four or more
	// letters, stretched the way a sender would.
	term := firstTerm(t, CategoryHate, TierSevere, func(s string) bool {
		if len(s) < 4 {
			return false
		}
		for i := 1; i < len(s); i++ {
			if s[i] == s[i-1] {
				return false
			}
		}
		return Fold(s) == s
	})
	stretched := string(term[0]) + strings.Repeat(string(term[1]), 6) + term[2:]
	if Fold(stretched) == term {
		t.Fatalf("the stretched form should not fold onto the term by itself")
	}
	hit, ok := newTermIndexFor(t).lookup(NewForms("you " + stretched))
	if !ok || hit.form != "stretched" {
		t.Fatalf("stretched term: ok=%v form=%q", ok, hit.form)
	}
	// Two of a letter is ordinary spelling and must not take the fallback.
	if _, ok := newTermIndexFor(t).lookup(NewForms("you " + string(term[0]) + strings.Repeat(string(term[1]), 2) + term[2:])); ok {
		t.Error("a doubled letter was treated as a stretch")
	}
	// An exception token is never collapsed onto its term.
	idx := newTermIndexFor(t)
	for _, ref := range idx.single {
		for x := range ref.except {
			if _, ok := idx.lookup(NewForms(x)); ok {
				t.Error("an exception token matched")
			}
		}
	}
}

func newTermIndexFor(t *testing.T) *termIndex {
	t.Helper()
	p, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	return buildTermIndex(p, nil, nil, map[string]bool{CategoryHate: true})
}

func TestDespacedAndSpelledOutTerms(t *testing.T) {
	c := newChecker(t, Config{})
	term := firstTerm(t, CategoryHate, TierSevere, func(s string) bool { return len(s) >= 4 && !strings.Contains(s, " ") })
	spaced := strings.Join(strings.Split(term, ""), " ")
	for _, text := range []string{spaced, "look at this " + spaced} {
		if _, ok := groupsOf(c.Inspect(Message{Text: text}))[GroupHateSevere]; !ok {
			t.Errorf("letter-spaced form not caught in %d-char message", len(text))
		}
	}
	// "a bad wordsmith"-style benign text: short words are not a spelled-out term.
	if len(c.Inspect(Message{Text: "i a m a g o o d d a y"})) != 0 {
		// Only complain if it is a hate hit: shape detectors may speak.
		for _, h := range c.Inspect(Message{Text: "i a m a g o o d d a y"}) {
			if strings.HasPrefix(h.Group, "hate_speech") {
				t.Error("benign spaced text matched a term")
			}
		}
	}
}

func TestAllowAndAddTerms(t *testing.T) {
	sev := firstTerm(t, CategoryHate, TierSevere, func(string) bool { return true })
	allowed := newChecker(t, Config{AllowTerms: []string{sev}})
	if _, ok := groupsOf(allowed.Inspect(Message{Text: sev}))[GroupHateSevere]; ok {
		t.Error("an allowed term still fired")
	}
	added := newChecker(t, Config{AddTerms: []AddTerm{{Term: "Zorblax Quux", Tier: TierSevere}, {Term: "snarkfish", Tier: TierContextual}}})
	if _, ok := groupsOf(added.Inspect(Message{Text: "that zorblax  quux guy"}))[GroupHateSevere]; !ok {
		t.Error("an added multi-word term did not fire")
	}
	if _, ok := groupsOf(added.Inspect(Message{Text: "snarkfish!"}))[GroupHateContextual]; !ok {
		t.Error("an added contextual term did not fire")
	}
}

func TestDetectorTogglesAndHoldOverrides(t *testing.T) {
	c := newChecker(t, Config{
		Detectors: map[string]bool{GroupEmail: false},
		Hold:      map[string]bool{GroupZalgo: false, GroupCaps: true},
	})
	got := groupsOf(c.Inspect(Message{Text: "I AM SHOUTING THIS VERY LOUD MESSAGE HERE x@e.io"}))
	if _, ok := got[GroupEmail]; ok {
		t.Error("a switched-off detector fired")
	}
	if h, ok := got[GroupCaps]; !ok || !h.Hold {
		t.Error("the caps hold override did not apply")
	}
	if c.HoldFor(GroupZalgo) || !c.HoldFor(GroupCaps) {
		t.Error("HoldFor disagrees with the override")
	}
	if !c.Enabled(GroupPhone) || c.Enabled(GroupEmail) || c.Enabled(GroupAddress) {
		t.Error("Enabled disagrees with the defaults")
	}
	if len(Groups()) != 23 {
		t.Errorf("Groups() lists %d, want 23", len(Groups()))
	}
}

func TestImpersonation(t *testing.T) {
	names := NewNameSet([]string{"kestrel_demo", "Mossy Lantern"})
	c := newChecker(t, Config{Names: names, AllowIDs: map[string]bool{"twitch:77": true}})
	cases := []struct {
		name, text, id string
		want           string
	}{
		{"kestrel_demo", "hi", "1", ""},              // the real account
		{"Kestrel_Demo", "hi", "1", ""},              // same name, other case
		{"kestrel_demo_official", "hi", "1", "fold"}, // decoration
		{"kestrelDemo2", "hi", "1", "fold"},          // digit suffix
		{"kеstrel_demo", "hi", "1", "fold"},          // Cyrillic е
		{"kestrel_demo_official", "hi", "77", ""},    // allow-listed id
		{"keszrel_demo", "hi chat", "1", ""},         // edit distance 1, no signal
		{"keszrel_demo", "join https://x.example", "1", "edit1"},
		{"keszrel_demo", "join @someone_else", "1", "edit1"},
		{"keszrel_demo", "free nitro giveaway @crypto_drops", "1", "edit1"},
		{"Mosy Lantern", "see https://x.example", "1", "edit1"},
		{"ab", "see https://x.example", "1", ""}, // too short to fold
		{"Nikita", "hello", "1", ""},
		{"", "hello", "1", ""},
	}
	for _, tc := range cases {
		got := ""
		for _, h := range c.Inspect(Message{Platform: "twitch", AuthorID: tc.id, AuthorName: tc.name, Text: tc.text}) {
			if h.Group == GroupImpersonation {
				got = h.Detector
				if !strings.Contains(h.Reason, "looks like") {
					t.Errorf("%q: reason %q does not say who", tc.name, h.Reason)
				}
			}
		}
		if got != tc.want {
			t.Errorf("%q: impersonation %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestNameSetIsBoundedAndSafeToShare(t *testing.T) {
	n := NewNameSet([]string{"alpha_streamer"})
	n.max = 5
	for i := 0; i < 20; i++ {
		n.Learn(fmt.Sprintf("moderator_%02d", i))
	}
	n.Learn("moderator_19") // a repeat changes nothing
	n.Learn("x")            // too short to protect
	if got := n.Len(); got != 6 {
		t.Errorf("Len = %d, want the static name plus 5 learned", got)
	}
	n.SetStatic([]string{"beta_streamer"})
	if got := n.Len(); got != 6 {
		t.Errorf("after SetStatic Len = %d, want the new static name plus 5 learned", got)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			n.Learn(fmt.Sprintf("racer_%d", i))
		}
	}()
	for i := 0; i < 200; i++ {
		n.match("someone_else_here", func() bool { return false })
	}
	<-done
}

func TestShapeFunctions(t *testing.T) {
	lim := DefaultShapeLimits()
	for s, want := range map[string]bool{
		"Chúc mừng bạn nhé": false,
		"Cảm ơn bạn đã xem": false,
		"नमस्ते दुनिया":     false,
		"สวัสดีครับ ทุกคน":  false,
		"hello world":       false,
		"h̛̖̗̘̙̜̝̞̟̚i":      true,
	} {
		if got := Zalgo(s, lim); got != want {
			t.Errorf("Zalgo(%q) = %v, want %v", s, got, want)
		}
	}
	if Zalgo("h̖̗i", ShapeLimits{}) {
		t.Error("a zero limit should switch zalgo off")
	}
	if !CharFlood(strings.Repeat("a", 30), lim) || CharFlood(strings.Repeat("a", 29), lim) {
		t.Error("the 30-in-a-row boundary is wrong")
	}
	if !CharFlood(strings.Repeat("ab ", 110), lim) || CharFlood(strings.Repeat("abcde ", 60), lim) {
		t.Error("the long-and-nearly-empty rule is wrong")
	}
	if CharFlood(strings.Repeat(" ", 40), lim) == false {
		// 40 spaces is 40 runes but a space is never a "non-space rune" run;
		// it is caught only by the distinct-rune rule once over 300.
		t.Log("spaces are not a run")
	}
	if CharFlood("x", ShapeLimits{}) {
		t.Error("a zero limit should switch char flood off")
	}
	if e, n := EmoteCount("Kappa hello LUL", 0); e != 2 || n != 3 {
		t.Errorf("EmoteCount = %d/%d", e, n)
	}
	if e, _ := EmoteCount("anything goes here", 7); e != 7 {
		t.Errorf("a platform count should win, got %d", e)
	}
	if !EmoteWall(strings.Repeat("x ", 20), 20, lim) || EmoteWall(strings.Repeat("x ", 20), 12, lim) {
		t.Error("platform-counted wall boundaries wrong")
	}
	if EmoteWall("a", 5, ShapeLimits{}) || EmoteWall("", 20, lim) {
		t.Error("EmoteWall should refuse a zero limit and an empty message")
	}
	if emoteOnly("", 0) || !emoteOnly("Kappa Kappa", 0) || emoteOnly("Kappa hi", 0) || !emoteOnly("a b", 2) {
		t.Error("emoteOnly is wrong")
	}
	if !isCommand("!play") || !isCommand("  /me waves") || isCommand("hello !play") || isCommand("  ") {
		t.Error("isCommand is wrong")
	}
	if Mentions("@a @b") != 2 || Links("http://a https://b") != 2 || Links("www.example.com") != 1 || Links("plain") != 0 {
		t.Error("Mentions or Links count wrongly")
	}
	if r, ok := CapsRatio("ABCDEFGHIJKL", 12); !ok || r != 1 {
		t.Errorf("CapsRatio = %v %v", r, ok)
	}
	if _, ok := CapsRatio("OK", 12); ok {
		t.Error("a short message cannot be shouting")
	}
}

func TestInspectLeavesNoTrace(t *testing.T) {
	raid, nd := NewRaid(RaidConfig{}), NewNearDup(NearDupConfig{})
	c := newChecker(t, Config{Raid: raid, NearDup: nd})
	for i := 0; i < 50; i++ {
		c.Inspect(Message{Platform: "twitch", AuthorID: fmt.Sprint(i), Text: "everyone type this exact message in chat"})
	}
	if raid.Tracked() != 0 || nd.Tracked() != 0 {
		t.Errorf("Inspect recorded state: raid %d, near-dup %d", raid.Tracked(), nd.Tracked())
	}
	c.Observe(Message{Platform: "twitch", AuthorID: "1", Text: "everyone type this exact message in chat"})
	if raid.Tracked() != 1 || nd.Tracked() != 1 {
		t.Errorf("Observe did not record: raid %d, near-dup %d", raid.Tracked(), nd.Tracked())
	}
}

var t0 = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

func TestRaidFiresAtFiveDistinctAuthorsAndHoldsTheRestOfTheWindow(t *testing.T) {
	r := NewRaid(RaidConfig{})
	msg := func(i int) Forms {
		return NewForms(fmt.Sprintf("everyone type this exact message in chat right now%s", strings.Repeat("!", i%3)))
	}
	var fired int
	for i := 0; i < 5; i++ {
		_, ok := r.Observe(t0.Add(time.Duration(i)*time.Second), fmt.Sprintf("twitch:%d", i), msg(i), 0)
		if ok {
			fired = i + 1
		}
	}
	if fired != 5 {
		t.Fatalf("fired at message %d, want 5", fired)
	}
	hit, ok := r.Observe(t0.Add(8*time.Second), "kick:99", msg(9), 0)
	if !ok || !hit.Continuing {
		t.Errorf("a later matching message should be held without recounting: %+v %v", hit, ok)
	}
	if len(hit.Names) == 0 || len(hit.Names) > 5 {
		t.Errorf("Names = %v", hit.Names)
	}
	if !strings.Contains(hit.Reason(20*time.Second), "5 accounts") {
		t.Errorf("reason %q", hit.Reason(20*time.Second))
	}
	// After the window the same text starts counting again from nothing.
	if _, ok := r.Observe(t0.Add(2*time.Minute), "twitch:200", msg(0), 0); ok {
		t.Error("an old raid held a message two minutes later")
	}
}

func TestRaidIgnoresRepeatsFromOneAuthorAndExemptShapes(t *testing.T) {
	r := NewRaid(RaidConfig{Allow: []string{"the community chant goes here today"}})
	long := NewForms("everyone type this exact message in chat right now")
	for i := 0; i < 20; i++ {
		if _, ok := r.Observe(t0.Add(time.Duration(i)*100*time.Millisecond), "twitch:1", long, 0); ok {
			t.Fatal("one author, many times, is not a raid")
		}
	}
	for name, text := range map[string]string{
		"short":   "gg",
		"command": "!play the next song please now",
		"emotes":  "Kappa Kappa Kappa Kappa Kappa",
		"allowed": "the community chant goes here today",
	} {
		for i := 0; i < 10; i++ {
			if _, ok := r.Observe(t0.Add(time.Duration(i)*time.Second), fmt.Sprintf("twitch:%d", 100+i), NewForms(text), 0); ok {
				t.Errorf("%s fired a raid", name)
				break
			}
		}
	}
	if _, ok := r.Observe(t0, "", long, 0); ok {
		t.Error("an anonymous message was counted")
	}
	var nilRaid *Raid
	if _, ok := nilRaid.Observe(t0, "x", long, 0); ok {
		t.Error("a nil tracker fired")
	}
}

func TestRaidMemoryIsBounded(t *testing.T) {
	r := NewRaid(RaidConfig{MaxClusters: 10, MaxAuthorsPer: 8})
	for i := 0; i < 500; i++ {
		text := fmt.Sprintf("unique raid message number %s here with enough characters", strings.Repeat(string(rune('a'+i%26)), 1+i%7))
		r.Observe(t0.Add(time.Duration(i)*time.Millisecond), fmt.Sprintf("twitch:%d", i), NewForms(text+fmt.Sprint(i)+" "+strings.Repeat("w"+fmt.Sprint(i), 3)), 0)
	}
	if r.Tracked() > 10 {
		t.Errorf("tracking %d clusters, cap is 10", r.Tracked())
	}
	same := NewForms("a raid of one text from a great many different accounts")
	for i := 0; i < 400; i++ {
		r.Observe(t0.Add(10*time.Second+time.Duration(i)*time.Millisecond), fmt.Sprintf("kick:%d", i), same, 0)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.clusters {
		if len(c.seen) > 8 {
			t.Errorf("a cluster remembers %d authors, cap is 8", len(c.seen))
		}
	}
}

func TestNearDuplicatesNeedMoreThanTheLimit(t *testing.T) {
	d := NewNearDup(NearDupConfig{})
	send := func(i int, text string) (int, bool) {
		return d.Observe(t0.Add(time.Duration(i)*5*time.Second), "twitch:1", NewForms(text))
	}
	for i, text := range []string{"buy now 1", "buy now 2", "buy now 3"} {
		if _, over := send(i, text); over {
			t.Fatalf("message %d is at the limit, not over it", i+1)
		}
	}
	if n, over := send(3, "buy now 4"); !over || n != 4 {
		t.Errorf("fourth numbered message: n=%d over=%v", n, over)
	}
	// Spacing past the window forgets.
	if _, over := d.Observe(t0.Add(10*time.Minute), "twitch:1", NewForms("buy now 9")); over {
		t.Error("an old burst still counted")
	}
	// Varying one word at a time still chains back to the first.
	v := NewNearDup(NearDupConfig{})
	var over bool
	for i, text := range []string{
		"i am selling a great product", "i am selling a really great product",
		"i am selling a great product now", "i am selling a great product today",
	} {
		_, over = v.Observe(t0.Add(time.Duration(i)*3*time.Second), "twitch:2", NewForms(text))
	}
	if !over {
		t.Error("a chain of near-copies was not caught")
	}
	// A short reaction and distinct messages are never a repeat.
	for i := 0; i < 10; i++ {
		if _, over := v.Observe(t0.Add(time.Duration(i)*time.Second), "twitch:3", NewForms("gg")); over {
			t.Fatal("gg counted as a repeat")
		}
	}
	for i, text := range []string{"i love this boss fight", "this soundtrack is so good", "what time does it end", "who is the best player", "that was an amazing play"} {
		if _, over := v.Observe(t0.Add(time.Duration(i)*time.Second), "twitch:4", NewForms(text)); over {
			t.Fatal("distinct messages counted as repeats")
		}
	}
	if _, over := (*NearDup)(nil).Observe(t0, "x", NewForms("hello there friend")); over {
		t.Error("a nil detector fired")
	}
}

func TestNearDupMemoryIsBounded(t *testing.T) {
	d := NewNearDup(NearDupConfig{MaxAuthors: 50, Retain: 4})
	for i := 0; i < 400; i++ {
		d.Observe(t0.Add(time.Duration(i)*time.Millisecond), fmt.Sprintf("twitch:%d", i), NewForms("one message from many different people today"))
	}
	if d.Tracked() > 50 {
		t.Errorf("tracking %d authors, cap is 50", d.Tracked())
	}
	for i := 0; i < 30; i++ {
		d.Observe(t0.Add(time.Hour+time.Duration(i)*time.Second), "twitch:retain", NewForms("one message from many different people today"))
	}
	d.mu.Lock()
	n := len(d.authors["twitch:retain"].entries)
	d.mu.Unlock()
	if n > 4 {
		t.Errorf("an author keeps %d entries, cap is 4", n)
	}
	// Idle authors are swept.
	d.Observe(t0.Add(48*time.Hour), "twitch:late", NewForms("a fresh message arrives much later on"))
	if d.Tracked() != 1 {
		t.Errorf("after a long idle %d authors remain, want 1", d.Tracked())
	}
}

func TestWindowedHitsFromTheChecker(t *testing.T) {
	c := newChecker(t, Config{Raid: NewRaid(RaidConfig{}), NearDup: NewNearDup(NearDupConfig{})})
	var raid, near bool
	for i := 0; i < 6; i++ {
		for _, h := range c.Observe(Message{Platform: "twitch", AuthorID: fmt.Sprint(i), At: t0.Add(time.Duration(i) * time.Second),
			Text: "everyone type this exact message in chat right now"}) {
			raid = raid || h.Group == GroupRaid && h.Hold && h.Category == CategoryRaid
		}
	}
	for i := 0; i < 5; i++ {
		for _, h := range c.Observe(Message{Platform: "twitch", AuthorID: "spam", At: t0.Add(time.Minute + time.Duration(i)*time.Second),
			Text: fmt.Sprintf("giveaway entry %d", i)}) {
			near = near || h.Group == GroupNearDuplicate && !h.Hold && h.Category == CategoryFlood && h.Detector == "near_duplicate"
		}
	}
	if !raid || !near {
		t.Errorf("raid=%v near-duplicate=%v", raid, near)
	}
}

func TestPackValidateRejectsAnExceptionThatIsASevereTerm(t *testing.T) {
	mk := func(exc []string) *Pack {
		return &Pack{ID: "t", Version: 1, Sources: []Source{{Name: "x"}}, Entries: []Entry{
			{Term: "zorb", Category: CategoryHate, Tier: TierSevere, Except: exc},
			{Term: "zorbs", Category: CategoryHate, Tier: TierSevere},
			{Term: "zorbo", Category: CategoryHate, Tier: TierContextual},
		}}
	}
	if err := mk([]string{"zorbs"}).Validate(); err == nil {
		t.Error("an exception that is itself a severe term was accepted")
	}
	if err := mk([]string{"zorbo"}).Validate(); err != nil {
		t.Errorf("an exception equal to a contextual term is allowed: %v", err)
	}
	if err := mk(nil).Validate(); err != nil {
		t.Errorf("a clean pack was refused: %v", err)
	}
}
