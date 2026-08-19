package testenv_test

// Guards on the Cloudflare Pages deploy for web/.
//
// The deploy is spread across five files that no compiler, linter or type
// checker relates to each other: .github/workflows/pages.yml, web/wrangler.toml,
// web/astro.config.mjs, web/public/_headers and docs/SITE-DEPLOY.md. Each is
// individually valid while saying something the others contradict, and every
// contradiction below fails the same way — silently, in production, with the
// site still serving.
//
// These are checks on configuration, and configuration checks are exactly where
// this repository has shipped vacuous guards before. So each one is written to
// fail rather than pass when the thing it reads is missing: no file here is
// optional, and a parse that finds nothing is a failure, not a pass.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// pagesWorkflowPath is the deploy workflow, relative to the repo root.
var pagesWorkflowPath = filepath.Join(".github", "workflows", "pages.yml")

// pagesWorkflow is the slice of the workflow schema these rules need. Named
// distinctly from workflowtimeout_test.go's `workflow`, which is the same
// package and a different slice of the same files.
type pagesWorkflow struct {
	Jobs map[string]struct {
		Defaults struct {
			Run struct {
				WorkingDirectory string `yaml:"working-directory"`
			} `yaml:"run"`
		} `yaml:"defaults"`
		Steps []pagesStep `yaml:"steps"`
	} `yaml:"jobs"`
}

type pagesStep struct {
	Name string `yaml:"name"`
	ID   string `yaml:"id"`
	If   string `yaml:"if"`
	Run  string `yaml:"run"`
	Uses string `yaml:"uses"`
}

// label is how a failure names a step: what a reader would search the file for.
func (s pagesStep) label() string {
	switch {
	case s.Name != "":
		return s.Name
	case s.ID != "":
		return "id: " + s.ID
	default:
		return "uses: " + s.Uses
	}
}

func readRepoFile(t *testing.T, parts ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{repoRoot(t)}, parts...)...)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(b) == 0 {
		t.Fatalf("%s is empty; every assertion that reads it would pass by "+
			"examining nothing", path)
	}
	return string(b)
}

// pagesDeploySteps returns the deploy job's steps and its default working
// directory. It fails, rather than returning nothing, if the workflow has no
// deploy job — which is the shape that would make every caller vacuous.
func pagesDeploySteps(t *testing.T) ([]pagesStep, string) {
	t.Helper()
	var wf pagesWorkflow
	if err := yaml.Unmarshal([]byte(readRepoFile(t, ".github", "workflows", "pages.yml")), &wf); err != nil {
		t.Fatalf("parse %s: %v", pagesWorkflowPath, err)
	}
	job, ok := wf.Jobs["deploy"]
	if !ok {
		names := make([]string, 0, len(wf.Jobs))
		for n := range wf.Jobs {
			names = append(names, n)
		}
		t.Fatalf("%s has no `deploy` job; it has %v. Every rule in this file "+
			"reads that job, so renaming it turns them all into no-ops rather "+
			"than into failures.", pagesWorkflowPath, names)
	}
	if len(job.Steps) == 0 {
		t.Fatalf("%s :: job deploy has no steps", pagesWorkflowPath)
	}
	return job.Steps, job.Defaults.Run.WorkingDirectory
}

// ---------------------------------------------------------------- rule one

// publishCommand is matched against the step body, and it is the full command
// rather than the word "deploy" on purpose: the run block also CONTAINS the
// string "wrangler pages deploy" inside a comment explaining why it no longer
// uses it, and a looser match would find the comment and call it the publish
// step.
const publishCommand = "npx --no-install wrangler deploy"

// The Workers form. This read `pages_build_output_dir` until Cloudflare stopped
// offering Pages project creation; the field it replaces served the identical
// purpose, so the assertions below are unchanged in substance.
var pagesOutputDirRE = regexp.MustCompile(`(?ms)^\s*\[assets\].*?^\s*directory\s*=\s*"([^"]+)"`)

// checkBuildDistRE finds the directory web/scripts/check-build.mjs asserts
// against: `new URL("../dist/", import.meta.url)`.
var checkBuildDistRE = regexp.MustCompile(`new URL\("\.\./([^/"]+)/"`)

// normaliseDir strips a leading ./ and a trailing / so "./dist", "dist/" and
// "dist" compare equal.
func normaliseDir(s string) string {
	return strings.Trim(strings.TrimPrefix(strings.TrimSpace(s), "./"), "/")
}

// TestThePagesDeployPublishesTheDirectoryTheSiteBuildsInto is the coupling
// between what the build writes and what the deploy uploads.
//
// Three files name that directory and nothing relates them:
//
//   - web/wrangler.toml says which directory wrangler uploads.
//   - web/astro.config.mjs decides where astro writes, by NOT setting outDir and
//     therefore taking Astro's default of ./dist.
//   - web/scripts/check-build.mjs asserts about files under ../dist/, and it is
//     the only one of the three that is EXECUTED — `npm run build` runs it, so
//     it is a witness to where the build really landed rather than a second
//     declaration of intent.
//
// Not every way of getting this wrong is silent, and it is worth being exact
// about which ones this buys. Point wrangler at a directory that does not exist
// and it refuses to deploy: loud, late, but unmistakable. Point it at one that
// DOES exist and is not the build output -- a directory left by an earlier
// layout, or one that stops being written to the day somebody sets outDir -- and
// the deploy succeeds and promotes the wrong tree. Nothing distinguishes that
// from a good deploy except opening the site. This is the check for that case,
// and it runs at review time rather than at deploy time.
func TestThePagesDeployPublishesTheDirectoryTheSiteBuildsInto(t *testing.T) {
	wrangler := readRepoFile(t, "web", "wrangler.toml")
	m := pagesOutputDirRE.FindStringSubmatch(wrangler)
	if m == nil {
		t.Fatalf("web/wrangler.toml declares no [assets] directory. Without "+
			"it wrangler treats the file as local-development configuration only, "+
			"warns, and deploys nothing -- and this test would have nothing to "+
			"compare.\n%s", wrangler)
	}
	deployed := normaliseDir(m[1])

	checkBuild := readRepoFile(t, "web", "scripts", "check-build.mjs")
	cm := checkBuildDistRE.FindStringSubmatch(checkBuild)
	if cm == nil {
		t.Fatalf("web/scripts/check-build.mjs no longer resolves its DIST with " +
			"`new URL(\"../<dir>/\", import.meta.url)`. That expression is this " +
			"test's only executed witness of where the build actually lands; " +
			"without it the comparison below is two declarations agreeing with " +
			"each other.")
	}
	built := normaliseDir(cm[1])

	if deployed != built {
		t.Errorf("wrangler uploads web/%s, and the build checks that run inside "+
			"`npm run build` assert against web/%s.\n"+
			"        If web/%[1]s happens to exist -- left over from an earlier "+
			"layout, or stale because the build stopped writing there -- wrangler "+
			"uploads it and reports success, and the site goes live serving the "+
			"wrong tree on the domain #143 was waiting to advertise.\n"+
			"        Change pages_build_output_dir in web/wrangler.toml, or "+
			"change where the build writes; the two have to be one directory.",
			deployed, built)
	}

	// Astro's default outDir is ./dist. wrangler.toml hard-codes a directory, so
	// an outDir override in the config would move the build out from under it.
	if astro := readRepoFile(t, "web", "astro.config.mjs"); strings.Contains(astro, "outDir") {
		t.Errorf("web/astro.config.mjs sets outDir. web/wrangler.toml hard-codes "+
			"%q and takes Astro's default on trust; an override there moves the "+
			"build without moving the upload. Set both or neither.", m[1])
	}

	// ./dist in wrangler.toml is relative to wrangler.toml, i.e. to web/. It
	// only resolves to web/dist because the job runs there.
	steps, workdir := pagesDeploySteps(t)
	_ = steps
	if workdir != "web" {
		t.Errorf("the deploy job's default working-directory is %q, not \"web\".\n"+
			"        pages_build_output_dir in web/wrangler.toml is relative to "+
			"that file, and wrangler is invoked with no directory argument, so "+
			"the job has to be standing in web/ for %q to mean web/%s.",
			workdir, m[1], deployed)
	}
}

// ---------------------------------------------------------------- rule two

// TestThePagesPublishIsGatedOnACredentialAndTheBuildIsNot is the shape that lets
// this workflow merge before the maintainer has published anything.
//
// #143 has been blocked for three probes on a deploy that does not exist.
// Merging one that goes red on every push to main until somebody adds a secret
// is not better: a permanently-red check on main is a check people learn to
// scroll past, and it is the same free pass a t.Skip buys, spelled in YAML.
//
// So: the publish step must be conditioned on a gate step's output, and the
// build must NOT be. The second half is not decoration. If the build is gated
// too, then before the secret exists this workflow does nothing at all, and web/
// -- which has no other CI anywhere in the repository -- goes on being checked
// only on somebody's laptop.
func TestThePagesPublishIsGatedOnACredentialAndTheBuildIsNot(t *testing.T) {
	steps, _ := pagesDeploySteps(t)

	var publish, build, gate *pagesStep
	for i := range steps {
		s := &steps[i]
		switch {
		case strings.Contains(s.Run, publishCommand):
			if publish != nil {
				t.Fatalf("two steps run `"+publishCommand+"` (%q and %q); this rule "+
					"checks one gate and would leave the other unexamined.",
					publish.label(), s.label())
			}
			publish = s
		case strings.Contains(s.Run, "npm run build"):
			build = s
		}
		if s.ID == "gate" {
			gate = s
		}
	}

	if publish == nil {
		t.Fatalf("no step in %s runs `"+publishCommand+"`. A workflow that publishes "+
			"nothing satisfies every gate trivially, which is precisely the "+
			"vacuous pass this file is written against.", pagesWorkflowPath)
	}
	if gate == nil {
		t.Fatalf("no step in %s has `id: gate`. The publish condition below "+
			"reads steps.gate.outputs, and a missing step makes that expression "+
			"evaluate to empty -- the condition is then permanently false and "+
			"the deploy silently never runs.", pagesWorkflowPath)
	}
	if !strings.Contains(gate.Run, "GITHUB_OUTPUT") || !strings.Contains(gate.Run, "ready=") {
		t.Errorf("the `gate` step does not write a `ready=` value to "+
			"$GITHUB_OUTPUT, so steps.gate.outputs.ready is always empty and the "+
			"publish step below can never fire.\n        run:\n%s", gate.Run)
	}
	if !strings.Contains(gate.Run, "CLOUDFLARE_API_TOKEN") &&
		!strings.Contains(gate.Run, "CF_API_TOKEN") {
		t.Errorf("the `gate` step does not look at the Cloudflare token at all. " +
			"Then it is not a gate on the credential, it is a constant, and the " +
			"publish step's condition tells you nothing about whether a deploy " +
			"can succeed.")
	}

	if !strings.Contains(publish.If, "steps.gate.outputs") {
		t.Errorf("the publish step %q has condition %q, which does not consult "+
			"the gate.\n"+
			"        Without it every push to main runs wrangler with an empty "+
			"CLOUDFLARE_API_TOKEN and fails. A check that is red on main for a "+
			"credential nobody has added yet is a check people learn to scroll "+
			"past -- and #143 is blocked behind exactly the kind of signal "+
			"nobody reads any more.", publish.label(), publish.If)
	}
	if !strings.Contains(publish.If, "refs/heads/main") {
		t.Errorf("the publish step %q has condition %q, which does not require "+
			"refs/heads/main. This workflow also runs on pull_request, so "+
			"without that half a PR branch publishes itself to the production "+
			"site.", publish.label(), publish.If)
	}

	if build == nil {
		t.Fatalf("no step in %s runs `npm run build`. That command is `astro "+
			"check && astro build && scripts/check-build.mjs`, and web/ has no "+
			"other CI in this repository -- dropping it here means the site is "+
			"checked nowhere.", pagesWorkflowPath)
	}
	if build.If != "" {
		t.Errorf("the build step %q is conditioned on %q.\n"+
			"        Gating the build on the deploy credential means that until "+
			"a secret is added this workflow runs `astro check`, the link "+
			"resolution pass and the built-CSS assertions on nothing at all. "+
			"Those checks are the reason this file is worth merging BEFORE the "+
			"gate opens.", build.label(), build.If)
	}
}

// ---------------------------------------------------------------- rule three

// nginxTokens splits an nginx directive body into tokens, treating a
// double-quoted run as one token however much whitespace it contains.
func nginxTokens(s string) []string {
	var out []string
	var cur strings.Builder
	quoted, has := false, false
	flush := func() {
		if has {
			out = append(out, cur.String())
			cur.Reset()
			has = false
		}
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"':
			quoted = !quoted
			has = true
		case !quoted && (c == ' ' || c == '\t' || c == '\n' || c == '\r'):
			flush()
		default:
			cur.WriteByte(c)
			has = true
		}
	}
	flush()
	return out
}

var collapseWS = regexp.MustCompile(`\s+`)

func collapse(s string) string { return collapseWS.ReplaceAllString(strings.TrimSpace(s), " ") }

// nginxAddHeaders parses `add_header Name value [always];` out of an nginx
// snippet, skipping comment lines, and returns name -> value.
func nginxAddHeaders(t *testing.T, src string) map[string]string {
	t.Helper()

	var body strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		body.WriteString(line)
		body.WriteString("\n")
	}
	s := body.String()

	out := map[string]string{}
	for {
		i := strings.Index(s, "add_header")
		if i < 0 {
			break
		}
		s = s[i+len("add_header"):]
		// Consume to the first semicolon that is not inside quotes.
		quoted, end := false, len(s)
		for j := 0; j < len(s); j++ {
			if s[j] == '"' {
				quoted = !quoted
			}
			if s[j] == ';' && !quoted {
				end = j
				break
			}
		}
		tokens := nginxTokens(s[:end])
		s = s[end:]
		if len(tokens) < 2 {
			t.Fatalf("could not parse an add_header directive out of %q", s[:end])
		}
		if tokens[len(tokens)-1] == "always" {
			tokens = tokens[:len(tokens)-1]
		}
		out[tokens[0]] = collapse(strings.Join(tokens[1:], " "))
	}
	return out
}

// pagesHeaders parses a Cloudflare Pages `_headers` file into name -> value for
// the rules that apply to every path, which is the `/*` block.
func pagesHeaders(src string) map[string]string {
	out := map[string]string{}
	inCatchAll := false
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// A path rule starts at column zero; a header is indented under it.
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			inCatchAll = trimmed == "/*"
			continue
		}
		if !inCatchAll {
			continue
		}
		name, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		out[strings.TrimSpace(name)] = collapse(value)
	}
	return out
}

// TestThePagesHeadersRestateEverySecurityHeaderTheNginxConfigDeclares is the one
// that catches the silent half of changing hosting providers.
//
// web/nginx-security-headers.conf exists because of a real defect: nginx's
// add_header is not additive across levels, so locations that set Cache-Control
// were serving JavaScript and fonts with no X-Content-Type-Options and no CSP
// while the HTML beside them had both. Cloudflare Pages makes that failure total
// rather than partial -- it does not read nginx configuration at all, so on
// Pages the entire set is absent unless web/public/_headers restates it. The
// site looks identical either way. Nothing in a build, a link check or a
// screenshot notices.
//
// Both names and VALUES are compared. A CSP whose frame-ancestors quietly became
// 'self' is a header that is present, spelled correctly, and no longer doing its
// job -- and a name-only check would call that a pass.
func TestThePagesHeadersRestateEverySecurityHeaderTheNginxConfigDeclares(t *testing.T) {
	declared := nginxAddHeaders(t, readRepoFile(t, "web", "nginx-security-headers.conf"))
	if len(declared) == 0 {
		t.Fatalf("parsed no add_header directives out of " +
			"web/nginx-security-headers.conf. An empty expectation is a test " +
			"that passes by comparing nothing against nothing.")
	}
	// The set this repository has argued for in writing. Named here so that
	// deleting a header from the nginx file -- which would otherwise shrink the
	// expectation and keep this test green -- fails instead.
	for _, want := range []string{
		"X-Content-Type-Options",
		"Referrer-Policy",
		"Content-Security-Policy",
	} {
		if _, ok := declared[want]; !ok {
			t.Fatalf("web/nginx-security-headers.conf no longer declares %s. If "+
				"that removal is deliberate, remove it from this list too and "+
				"say why; leaving the list to follow the file means a header "+
				"can be dropped from both and nothing objects.", want)
		}
	}

	served := pagesHeaders(readRepoFile(t, "web", "public", "_headers"))
	if len(served) == 0 {
		t.Fatalf("web/public/_headers has no `/*` block, so it sets nothing on " +
			"the pages people actually load.")
	}

	for name, want := range declared {
		got, ok := served[name]
		if !ok {
			t.Errorf("nginx declares %s: %s, and web/public/_headers does not "+
				"set it.\n"+
				"        Cloudflare Pages does not read nginx configuration. On "+
				"Pages this header is simply absent, the site serves normally, "+
				"and nothing in the build notices.", name, want)
			continue
		}
		if got != want {
			t.Errorf("%s disagrees between the two hosting paths:\n"+
				"          nginx : %s\n"+
				"          Pages : %s\n"+
				"        A header that is present and weakened is worse than one "+
				"that is missing, because it reads as covered.", name, want, got)
		}
	}
}

// ---------------------------------------------------------------- rule four

var astroSiteRE = regexp.MustCompile(`site:\s*"https://([^/"]+)"`)
var wranglerNameRE = regexp.MustCompile(`(?m)^\s*name\s*=\s*"([^"]+)"`)
var pagesDevRE = regexp.MustCompile(`([a-z0-9-]+)\.pages\.dev`)

// TestTheDocumentedDNSRecordsNameTheHostsTheSiteIsBuiltFor keeps the one
// instruction only a human can carry out honest.
//
// docs/SITE-DEPLOY.md is the whole deliverable for the maintainer: it is where
// the DNS records are written down, and a DNS record is the one part of this
// that no test, workflow or build can perform. So the records have to name the
// host the site is actually built for -- astro.config.mjs's `site` is baked into
// every canonical URL, og:url and schema.org url by Base.astro -- and they have
// to point at the Pages project the workflow actually deploys to.
//
// Both halves have gone wrong in this repository's own history in the general
// form: #143's premise is a repository field pointing at a host nothing serves.
func TestTheDocumentedDNSRecordsNameTheHostsTheSiteIsBuiltFor(t *testing.T) {
	m := astroSiteRE.FindStringSubmatch(readRepoFile(t, "web", "astro.config.mjs"))
	if m == nil {
		t.Fatalf("web/astro.config.mjs declares no `site: \"https://...\"`. " +
			"Without it Astro emits no canonical URLs and @astrojs/sitemap has " +
			"no origin -- and this test has no host to look for.")
	}
	apex := m[1]

	doc := readRepoFile(t, "docs", "SITE-DEPLOY.md")
	for _, host := range []string{apex, "www." + apex} {
		if !strings.Contains(doc, "| `"+host+"` |") && !strings.Contains(doc, "`"+host+"`") {
			t.Errorf("docs/SITE-DEPLOY.md never mentions %s, and astro.config.mjs "+
				"builds the site for it. A deploy guide that omits a hostname the "+
				"pages already claim as canonical leaves that hostname "+
				"unconfigured -- which is #143's blocker exactly: a name nothing "+
				"answers for.", host)
		}
	}

	wm := wranglerNameRE.FindStringSubmatch(readRepoFile(t, "web", "wrangler.toml"))
	if wm == nil {
		t.Fatalf("web/wrangler.toml declares no project `name`; wrangler cannot " +
			"deploy without one.")
	}
	wantTarget := wm[1] + ".pages.dev"

	found := pagesDevRE.FindAllString(doc, -1)
	if len(found) == 0 {
		t.Fatalf("docs/SITE-DEPLOY.md names no *.pages.dev target. The CNAME "+
			"records it documents have to point somewhere, and %q is the only "+
			"hostname this project has before a custom domain resolves.",
			wantTarget)
	}
	// Deduplicated: the document names the target on every record row, and five
	// copies of one disagreement is five copies of one edit to make.
	seen := map[string]bool{}
	for _, got := range found {
		if seen[got] {
			continue
		}
		seen[got] = true
		if got != wantTarget {
			t.Errorf("docs/SITE-DEPLOY.md tells the maintainer to point DNS at "+
				"%s, and web/wrangler.toml deploys to the project %q, whose "+
				"hostname is %s.\n"+
				"        The records would be created, resolve, and serve someone "+
				"else's project or nothing at all -- and the failure appears as a "+
				"domain that does not work, which is the state #143 has been "+
				"stuck in since it was opened.", got, wm[1], wantTarget)
		}
	}
}

// Cloudflare Workers Static Assets CONCATENATES header values across matching
// _headers rules. Cloudflare Pages replaced them. This file was written for
// Pages, and its own comment said so: "a later rule overrides an earlier one for
// the same header name, which is why the cache rules come after the catch-all."
//
// After the migration to Workers that stopped being true, and the live site
// served every content-hashed asset as
//
//	Cache-Control: no-cache, public, max-age=31536000, immutable
//
// where RFC 9111 gives `no-cache` the last word. Eleven of eleven assets
// revalidated on every navigation, and the render-blocking stylesheet cost about
// 588ms in front of first paint each time. Nothing failed; it was merely slow,
// which is why it took a measurement rather than a test to find.
//
// The rule this enforces is narrow and mechanical: no two rules may set the same
// header, because on Workers the second does not win -- it joins.
//
// Proven able to fail against the committed tree by adding `Cache-Control:
// no-cache` back to the `/*` block: this reports /_astro/* and /* both setting
// Cache-Control.
func TestNoTwoHeaderRulesSetTheSameHeader(t *testing.T) {
	raw := readRepoFile(t, "web", "public/_headers")

	var path string
	// header name -> the paths that set it
	setters := map[string][]string{}
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			path = trimmed
			continue
		}
		name, _, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		name = strings.ToLower(strings.TrimSpace(name))
		setters[name] = append(setters[name], path)
	}
	if len(setters) == 0 {
		t.Fatal("no header rules parsed out of web/public/_headers; this guard " +
			"would pass just as happily on an empty file")
	}

	for name, paths := range setters {
		// A header set by several DISJOINT path rules is fine -- /fonts/* and
		// /_astro/* both setting Cache-Control never match one request. What is
		// not fine is a rule that also matches everything.
		hasCatchAll := false
		for _, p := range paths {
			if p == "/*" {
				hasCatchAll = true
			}
		}
		if hasCatchAll && len(paths) > 1 {
			t.Errorf("%q is set on /* AND on %v.\n"+
				"On Workers Static Assets those values are CONCATENATED, not "+
				"replaced, so the /* value survives into every more specific "+
				"rule. For Cache-Control that means a `no-cache` on /* defeats "+
				"every `immutable` below it.",
				name, paths)
		}
	}
}
