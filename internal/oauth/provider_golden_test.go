package oauth

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// THE PROVIDER GOLDEN. #161, for the six skips in this package.
//
// internal/oauth had six t.Skip sites, and three of them are the worst species
// there is: a test that SILENCES ITSELF WHEN ITS SUBJECT DRIFTS.
//
//	scopever_test.go   "Twitch's scope version has moved; this case needs updating"
//	scopever_test.go   "Twitch asks for fewer than two scopes; this case needs updating"
//	guide_drift_test.go "the Facebook caveat has been reworded; nothing to assert"
//	guide_drift_test.go "no Facebook guide"
//	kick_test.go       "Kick is not in Providers() yet"  (x2)
//
// Twitch's ScopeVersion() is 4. The skip fires when it is not 1. So
// TestATokenIssuedBeforeAScopeWasAddedIsFlagged -- whose own comment calls
// itself "the case the whole mechanism exists for" -- has been passing on main
// by declining to run, for however many versions it has been since 1. A skip
// that fires BECAUSE the thing it tests changed is a test that deletes itself on
// the day it matters, and it prints "ok" while doing so.
//
// A golden is the answer rather than a date-stamped drift guard: it asserts on
// every single invocation, and the only way to discharge it is a diff a reviewer
// reads. Same shape as testdata/route-coverage.json, applied to a fifth surface.
//
// Regenerate with:
//
//	go test ./internal/oauth -run TestProviderGolden -update-oauth-golden

var updateOAuthGolden = flag.Bool("update-oauth-golden", false,
	"rewrite internal/oauth/testdata/provider-scopes.json and guide-notes.json from the "+
		"live providers. It records drift; it never explains it.")

const (
	scopesGoldenPath = "testdata/provider-scopes.json"
	guidesGoldenPath = "testdata/guide-notes.json"
)

type providerScopes struct {
	Platform     string   `json:"platform"`
	ScopeVersion int      `json:"scopeVersion"`
	Scopes       []string `json:"scopes"`
	// ManualKeyReason is the advice an operator sees when a platform's key
	// cannot be fetched with the token they hold. kick_test.go used to assert
	// this only if Kick happened to be registered, which is a condition the test
	// itself is about.
	ManualKeyReason string `json:"manualKeyReason,omitempty"`
}

type guideNote struct {
	Platform string   `json:"platform"`
	Note     string   `json:"note"`
	Steps    []string `json:"steps"`
}

func liveProviderScopes() []providerScopes {
	var out []providerScopes
	for p, prov := range Providers() {
		row := providerScopes{
			Platform:     string(p),
			ScopeVersion: prov.ScopeVersion(),
			Scopes:       append([]string(nil), prov.Scopes()...),
		}
		if mk, ok := ManualKeyFor(p); ok {
			row.ManualKeyReason = mk.ManualKeyReason()
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Platform < out[j].Platform })
	return out
}

func liveGuideNotes() []guideNote {
	var out []guideNote
	for _, g := range guides() {
		out = append(out, guideNote{
			Platform: string(g.Platform),
			Note:     g.Note,
			Steps:    append([]string(nil), g.Steps...),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Platform < out[j].Platform })
	return out
}

// TestProviderGolden is the drift guard, and it FAILS on drift rather than
// stepping aside for it.
func TestProviderGolden(t *testing.T) {
	assertGolden(t, scopesGoldenPath, liveProviderScopes(),
		"a provider's scope list, scope version or manual-key advice has moved. "+
			"Every one of those changes something an operator experiences: a scope "+
			"version bump tells existing accounts to reconnect, a scope removed "+
			"silently keeps a token that can no longer do the job. Read the diff, "+
			"decide whether it is intended, and regenerate with -update-oauth-golden.")
	assertGolden(t, guidesGoldenPath, liveGuideNotes(),
		"a setup guide's note or steps have moved. guide_drift_test.go's manual-key "+
			"phrase check is calibrated against Facebook's legitimate per-broadcast "+
			"caveat, and it used to SKIP when that wording changed -- which is the "+
			"moment the calibration needs re-checking, not the moment to stop "+
			"looking. Read the diff and regenerate with -update-oauth-golden.")
}

func assertGolden[T any](t *testing.T, path string, live []T, why string) {
	t.Helper()
	got, err := json.MarshalIndent(live, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	got = append(got, '\n')
	if *updateOAuthGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		t.Logf("regenerated %s", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v\nRun `go test ./internal/oauth -run TestProviderGolden "+
			"-update-oauth-golden` to create it.", path, err)
	}
	if string(want) != string(got) {
		t.Errorf("%s is out of date.\n%s\n--- committed ---\n%s\n--- live ---\n%s",
			path, why, want, got)
	}
}

// TestEveryRegisteredProviderIsInTheGolden closes the direction a golden cannot
// close on its own: a provider ADDED and never recorded would produce a diff, but
// only if somebody ran the test before regenerating blindly. This states the
// count as its own assertion so the failure names the platform.
func TestEveryRegisteredProviderIsInTheGolden(t *testing.T) {
	b, err := os.ReadFile(scopesGoldenPath)
	if err != nil {
		t.Fatalf("read %s: %v", scopesGoldenPath, err)
	}
	var golden []providerScopes
	if err := json.Unmarshal(b, &golden); err != nil {
		t.Fatalf("parse %s: %v", scopesGoldenPath, err)
	}
	recorded := map[string]bool{}
	for _, g := range golden {
		recorded[g.Platform] = true
	}
	for p := range Providers() {
		if !recorded[string(p)] {
			t.Errorf("%s is a registered provider and is not in %s. A platform whose "+
				"scopes nobody recorded is a platform whose scope drift nobody will see.",
				p, scopesGoldenPath)
		}
	}
	for _, g := range golden {
		if _, ok := Providers()[db.Platform(g.Platform)]; !ok {
			t.Errorf("%s is in %s and is no longer a registered provider. Removing a "+
				"platform is a real change; regenerate so it lands as a diff.",
				g.Platform, scopesGoldenPath)
		}
	}
}
