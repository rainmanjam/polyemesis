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

// curlFailFlag matches whether a curl command line turns an HTTP error or a
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

// TestDocPrometheusExampleScrapesOverTLS: Prometheus defaults to http on the
// target's port 80. On an install that terminates TLS itself, :80 is the
// HTTP->HTTPS redirect helper, so the documented scrape sent its bearer token
// in cleartext on every scrape before being redirected -- and then failed
// verification against the self-signed certificate anyway. Staging-readiness
// row 14. The token is on the wire before anything on the server can refuse
// it, so the only place to stop this is the example people copy.
func TestDocPrometheusExampleScrapesOverTLS(t *testing.T) {
	const rel = "docs/MONITORING.md"
	doc := readDoc(t, rel)
	var block []string
	inFence, cur := false, []string{}
	for _, l := range strings.Split(doc, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			if inFence && strings.Contains(strings.Join(cur, "\n"), "scrape_configs:") &&
				strings.Contains(strings.Join(cur, "\n"), "polyemesis") {
				block = cur
				break
			}
			inFence, cur = !inFence, nil
			continue
		}
		if inFence {
			cur = append(cur, l)
		}
	}
	if block == nil {
		t.Fatalf("%s has no scrape_configs example for polyemesis; this guard would check nothing", rel)
	}
	body := strings.Join(block, "\n")
	for _, want := range []string{"scheme: https", "tls_config:", "ca_file:"} {
		if !strings.Contains(body, want) {
			t.Errorf("%s's Prometheus example has no %q. Without an explicit https scheme "+
				"Prometheus scrapes http://<target>:80, and the bearer token crosses the "+
				"network in cleartext before the redirect:\n%s", rel, want, body)
		}
	}
	if !regexp.MustCompile(`targets:\s*\[\s*'[^']+:\d+'`).MatchString(body) {
		t.Errorf("%s's Prometheus target names no port. With scheme https Prometheus "+
			"would pick 443, which is right only for some installs -- say it:\n%s", rel, body)
	}
}

// TestDocWindowsAbortIsDescribedAsFixed: #440, the intermittent Windows runtime
// abort, was traced and fixed in 0.9.0 and the issue closed on 2026-09-04. Two
// releases later INSTALL.md's platform table, its Windows notes and the release
// body release.yml publishes still called it a known unresolved defect -- and
// scripts/test-release-gates.sh REQUIRED the release body to name it, so the
// stale warning was enforced. Staging-readiness row 39. A warning about a fixed
// bug costs twice: the operator avoids a platform for a reason that is gone,
// and learns to skim the warnings that are still true.
//
// The property: every sentence that names #440 in operator-facing text says it
// is fixed. Mentioning it at all stays allowed -- someone on 0.8.x needs to
// know it exists.
func TestDocWindowsAbortIsDescribedAsFixed(t *testing.T) {
	sentenceEnd := regexp.MustCompile(`[.!?](\s|$)`)
	for _, rel := range []string{"docs/INSTALL.md", "README.md", ".github/workflows/release.yml"} {
		// Paragraphs, with their lines joined, so a sentence that wraps is
		// still one sentence.
		for _, para := range regexp.MustCompile(`\n\s*\n`).Split(readDoc(t, rel), -1) {
			joined := strings.Join(strings.Fields(para), " ")
			for _, s := range sentenceEnd.Split(joined, -1) {
				if strings.Contains(s, "#440") && !strings.Contains(s, "fixed") {
					t.Errorf("%s names #440 without saying it is fixed. It was fixed in 0.9.0 "+
						"(issue closed 2026-09-04):\n    %s", rel, s)
				}
			}
		}
	}
}

// TestDocLaunchdJobHasEveryFileItNames: INSTALL.md's launchd plist ran
// /usr/local/bin/polyemesis with -config ".../Application Support/polyemesis/
// config.yaml", and no step on the page put a binary or a config file at either
// path. An explicit -config that is missing is a refusal to start (#644, main.go's
// configLoaderFor), and KeepAlive turns that into a crash loop -- exploratory
// IU-9 followed the page literally and got exactly that. The guard reads every
// <string> path the plist hands the program and requires a shell step in the
// same section that writes it.
func TestDocLaunchdJobHasEveryFileItNames(t *testing.T) {
	const rel = "docs/INSTALL.md"
	sec := docSection(t, readDoc(t, rel), rel, "Run it at login, or at boot")

	// The fact the guard depends on: an explicit --config that is absent refuses.
	if !strings.Contains(readDoc(t, "cmd/polyemesis/main.go"), "configLoaderFor(flag.Visit)") {
		t.Fatal("main.go no longer loads the config through configLoaderFor(flag.Visit); if a " +
			"missing -config no longer refuses to start, revisit this guard.")
	}

	lines := fencedLines(sec)
	var shell []string
	for _, l := range lines {
		if !strings.Contains(l, "<") {
			shell = append(shell, l)
		}
	}
	// The plist is XML inside the section; resolve its $HOME-relative paths the
	// way the shell steps write them.
	norm := func(p string) string {
		p = strings.ReplaceAll(p, "/Users/YOU", "$HOME")
		return strings.ReplaceAll(p, `"`, "")
	}
	written := func(path string) bool {
		for _, l := range shell {
			l = norm(l)
			if (strings.Contains(l, "cp ") || strings.Contains(l, "install ") || strings.Contains(l, "> ")) &&
				strings.Contains(l, path) {
				return true
			}
		}
		return false
	}
	want := map[string]bool{}
	for _, m := range regexp.MustCompile(`<string>(/[^<]*/(polyemesis|config\.yaml))</string>`).FindAllStringSubmatch(sec, -1) {
		want[norm(m[1])] = true
	}
	if len(want) < 2 {
		t.Fatalf("expected the plist in %s to name the binary and a config.yaml; found %v -- "+
			"this guard would check nothing", rel, want)
	}
	for p := range want {
		if !written(p) {
			t.Errorf("%s's launchd plist names %s and no command in that section puts a file "+
				"there. A missing -config is a refusal to start, and KeepAlive makes it a crash loop.",
				rel, p)
		}
	}

	// The release assets carry the tag: polyemesis-<tag>-darwin-<arch>.
	for i, l := range strings.Split(readDoc(t, rel), "\n") {
		if strings.Contains(l, "polyemesis-darwin-") {
			t.Errorf("%s:%d names a darwin asset without its version; the release publishes "+
				"polyemesis-<tag>-darwin-<arch> (Makefile's release target):\n    %s",
				rel, i+1, strings.TrimSpace(l))
		}
	}
}

// TestDocServiceInstallStepsMatchTheUnitHeader: deploy/polyemesis.service opens
// with the commands that install it, and INSTALL.md's "Run it as a service"
// repeats them. They drifted: the unit gained `chmod 0750 /var/lib/polyemesis`
// (#297 -- the directory holds secret.key and the sealed stream keys) and the
// page did not, so an operator following the page got a 0755 data directory.
// Staging-readiness row 30, exploratory IU-3. Two copies of a procedure are
// held to be one copy here: every command in either must be in the other.
func TestDocServiceInstallStepsMatchTheUnitHeader(t *testing.T) {
	norm := func(l string) string {
		if i := strings.Index(l, " #"); i >= 0 {
			l = l[:i]
		}
		return strings.Join(strings.Fields(l), " ")
	}
	unit := map[string]bool{}
	for _, l := range strings.Split(readDoc(t, "deploy/polyemesis.service"), "\n") {
		if !strings.HasPrefix(l, "#") {
			break // the header ends at the first non-comment line
		}
		c := strings.TrimSpace(strings.TrimPrefix(l, "#"))
		if strings.HasPrefix(c, "sudo ") || strings.HasPrefix(c, "journalctl ") {
			unit[norm(c)] = true
		}
	}
	const rel = "docs/INSTALL.md"
	sec := docSection(t, readDoc(t, rel), rel, "Run it as a service")
	page := map[string]bool{}
	for _, l := range fencedLines(sec) {
		c := strings.TrimSpace(l)
		if strings.HasPrefix(c, "sudo ") || strings.HasPrefix(c, "journalctl ") {
			page[norm(c)] = true
		}
	}
	if len(unit) < 5 || len(page) < 5 {
		t.Fatalf("found %d commands in the unit header and %d in %s; this guard would check "+
			"almost nothing", len(unit), len(page), rel)
	}
	for c := range unit {
		if !page[c] {
			t.Errorf("deploy/polyemesis.service's install notes run %q and %s's \"Run it as a "+
				"service\" does not", c, rel)
		}
	}
	for c := range page {
		if !unit[c] {
			t.Errorf("%s's \"Run it as a service\" runs %q and deploy/polyemesis.service's "+
				"install notes do not", rel, c)
		}
	}
}

// fencedBlocks returns each fenced code block in md as its raw lines.
func fencedBlocks(md string) [][]string {
	var out [][]string
	var cur []string
	inFence := false
	for _, l := range strings.Split(md, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			if inFence {
				out = append(out, cur)
			}
			inFence, cur = !inFence, nil
			continue
		}
		if inFence {
			cur = append(cur, l)
		}
	}
	return out
}

// TestDocManualUpgradeCarriesUpdateShGuards: UPGRADING.md's manual upgrade is
// the procedure staging actually uses, and it had none of the guards update.sh
// applies (staging-readiness row 36): a date-only stamp, so a second upgrade the
// same day cp -a'd the new copy INSIDE the first; no refusal of an existing
// destination; no polyemesis.previous; no -verify-backup before the binary was
// replaced. Its Docker half ran `docker compose pull` on a compose file that
// BUILDS its image, so the "upgrade" restarted the old version (exploratory
// IU-6), wrote the backup tarball into the clone where the next build's
// `COPY . .` picks it up, and used `|| exit 1`, which closes the terminal it is
// pasted into.
func TestDocManualUpgradeCarriesUpdateShGuards(t *testing.T) {
	const rel = "docs/UPGRADING.md"
	sec := docSection(t, readDoc(t, rel), rel, "The short version")

	var binary, docker string
	for _, b := range fencedBlocks(sec) {
		body := strings.Join(b, "\n")
		// PASTE-SAFE: an `exit` at the top level of a pasted block exits the
		// operator's own shell. Every block that can refuse must run in its own.
		if strings.Contains(body, "exit") && !regexp.MustCompile(`^(sudo )?sh -eu\b.*<<'EOF'$`).MatchString(strings.TrimSpace(b[0])) {
			t.Errorf("%s: a block in \"The short version\" can `exit` but does not run in its own "+
				"`sh -eu <<'EOF'`; pasted, a refusal closes the operator's terminal:\n%s", rel, body)
		}
		// The first match of each: the rollback block after the binary
		// upgrade also stops the service and installs a binary.
		switch {
		case binary == "" && strings.Contains(body, "systemctl stop") && strings.Contains(body, "install -m"):
			binary = body
		case docker == "" && strings.Contains(body, "docker compose") && strings.Contains(body, "tar czf"):
			docker = body
		}
	}
	if binary == "" || docker == "" {
		t.Fatalf("%s: could not find both the binary and the Docker manual upgrade blocks; "+
			"this guard would check nothing", rel)
	}

	for _, want := range []struct{ text, why string }{
		{"date +%F-%H%M", "a date-only stamp makes a second same-day upgrade nest its copy inside the first"},
		{`[ -e "$dest" ]`, "an existing backup destination must be refused, not copied into"},
		{"polyemesis.previous", "the running binary is the way back and must be kept"},
		{"secret.key", "a backup without secret.key restores every destination disabled"},
		{"-verify-backup", "the copy must be opened and checked, not assumed"},
	} {
		if !strings.Contains(binary, want.text) {
			t.Errorf("%s's manual binary upgrade has no %q: %s", rel, want.text, want.why)
		}
	}
	if v, i := strings.Index(binary, "-verify-backup"), strings.Index(binary, "install -m"); v < 0 || i < v {
		t.Errorf("%s's manual binary upgrade replaces the binary before -verify-backup has "+
			"passed; a failed check must leave the old binary in place", rel)
	}

	if !strings.Contains(docker, "--build") || !strings.Contains(docker, "git pull") {
		t.Errorf("%s's Docker upgrade for a clone must `git pull` and `up -d --build`: the "+
			"repository's compose service has build:, so `pull` fetches nothing", rel)
	}
	if strings.Contains(docker, `$PWD:/backup`) {
		t.Errorf("%s's Docker upgrade writes the backup into the working directory; in a clone "+
			"the next build's `COPY . .` copies it, secret.key included, into the image", rel)
	}
}

// TestDocEveryReleaseHasAnUpgradeNote: docs/RELEASE-RUNBOOK.md requires an
// upgrade note for a changed default, and 0.9.0 changed two (the loopback
// default bind, and a missing explicit --config now refusing to start) with no
// note in UPGRADING.md; 0.10.0 had none either, and the page's banner still
// named 0.8.0 as the newest release. Staging-readiness row 30. The rule held
// here: every release in CHANGELOG.md from 0.9.0 on has a "### Upgrading to
// X.Y.Z" heading -- even when its body is "nothing to do", which is itself the
// answer an operator came to the page for.
func TestDocEveryReleaseHasAnUpgradeNote(t *testing.T) {
	released := regexp.MustCompile(`(?m)^## \[(\d+)\.(\d+)\.(\d+)\]`).FindAllStringSubmatch(readDoc(t, "CHANGELOG.md"), -1)
	if len(released) == 0 {
		t.Fatal("found no released version headings in CHANGELOG.md; this guard would check nothing")
	}
	upgrading := readDoc(t, "docs/UPGRADING.md")
	checked := 0
	for _, m := range released {
		major, minor := m[1], m[2]
		// Per-release notes start at 0.9.0; older releases are covered by the
		// topical notes (0.7.0's sealed keys, multi-source, one-port ingest...).
		if major == "0" && len(minor) == 1 && minor < "9" {
			continue
		}
		v := m[1] + "." + m[2] + "." + m[3]
		checked++
		if !regexp.MustCompile(`(?m)^### Upgrading to ` + regexp.QuoteMeta(v) + `\b`).MatchString(upgrading) {
			t.Errorf("docs/UPGRADING.md has no \"### Upgrading to %s\" note. Every release gets "+
				"one, even if it says there is nothing to do.", v)
		}
	}
	if checked == 0 {
		t.Error("no release from 0.9.0 on was found in CHANGELOG.md; this guard checked nothing")
	}
}

// TestDocProxyExamplesServeTheConsoleFromARoot: the console cannot live under a
// sub-path. Its API base is the absolute "/api/v1" (ui/src/lib/api.ts), Vite is
// built with no `base`, and the WebSocket, the assets and the /hls/ preview are
// requested by absolute path -- so behind `location /polyemesis/` every request
// goes to the other site's root. Nothing said so: not deploy/nginx.conf.example,
// not INSTALL.md, not TLS.md. Staging-readiness row 48.
func TestDocProxyExamplesServeTheConsoleFromARoot(t *testing.T) {
	// The fact, from the code, so the guard retires the day a base path works.
	if !strings.Contains(readDoc(t, "ui/src/lib/api.ts"), `const BASE = "/api/v1";`) {
		t.Skip(`ui/src/lib/api.ts no longer hard-codes BASE = "/api/v1"; if the console can now ` +
			"be served under a prefix, the sub-path warnings this guards may be obsolete")
	}
	if regexp.MustCompile(`(?m)^\s*base\s*:`).MatchString(readDoc(t, "ui/vite.config.ts")) {
		t.Skip("ui/vite.config.ts sets a base; revisit the sub-path warnings")
	}

	nginx := readDoc(t, "deploy/nginx.conf.example")
	// Every proxy_pass must sit in `location /`.
	loc := regexp.MustCompile(`(?s)location\s+(\S+)\s*\{([^}]*)\}`)
	proxied := 0
	for _, m := range loc.FindAllStringSubmatch(nginx, -1) {
		if !strings.Contains(m[2], "proxy_pass") {
			continue
		}
		proxied++
		if m[1] != "/" {
			t.Errorf("deploy/nginx.conf.example proxies polyemesis at `location %s`; the console "+
				"requests everything by absolute path and only works from `/` of its own hostname", m[1])
		}
	}
	if proxied == 0 {
		t.Fatal("deploy/nginx.conf.example has no proxy_pass location; this guard would check nothing")
	}

	for _, where := range []struct{ rel, text string }{
		{"deploy/nginx.conf.example", nginx},
		{"docs/INSTALL.md", docSection(t, readDoc(t, "docs/INSTALL.md"), "docs/INSTALL.md", "Behind a reverse proxy")},
		{"docs/TLS.md", docSection(t, readDoc(t, "docs/TLS.md"), "docs/TLS.md", "Behind a reverse proxy")},
	} {
		if !strings.Contains(strings.ToLower(where.text), "sub-path") {
			t.Errorf("%s's reverse-proxy guidance does not say the console cannot be served from "+
				"a sub-path. An operator will try `location /polyemesis/` and get a blank page.", where.rel)
		}
	}
}
