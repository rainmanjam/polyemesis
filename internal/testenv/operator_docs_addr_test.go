package testenv

import (
	"regexp"
	"strings"
	"testing"
)

// operatorDocs are the pages an operator follows with a terminal open. Research
// notes and design documents are not in the list: they cite code by line on
// purpose, as evidence at a date.
var operatorDocs = []string{
	"README.md",
	"docs/INSTALL.md",
	"docs/UPGRADING.md",
	"docs/TLS.md",
	"docs/MONITORING.md",
	"docs/CONFIGURATION.md",
	"docs/TROUBLESHOOTING.md",
	"docs/QUICKSTART.md",
	"docs/FAQ.md",
	"docs/OBS.md",
}

// TestOperatorDocsCiteSymbolsNotLineNumbers: INSTALL.md cited
// `internal/config/config.go:414-406` and `scripts/install.sh:1508-1480` --
// ranges that run backwards -- and `deploy/polyemesis.service:51-57`, which had
// drifted onto the wrong comment block. A line number is true on the day it is
// written and wrong after the next edit above it, with nothing to say so. Name
// the function, the variable or the comment heading instead; those move with the
// code. Staging-readiness row 30. Prevention rung: the citation cannot be
// committed in these files at all.
func TestOperatorDocsCiteSymbolsNotLineNumbers(t *testing.T) {
	cite := regexp.MustCompile(`[A-Za-z0-9_./-]+\.(go|sh|ts|tsx|mjs|yml|yaml|service|example|ps1):[0-9]+`)
	for _, rel := range operatorDocs {
		for i, l := range strings.Split(readDoc(t, rel), "\n") {
			for _, m := range cite.FindAllString(l, -1) {
				t.Errorf("%s:%d cites %s by line number. Name the symbol instead -- a line "+
					"number goes stale with the next edit above it, silently.", rel, i+1, m)
			}
		}
	}
}

// TestDocAddrAdviceNamesTheFlagThatBeatsIt: every shipped way of running the
// server passes --addr -- deploy/polyemesis.service, install.sh's generated
// unit, and the image's CMD -- and main.go applies the flag AFTER config.yaml.
// So "set addr: \":443\" in config.yaml", which INSTALL.md, TLS.md and the
// server's own startup warning all said, changed nothing on any of them: the
// operator stayed on 8080, and INSTALL.md predicted a different failure (a
// bind refusal) from the one that happened (no change at all). Staging-readiness
// row 25.
func TestDocAddrAdviceNamesTheFlagThatBeatsIt(t *testing.T) {
	// The fact itself, read from the code, so this guard retires with it.
	mainGo := readDoc(t, "cmd/polyemesis/main.go")
	load := strings.Index(mainGo, "cfg, err := configLoaderFor(")
	override := strings.Index(mainGo, "cfg.Addr = *addr")
	if load < 0 || override < 0 || override < load {
		t.Fatal("main.go no longer applies --addr after loading config.yaml. If the flag " +
			"stopped overriding the file, the docs this guards need rewriting, not this check.")
	}
	for _, where := range []struct{ rel, text string }{
		{"deploy/polyemesis.service", "--addr :8080"},
		{"Dockerfile", `CMD ["-addr", ":8080"`},
	} {
		if !strings.Contains(readDoc(t, where.rel), where.text) {
			t.Fatalf("%s no longer passes %q. Update this guard and the docs it protects together.",
				where.rel, where.text)
		}
	}

	// Every place a doc tells the operator to put :443 in config.yaml must, close
	// by, say what overrides it.
	const window = 40
	advice := regexp.MustCompile(`addr: \\?":443"`)
	for _, rel := range []string{"docs/INSTALL.md", "docs/TLS.md", "docs/CONFIGURATION.md"} {
		lines := strings.Split(readDoc(t, rel), "\n")
		for i, l := range lines {
			if !advice.MatchString(l) {
				continue
			}
			end := i + window
			if end > len(lines) {
				end = len(lines)
			}
			if !strings.Contains(strings.Join(lines[i:end], "\n"), "--addr") {
				t.Errorf("%s:%d says addr: \":443\" and nothing in the next %d lines mentions "+
					"--addr. The shipped unit, install.sh's unit and the image all pass --addr, "+
					"which beats config.yaml -- the operator follows this and nothing changes.",
					rel, i+1, window)
			}
		}
	}
}

// TestDocTLSWarnsTheImageHealthcheckIsPlainHTTP: the image's HEALTHCHECK is
// `wget http://127.0.0.1:8080/...`. Turn on any TLS mode inside the container
// and a healthy server is marked unhealthy, and an orchestrator restarts it.
// install.sh writes a matching healthcheck into its compose file; an operator
// who mounts a config.yaml into the repository's compose file is on their own,
// and TLS.md -- the page they are reading when they do it -- did not say.
func TestDocTLSWarnsTheImageHealthcheckIsPlainHTTP(t *testing.T) {
	df := readDoc(t, "Dockerfile")
	i := strings.Index(df, "HEALTHCHECK")
	if i < 0 {
		t.Fatal("the Dockerfile has no HEALTHCHECK; update this guard")
	}
	if !strings.Contains(df[i:], "http://127.0.0.1:8080") {
		return // the image's check is no longer plain http; nothing left to warn about
	}
	tls := readDoc(t, "docs/TLS.md")
	if !strings.Contains(tls, "HEALTHCHECK") || !strings.Contains(tls, "--no-check-certificate") {
		t.Error("docs/TLS.md does not warn that the image's HEALTHCHECK is plain http, or " +
			"does not give the https replacement. Any TLS mode inside the container makes " +
			"a healthy server report unhealthy.")
	}
}
