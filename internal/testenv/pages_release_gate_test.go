package testenv_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"gopkg.in/yaml.v3"
)

// pages.yml redeployed the site on EVERY completed release run -- failed tag
// runs, scheduled rehearsals, dry-run dispatches -- so the site could announce
// a version that was never published (poka-yoke audit 2026-11-04). Its first
// job is gated on a successful tag-push release; this evaluates that gate for
// each kind of run rather than matching its text.
func TestThePagesDeployRunsAfterOnlyAPublishedRelease(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), pagesWorkflowPath))
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		Jobs map[string]struct {
			If    string `yaml:"if"`
			Needs any    `yaml:"needs"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatal(err)
	}
	gate := wf.Jobs["site-scope"].If
	if gate == "" {
		t.Fatal("pages.yml's site-scope job has no if: every completed release run redeploys the site")
	}
	// Every other job must depend on site-scope, or skipping it gates nothing.
	for name, j := range wf.Jobs {
		if name == "site-scope" {
			continue
		}
		if !strings.Contains(strings.TrimSpace(func() string {
			switch n := j.Needs.(type) {
			case string:
				return n
			case []any:
				var parts []string
				for _, v := range n {
					parts = append(parts, v.(string))
				}
				return strings.Join(parts, " ")
			}
			return ""
		}()), "site-scope") {
			t.Errorf("job %s does not need site-scope, so the release gate does not reach it", name)
		}
	}

	for _, tc := range []struct {
		name               string
		event, conc, runEv string
		want               bool
	}{
		{"a pull request builds", "pull_request", "", "", true},
		{"a push to main builds", "push", "", "", true},
		{"a manual run builds", "workflow_dispatch", "", "", true},
		{"a successful tag release redeploys", "workflow_run", "success", "push", true},
		{"a failed tag release does not", "workflow_run", "failure", "push", false},
		{"a scheduled rehearsal does not", "workflow_run", "success", "schedule", false},
		{"a dry-run dispatch does not", "workflow_run", "success", "workflow_dispatch", false},
		{"a cancelled run does not", "workflow_run", "cancelled", "push", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := map[string]string{
				"github.event_name":                    tc.event,
				"github.event.workflow_run.conclusion": tc.conc,
				"github.event.workflow_run.event":      tc.runEv,
			}
			got, err := evalGate(gate, ctx)
			if err != nil {
				t.Fatalf("cannot evaluate %q: %v", gate, err)
			}
			if got != tc.want {
				t.Errorf("gate = %v for %s, want %v", got, tc.name, tc.want)
			}
		})
	}
}

// evalGate evaluates the subset of GitHub's expression syntax the gate uses:
// identifiers, 'string' literals, ==, !=, &&, || and parentheses. Anything
// else is an error, so a gate rewritten past this subset fails loudly.
func evalGate(expr string, ctx map[string]string) (bool, error) {
	p := &gateParser{toks: gateTokens(expr), ctx: ctx}
	v, err := p.or()
	if err == nil && p.i != len(p.toks) {
		err = errUnexpected(p.toks[p.i])
	}
	return v, err
}

type gateParser struct {
	toks []string
	i    int
	ctx  map[string]string
}

type errUnexpected string

func (e errUnexpected) Error() string { return "unexpected token " + string(e) }

func (p *gateParser) peek() string {
	if p.i < len(p.toks) {
		return p.toks[p.i]
	}
	return ""
}

func (p *gateParser) or() (bool, error) {
	v, err := p.and()
	for err == nil && p.peek() == "||" {
		p.i++
		var r bool
		r, err = p.and()
		v = v || r
	}
	return v, err
}

func (p *gateParser) and() (bool, error) {
	v, err := p.cmp()
	for err == nil && p.peek() == "&&" {
		p.i++
		var r bool
		r, err = p.cmp()
		v = v && r
	}
	return v, err
}

func (p *gateParser) cmp() (bool, error) {
	if p.peek() == "(" {
		p.i++
		v, err := p.or()
		if err != nil {
			return false, err
		}
		if p.peek() != ")" {
			return false, errUnexpected(p.peek())
		}
		p.i++
		return v, nil
	}
	l := p.value()
	op := p.peek()
	if op != "==" && op != "!=" {
		return false, errUnexpected(op)
	}
	p.i++
	r := p.value()
	return (l == r) == (op == "=="), nil
}

func (p *gateParser) value() string {
	tok := p.peek()
	p.i++
	if strings.HasPrefix(tok, "'") {
		return strings.Trim(tok, "'")
	}
	return p.ctx[tok]
}

func gateTokens(s string) []string {
	var out []string
	for i := 0; i < len(s); {
		c := rune(s[i])
		switch {
		case unicode.IsSpace(c):
			i++
		case c == '(' || c == ')':
			out = append(out, string(c))
			i++
		case c == '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return append(out, s[i:])
			}
			out = append(out, s[i:i+j+2])
			i += j + 2
		case strings.HasPrefix(s[i:], "==") || strings.HasPrefix(s[i:], "!=") ||
			strings.HasPrefix(s[i:], "&&") || strings.HasPrefix(s[i:], "||"):
			out = append(out, s[i:i+2])
			i += 2
		default:
			j := i
			for j < len(s) && (s[j] == '.' || s[j] == '_' || unicode.IsLetter(rune(s[j])) || unicode.IsDigit(rune(s[j]))) {
				j++
			}
			if j == i {
				return append(out, s[i:i+1])
			}
			out = append(out, s[i:j])
			i = j
		}
	}
	return out
}
