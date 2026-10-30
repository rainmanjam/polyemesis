package testenv

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// THE DEMO SEEDER NAMES THE PROGRAMME IT ASKS ABOUT.
//
// scripts/demo-seed.sh is how the site and docs screenshots are captured
// (ui/e2e/capture.config.ts says to run it, finding S5). Its driver waited for
// the probe with a bare GET /source, and an install running several programmes
// answers that with 400 source_required rather than guess which one is meant.
// The fixture always seeds several, so every run died at
//
//	GET /source: 400 ... must say which one it is for: add "?source=<id>"
//
// before a destination existed or a single shot was taken -- and nothing ran
// the harness except a person about to refresh the screenshots.
//
// The driver is //go:build ignore, so no unit test can call it; this reads the
// source instead. Every GET of /source in it must carry ?source=.
func TestDemoSeedDriverScopesEverySourceRead(t *testing.T) {
	root := repoRootFromTest(t)
	b, err := os.ReadFile(filepath.Join(root, "scripts", "demo_seed_driver.go"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)

	bare := regexp.MustCompile(`"/source"`)
	for i, line := range strings.Split(src, "\n") {
		if bare.MatchString(line) && !strings.HasPrefix(strings.TrimSpace(line), "//") {
			t.Errorf("scripts/demo_seed_driver.go:%d reads /source without ?source=: %s\n"+
				"the fixture seeds several programmes, so this answers 400 source_required",
				i+1, strings.TrimSpace(line))
		}
	}

	// POSITIVE CONTROL: the driver does read /source, scoped. If this stops
	// matching, the read moved and the check above proves nothing.
	if !strings.Contains(src, `"/source?source=`) {
		t.Error(`scripts/demo_seed_driver.go has no "/source?source=" read; ` +
			"the probe wait moved, so update this guard to follow it")
	}

	// The premise: the fixture really does seed more than one programme. With
	// one, a bare /source would be answered and this guard would be moot.
	fx, err := os.ReadFile(filepath.Join(root, "scripts", "demo-seed.fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Sources []json.RawMessage `json:"sources"`
	}
	if err := json.Unmarshal(fx, &parsed); err != nil {
		t.Fatalf("demo-seed.fixture.json: %v", err)
	}
	if len(parsed.Sources) < 2 {
		t.Fatalf("demo-seed.fixture.json seeds %d programme(s); this guard assumes several", len(parsed.Sources))
	}
}
