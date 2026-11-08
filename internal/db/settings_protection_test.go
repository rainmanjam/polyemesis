package db

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func decodeAutomod(t *testing.T, base AutomodSettings, doc string) AutomodSettings {
	t.Helper()
	if err := json.Unmarshal([]byte(doc), &base); err != nil {
		t.Fatal(err)
	}
	return base
}

func TestAFreshInstallHasProtectionOnAndArmsNothing(t *testing.T) {
	d := testDB(t)
	s, err := d.GetSettings() // no row yet: the ErrNoRows branch
	if err != nil {
		t.Fatal(err)
	}
	p := s.Automod.Protection
	if p.State != ProtectionOn || p.Pack != DefaultProtectionPack || p.PackVersion != DefaultProtectionPackVersion {
		t.Fatalf("fresh protection: %+v", p)
	}
	for k := range s.Automod.On {
		if strings.HasSuffix(k, "/protection") && !strings.Contains(k, "/flag/") {
			t.Fatalf("a fresh install armed %s", k)
		}
	}
	// Sparse: no copy of the pack's own defaults.
	if p.Categories != nil || p.Ceilings != nil || p.AddTerms != nil || p.AllowTerms != nil ||
		p.AllowDomains != nil || p.HoldDetectors != nil {
		t.Fatalf("DefaultSettings wrote pack data into the diff: %+v", p)
	}
	if p.StrikeWindowSeconds != DefaultLadderSeconds || p.LadderTimeoutSeconds != DefaultLadderSeconds {
		t.Fatalf("ladder defaults: %+v", p)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("the default settings do not validate: %v", err)
	}
	// And the second read is the stored row, still on.
	again, err := d.GetSettings()
	if err != nil || again.Automod.Protection.State != ProtectionOn {
		t.Fatalf("second read: %v %+v", err, again.Automod.Protection)
	}
}

func TestAnExistingInstallIsUndecidedNotSwitchedOn(t *testing.T) {
	d := testDB(t)
	// A blob written by a build with no protection block at all.
	legacy := `{"automod":{"enabled":true,"on":{"twitch/flag/rules":true}}}`
	if _, err := d.SQL().Exec(`INSERT INTO settings (id, json) VALUES (1, ?)`, legacy); err != nil {
		t.Fatal(err)
	}
	s, err := d.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Automod.Protection.State; got != "" {
		t.Fatalf("an upgraded install reads protection %q, want undecided", got)
	}
	if s.Automod.Protection.PackVersion != 0 {
		t.Fatalf("an undecided install has accepted pack v%d", s.Automod.Protection.PackVersion)
	}
	// A partial block names no state: still undecided.
	if _, err := d.SQL().Exec(`UPDATE settings SET json = ? WHERE id = 1`, `{"automod":{"protection":{}}}`); err != nil {
		t.Fatal(err)
	}
	if s, _ = d.GetSettings(); s.Automod.Protection.State != "" {
		t.Fatalf("a block with no state reads %q", s.Automod.Protection.State)
	}
	// An answer is kept.
	for _, st := range []string{"on", "off"} {
		if _, err := d.SQL().Exec(`UPDATE settings SET json = ? WHERE id = 1`, `{"automod":{"protection":{"state":"`+st+`"}}}`); err != nil {
			t.Fatal(err)
		}
		if s, _ = d.GetSettings(); s.Automod.Protection.State != st {
			t.Fatalf("stored %q reads %q", st, s.Automod.Protection.State)
		}
	}
}

func TestASentProtectionCollectionReplacesTheStoredOne(t *testing.T) {
	on := true
	base := AutomodSettings{Protection: AutomodProtection{
		State:         ProtectionOn,
		Categories:    map[string]ProtectionCategory{"doxxing": {Enabled: &on}, "scam": {Enabled: &on}},
		Ceilings:      map[string]string{"scam": "timeout"},
		AddTerms:      []ProtectionTerm{{Term: "aaaa", Category: "hate_speech", Tier: "severe"}, {Term: "bbbb", Category: "hate_speech", Tier: "severe"}},
		AllowTerms:    []string{"cccc", "dddd"},
		AllowDomains:  []string{"a.example", "b.example"},
		HoldDetectors: map[string]bool{"caps": true},
		Impersonation: ProtectionImpersonation{Protect: []string{"x", "y"}, AllowIDs: []string{"twitch:1"}},
	}}
	got := decodeAutomod(t, base, `{"protection":{
		"categories":{"scam":{}},
		"ceilings":null,
		"addTerms":[{"term":"eeee","category":"hate_speech"}],
		"allowTerms":[],
		"allowDomains":["c.example"],
		"holdDetectors":{},
		"impersonation":{"protect":["z"]}}}`).Protection
	if len(got.Categories) != 1 || got.Categories["scam"].Enabled != nil {
		t.Fatalf("categories merged instead of replaced: %+v", got.Categories)
	}
	if got.Ceilings != nil {
		t.Fatalf("a null ceilings did not clear: %+v", got.Ceilings)
	}
	if len(got.AddTerms) != 1 || got.AddTerms[0].Term != "eeee" || got.AddTerms[0].Tier != "" {
		t.Fatalf("addTerms kept a stale field by index: %+v", got.AddTerms)
	}
	if len(got.AllowTerms) != 0 || len(got.HoldDetectors) != 0 {
		t.Fatalf("empty collections did not clear: %+v %+v", got.AllowTerms, got.HoldDetectors)
	}
	if len(got.AllowDomains) != 1 || got.AllowDomains[0] != "c.example" {
		t.Fatalf("allowDomains: %v", got.AllowDomains)
	}
	if len(got.Impersonation.Protect) != 1 || got.Impersonation.Protect[0] != "z" {
		t.Fatalf("protect: %v", got.Impersonation.Protect)
	}
	// Not named: untouched. A client that has never heard of allowIds is safe.
	if len(got.Impersonation.AllowIDs) != 1 || got.State != ProtectionOn {
		t.Fatalf("an unnamed key changed: %+v", got)
	}
}

func TestACollectionSavedAfterAnEditSurvivesAFullRoundTrip(t *testing.T) {
	// The console round-trips the whole document. Removing the last term must
	// stick: the empty list marshals to null, which has to read as "empty".
	base := AutomodSettings{Protection: AutomodProtection{AddTerms: []ProtectionTerm{{Term: "aaaa", Category: "hate_speech", Tier: "severe"}}}}
	sent := AutomodSettings{}
	b, err := json.Marshal(sent)
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeAutomod(t, base, string(b)).Protection; len(got.AddTerms) != 0 {
		t.Fatalf("the last added term came back: %+v", got.AddTerms)
	}
}

func TestProtectionStrictFieldsRefuseATypo(t *testing.T) {
	for _, doc := range []string{
		`{"protection":{"stat":"on"}}`,
		`{"protection":{"categories":{"scam":{"enable":true}}}}`,
		`{"protection":{"addTerms":[{"word":"x"}]}}`,
		`{"protection":{"impersonation":{"protected":["x"]}}}`,
	} {
		if err := CheckAutomodFields([]byte(doc)); err == nil {
			t.Errorf("accepted a typo: %s", doc)
		}
	}
	if err := CheckAutomodFields([]byte(`{"protection":{"state":"on","categories":{"scam":{"enabled":false,"detectors":{"bio":false}}}}}`)); err != nil {
		t.Errorf("refused a valid block: %v", err)
	}
}

func TestProtectionProblems(t *testing.T) {
	long := strings.Repeat("x", 101)
	cases := []struct {
		name string
		p    AutomodProtection
		want string // substring of a problem, "" for none
	}{
		{"fine", AutomodProtection{State: ProtectionOn, StrikeWindowSeconds: 600, LadderTimeoutSeconds: 600}, ""},
		{"undecided with zeros", AutomodProtection{}, ""},
		{"bad state", AutomodProtection{State: "maybe"}, "state"},
		{"zero timeout when on is a permanent ban", AutomodProtection{State: ProtectionOn, StrikeWindowSeconds: 600}, "ladder timeout"},
		{"zero window when on", AutomodProtection{State: ProtectionOn, LadderTimeoutSeconds: 600}, "strike window"},
		{"negative timeout", AutomodProtection{LadderTimeoutSeconds: -1}, "ladder timeout"},
		{"timeout over two weeks", AutomodProtection{LadderTimeoutSeconds: 14*24*3600 + 1}, "ladder timeout"},
		{"unknown ceiling", AutomodProtection{Ceilings: map[string]string{"scam": "kick"}}, "ceiling"},
		{"self_harm cannot be raised", AutomodProtection{Ceilings: map[string]string{"self_harm": "delete"}}, "self_harm"},
		{"self_harm at hold", AutomodProtection{Ceilings: map[string]string{"self_harm": "hold"}}, ""},
		{"short term", AutomodProtection{AddTerms: []ProtectionTerm{{Term: "a"}}}, "added term"},
		{"long term", AutomodProtection{AddTerms: []ProtectionTerm{{Term: long}}}, "added term"},
		{"two rune term", AutomodProtection{AddTerms: []ProtectionTerm{{Term: "日本"}}}, ""},
		{"short allowed term", AutomodProtection{AllowTerms: []string{"a"}}, "allowed terms"},
		{"blank domain", AutomodProtection{AllowDomains: []string{"  "}}, "allowed domains"},
		{"too many names", AutomodProtection{Impersonation: ProtectionImpersonation{Protect: make([]string, 51)}}, "more than"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var probs []string
			tc.p.problems(func(f string, a ...any) { probs = append(probs, fmt.Sprintf(f, a...)) })
			joined := strings.Join(probs, "; ")
			if tc.want == "" && joined != "" {
				t.Fatalf("unexpected problems: %s", joined)
			}
			if tc.want != "" && !strings.Contains(joined, tc.want) {
				t.Fatalf("problems %q lack %q", joined, tc.want)
			}
		})
	}
}

// Accepting the pack records the version accepted: an undecided install that
// says yes with only {state:"on"} is on v1, not on v0.
func TestAcceptingTheProtectionPackRecordsItsVersion(t *testing.T) {
	d := testDB(t)
	legacy := `{"automod":{"enabled":true}}`
	if _, err := d.SQL().Exec(`INSERT INTO settings (id, json) VALUES (1, ?)`, legacy); err != nil {
		t.Fatal(err)
	}
	s, err := d.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.Automod.Protection.State != "" || s.Automod.Protection.PackVersion != 0 {
		t.Fatalf("not undecided: %+v", s.Automod.Protection)
	}
	// The operator says yes: the API decodes the sent body over the stored one.
	if err := json.Unmarshal([]byte(`{"automod":{"protection":{"state":"on"}}}`), &s); err != nil {
		t.Fatal(err)
	}
	if got := s.Automod.Protection; got.State != ProtectionOn || got.PackVersion != DefaultProtectionPackVersion {
		t.Fatalf("accepted pack: state %q packVersion %d, want on / %d", got.State, got.PackVersion, DefaultProtectionPackVersion)
	}
	if err := d.PutSettings(s); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.GetSettings(); got.Automod.Protection.PackVersion != DefaultProtectionPackVersion {
		t.Fatalf("stored packVersion %d", got.Automod.Protection.PackVersion)
	}
	// Saying no does not accept anything.
	s2, _ := d.GetSettings()
	if err := json.Unmarshal([]byte(`{"automod":{"protection":{"state":"off","packVersion":0}}}`), &s2); err != nil {
		t.Fatal(err)
	}
	if s2.Automod.Protection.PackVersion != 0 {
		t.Fatalf("off recorded version %d", s2.Automod.Protection.PackVersion)
	}
}
