package testenv

// THE WEBSITE IS A CLAIM SURFACE AND NOTHING CHECKED IT. #768.
//
// web/src/pages/features.astro said an operator could "pin the source to
// primary, backup or slate, or hand it back with auto" on 2026-08-16. The
// console gained the ability to hand it back on 2026-09-09, twenty-four days
// later. POST /failover/source had been live since the failover tier shipped
// and had no caller anywhere in the console, so for those twenty-four days the
// site advertised a capability no operator could reach.
//
// Nothing went red, and nothing could have. settings-reachability.test.tsx
// ties settings leaves to controls and features-shots.test.ts ties the site's
// images to docs/media, but no artefact tied the site's CLAIMS to the product.
// A features page is exactly where an aspirational sentence survives longest,
// because the person writing copy and the person wiring the button are working
// a month apart and neither is reading the other's file.
//
// So every section carries a receipt:
//
//	provenBy          an api.ts wrapper that performs the action the section
//	                  describes. Asserted to exist AND to be called from
//	                  outside api.ts -- the wrapper existing is what was true
//	                  the whole time in #768's case, and it is not the
//	                  property that matters.
//	claimIsBehaviour  the section describes something the server does on its
//	                  own with no operator action to prove. A written decision
//	                  rather than a gap, and the reason is read below.
//
// Rung 2. Control would mean generating the copy from the product, which is
// the wrong trade for marketing prose -- the page has to be allowed to say
// things in a human order.
//
// WHAT THIS DOES NOT COVER, said here rather than discovered later. Prose that
// overclaims INSIDE a feature that does exist -- "32 tracks" where the product
// does 16 -- is invisible to this: the receipt proves the capability is
// reachable, not that every sentence about it is true. A wrapper called only
// from dead code also passes. Both are narrower failures than the one this
// catches, which is a whole capability advertised and unreachable.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const featuresPage = "web/src/pages/features.astro"

type featureSection struct {
	id        string
	provenBy  string // an api.ts wrapper, or ""
	behaviour string // the stated reason, or ""
}

// readFeatureSections pulls one entry per `id:` in the page, with whichever
// receipt follows it.
func readFeatureSections(t *testing.T, root string) []featureSection {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, featuresPage))
	if err != nil {
		t.Fatalf("read %s: %v", featuresPage, err)
	}
	src := string(b)

	idRe := regexp.MustCompile(`id:\s*"([a-z0-9-]+)",\s*\n\s*(provenBy|claimIsBehaviour):\s*"((?:[^"\\]|\\.)*)"`)
	bareRe := regexp.MustCompile(`id:\s*"([a-z0-9-]+)",`)

	withReceipt := map[string]featureSection{}
	for _, m := range idRe.FindAllStringSubmatch(src, -1) {
		s := featureSection{id: m[1]}
		if m[2] == "provenBy" {
			s.provenBy = m[3]
		} else {
			s.behaviour = m[3]
		}
		withReceipt[m[1]] = s
	}

	var out []featureSection
	for _, m := range bareRe.FindAllStringSubmatch(src, -1) {
		if s, ok := withReceipt[m[1]]; ok {
			out = append(out, s)
			continue
		}
		out = append(out, featureSection{id: m[1]})
	}
	return out
}

func TestEveryFeatureClaimNamesSomethingThatExists(t *testing.T) {
	root := repoRootFromTest(t)
	sections := readFeatureSections(t, root)

	// POSITIVE CONTROL, first. A renamed file, a restructured page or a broken
	// regex yields no sections, and every assertion below is then satisfied by
	// a loop that runs zero times.
	if len(sections) < 8 {
		t.Fatalf("found %d feature sections in %s; the page has moved or been "+
			"restructured, so this test is asserting nothing about its claims",
			len(sections), featuresPage)
	}

	apiSrc, err := os.ReadFile(filepath.Join(root, "ui/src/lib/api.ts"))
	if err != nil {
		t.Fatalf("read ui/src/lib/api.ts: %v", err)
	}

	proven := 0
	for _, s := range sections {
		switch {
		case s.provenBy == "" && s.behaviour == "":
			t.Errorf("feature section %q carries no receipt.\n"+
				"A section that claims an operator can do something must name the "+
				"api.ts wrapper that does it (provenBy), so the claim goes red when "+
				"the capability does not exist. A section describing something the "+
				"server does on its own says so with claimIsBehaviour and why. The "+
				"failover section claimed an action for twenty-four days before any "+
				"console code could perform it -- see #768.", s.id)

		case s.behaviour != "":
			if len(s.behaviour) < 30 {
				t.Errorf("feature section %q is excused as behaviour with the reason %q, "+
					"which is too short to be one. An excuse nobody can evaluate is the "+
					"thing this file exists to refuse.", s.id, s.behaviour)
			}

		default:
			proven++
			// The wrapper has to exist...
			if !strings.Contains(string(apiSrc), "\n  "+s.provenBy+":") {
				t.Errorf("feature section %q is proven by api.%s, and ui/src/lib/api.ts "+
					"declares no such wrapper. Either it was renamed -- update the "+
					"receipt -- or the capability is gone and the section is advertising "+
					"it anyway.", s.id, s.provenBy)
				continue
			}
			// ...and something outside api.ts has to call it. This is the half
			// that matters: in #768 the ROUTE existed and was live, and what was
			// missing was anything in the console reaching it.
			if callers := callersOutsideAPI(t, root, s.provenBy); len(callers) == 0 {
				t.Errorf("feature section %q is proven by api.%s, which exists in api.ts "+
					"and is called from nowhere else in ui/src.\n"+
					"That is exactly the #768 shape: a live endpoint, a wrapper for it, "+
					"and no screen an operator can reach it from -- while this page says "+
					"they can.", s.id, s.provenBy)
			}
		}
	}

	// A page of nothing but behaviour exemptions would satisfy every branch
	// above without a single claim being checked against the product.
	if proven < 5 {
		t.Errorf("only %d of %d sections carry a provenBy receipt. The rest are excused "+
			"as behaviour, and a page excused end to end is not a page this test has "+
			"checked.", proven, len(sections))
	}
}

// callersOutsideAPI finds files that call the wrapper, ignoring api.ts itself
// and tests.
//
// MATCHED AS ".name(" RATHER THAN "api.name(", which is not a detail. The
// first version of this looked for `api.compileRouting` and reported it
// uncalled -- RoutingPage.tsx calls it as a method chain with the receiver on
// the previous line. A guard that reports a false absence is a guard somebody
// turns off, and it would have been turned off on its first run.
func callersOutsideAPI(t *testing.T, root, wrapper string) []string {
	t.Helper()
	needle := "." + wrapper + "("
	var out []string
	err := filepath.Walk(filepath.Join(root, "ui/src"), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.HasSuffix(p, ".ts") && !strings.HasSuffix(p, ".tsx") {
			return nil
		}
		if strings.HasSuffix(p, "lib/api.ts") || strings.Contains(p, ".test.") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), needle) {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk ui/src: %v", err)
	}
	return out
}
