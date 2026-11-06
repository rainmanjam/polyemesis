package testenv_test

import (
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A push to main and a pull request must agree on which files are the site's.
// The push trigger lists them as `paths:`; the PR scope is a grep regex in the
// site-scope job, because a workflow-level filter would make the required check
// absent. Two lists for one fact: this holds them to the same set, so a file
// added to one and not the other (the README, go.mod) fails here instead of
// merging green and building red on the next unrelated web change.
func TestPagesPushPathsAndPullRequestScopeAgree(t *testing.T) {
	text := readRepoFile(t, ".github", "workflows", "pages.yml")
	var wf struct {
		On struct {
			Push struct {
				Paths []string `yaml:"paths"`
			} `yaml:"push"`
		} `yaml:"on"`
	}
	if err := yaml.Unmarshal([]byte(text), &wf); err != nil {
		t.Fatal(err)
	}
	paths := wf.On.Push.Paths
	m := regexp.MustCompile(`grep -qE '(\^\(.*\))' <<<"\$CHANGED"`).FindStringSubmatch(text)
	if len(paths) == 0 || m == nil {
		t.Fatalf("cannot find push.paths (%d) or the site-scope regex (%v)", len(paths), m != nil)
	}
	scope := regexp.MustCompile(m[1])

	// One path per leaf alternative: every `|` adds one, including inside the
	// group that shares the integrations/streamdeck/ prefix.
	alts := strings.Count(m[1], "|") + 1
	if alts != len(paths) {
		t.Errorf("push.paths lists %d entries, the PR scope regex has %d alternatives", len(paths), alts)
	}
	for _, p := range paths {
		sample := strings.ReplaceAll(p, "**", "x")
		if !scope.MatchString(sample) {
			t.Errorf("push.paths has %q, which the PR scope regex does not match", p)
		}
	}
}
