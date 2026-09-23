package testenv

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// THE COMMANDS AN OPERATOR PASTES ARE CODE, AND THEY WERE NEVER RUN.
//
// Exploratory testing of v0.10.0 pasted every command in docs/INSTALL.md and
// docs/UPGRADING.md into real installs, and the worst failures were not wrong
// facts but commands that answer confidently when they did not work:
//
//   - `curl -s localhost:8080/api/v1/destinations | grep -o keyUnreadable | wc -l`
//     on an install.sh install, which serves HTTPS on :443. curl fails, prints
//     nothing, and `wc -l` prints 0 -- the all-clear, for the one failure that
//     check exists to find. Confirmed with a positive control: keys that could
//     not be read, and the documented command still said 0.
//   - `curl -s http://localhost:8080/api/v1/health` as "Verifying the install",
//     which fails on every default install.sh install and on a hand-installed
//     unit using config.example.yaml (tls.mode auto resolves to selfsigned).
//
// Each guard below reads the markdown and pins a property of the COMMAND, not
// its wording, so a rewrite that keeps the property passes and one that loses
// it does not. They are named so ci.yml's docs-only `-run` pattern picks them
// up: a documentation-only pull request is exactly the change that breaks them.

// readDoc returns a repository file as a string.
func readDoc(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRootFromTest(t), rel))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(b)
}

// docSection returns the body of the markdown section whose heading text starts
// with title, up to the next heading of the same or a higher level. Fatal when
// absent: a guard that cannot find its section would otherwise pass by reading
// nothing.
func docSection(t *testing.T, doc, rel, title string) string {
	t.Helper()
	lines := strings.Split(doc, "\n")
	start, level := -1, 0
	inFence := false
	for i, l := range lines {
		if strings.HasPrefix(l, "```") {
			inFence = !inFence
		}
		if inFence {
			continue
		}
		m := headingRE.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		if start >= 0 && len(m[1]) <= level {
			return strings.Join(lines[start:i], "\n")
		}
		if start < 0 && strings.HasPrefix(m[2], title) {
			start, level = i, len(m[1])
		}
	}
	if start < 0 {
		t.Fatalf("%s has no section headed %q. If it was renamed, update this guard in the "+
			"same commit -- it would otherwise check nothing.", rel, title)
	}
	return strings.Join(lines[start:], "\n")
}

var headingRE = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)

// fencedLines returns every line inside a fenced code block in md, with shell
// continuations joined so one command is one string.
func fencedLines(md string) []string {
	var out []string
	inFence := false
	pending := ""
	for _, l := range strings.Split(md, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			inFence = !inFence
			pending = ""
			continue
		}
		if !inFence {
			continue
		}
		if strings.HasSuffix(l, `\`) {
			pending += strings.TrimSuffix(l, `\`) + " "
			continue
		}
		out = append(out, pending+l)
		pending = ""
	}
	return out
}

// curlFailsLoudly reports whether a curl command line turns an HTTP error or a
// refused connection into a non-zero exit with a message (-f / --fail, and -S
// so -s does not also silence the error).
var curlFailFlag = regexp.MustCompile(`(^|\s)(-[a-zA-Z]*f[a-zA-Z]*|--fail)(\s|$)`)

func TestDocVerifyCommandsFailLoudlyAndNameTheirURL(t *testing.T) {
	cases := []struct{ rel, section string }{
		{"docs/INSTALL.md", "Verifying the install"},
		{"docs/UPGRADING.md", "Verifying an upgrade"},
	}
	for _, c := range cases {
		sec := docSection(t, readDoc(t, c.rel), c.rel, c.section)
		curls := 0
		for _, l := range fencedLines(sec) {
			if !strings.Contains(l, "curl ") {
				continue
			}
			curls++
			if !curlFailFlag.MatchString(l) {
				t.Errorf("%s, %q: this curl has no -f, so a refused connection or an error "+
					"status prints nothing and exits 0 -- and the command after it reads that "+
					"silence as an answer:\n    %s", c.rel, c.section, strings.TrimSpace(l))
			}
			// ONE ADDRESS DOES NOT FIT EVERY INSTALL. install.sh defaults to
			// selfsigned TLS on :443, a hand unit with config.example.yaml serves
			// HTTPS on :8080, and a bare binary serves plain HTTP on :8080. A
			// literal address is right for one of those and silently wrong for
			// the others, so the command must take the operator's own.
			if strings.Contains(l, "localhost:") || strings.Contains(l, "127.0.0.1:") ||
				strings.Contains(l, "http://localhost") {
				t.Errorf("%s, %q: this curl hard-codes an address, which is wrong for at "+
					"least one install shape. Use \"$POLYEMESIS_URL\" and say how to set it:\n    %s",
					c.rel, c.section, strings.TrimSpace(l))
			}
			if !strings.Contains(l, "POLYEMESIS_URL") {
				t.Errorf("%s, %q: this curl does not use $POLYEMESIS_URL:\n    %s",
					c.rel, c.section, strings.TrimSpace(l))
			}
		}
		if curls == 0 {
			t.Errorf("%s, %q has no curl command at all; this guard would pass by reading nothing",
				c.rel, c.section)
		}
	}
}

// TestDocKeyUnreadableCountReadsTheWireShape pins the one check that exists to
// catch a restore without secret.key. Counting the WORD with grep | wc -l says 0
// for every failure that produces no body -- wrong scheme, wrong port, a 401 --
// and it is omitempty, so a healthy install and an unreachable one agree.
func TestDocKeyUnreadableCountReadsTheWireShape(t *testing.T) {
	// The shape the jq path below depends on, read from the code that writes it,
	// so the doc cannot be "fixed" to a path the server never produces.
	handlers := readDoc(t, "internal/api/handlers.go")
	if !strings.Contains(handlers, `item := map[string]any{"destination": shown}`) {
		t.Fatal("GET /destinations no longer wraps each row as {\"destination\": ...}. " +
			"The documented keyUnreadable count reads .destination.keyUnreadable; update " +
			"docs/UPGRADING.md and this guard together.")
	}
	if !strings.Contains(readDoc(t, "internal/db/destinations.go"), "`json:\"keyUnreadable,omitempty\"`") {
		t.Fatal("db.Destination.KeyUnreadable is no longer serialised as keyUnreadable; " +
			"update docs/UPGRADING.md and this guard together.")
	}

	found := 0
	for _, rel := range []string{"docs/UPGRADING.md", "docs/INSTALL.md", "docs/TROUBLESHOOTING.md"} {
		for _, l := range fencedLines(readDoc(t, rel)) {
			if !strings.Contains(l, "keyUnreadable") {
				continue
			}
			found++
			if strings.Contains(l, "wc -l") || strings.Contains(l, "grep") {
				t.Errorf("%s counts keyUnreadable by grepping the response text. A response "+
					"that never arrived counts 0, which reads as all-clear:\n    %s",
					rel, strings.TrimSpace(l))
			}
			if !strings.Contains(l, ".destination.keyUnreadable") {
				t.Errorf("%s reads keyUnreadable at a path GET /destinations does not produce "+
					"(rows are {\"destination\": {...}}), so the count is always 0:\n    %s",
					rel, strings.TrimSpace(l))
			}
		}
	}
	if found == 0 {
		t.Error("no operator doc carries a keyUnreadable check any more. docs/UPGRADING.md's " +
			"\"Verifying an upgrade\" is where an operator is told to run it.")
	}
}
