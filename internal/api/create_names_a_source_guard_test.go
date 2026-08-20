package api

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// EVERY CALLER THAT CREATES A DESTINATION OR RENDITION NAMES ITS PROGRAMME, and
// this walks the tree to say so rather than trusting anyone to have found them
// all.
//
// It exists because nobody did. requireNamedSource made an omitted sourceId a
// 400, and the callers were then found in FOUR passes, each of which believed
// it was complete:
//
//  1. internal/api tests            -- found by running the suite
//  2. scripts/*.go drivers          -- found when the acceptance matrix went red
//  3. driverlib.CreateDest          -- found when acceptance-failover went red
//  4. ui/e2e/*.ts Playwright specs  -- found when the browser suite went red
//
// Every miss was a grep whose scope was too narrow, and each one cost a full CI
// cycle to discover. A grep that is wrong is indistinguishable from a grep that
// is right until something fails; this test is the same grep, run by CI, that
// fails immediately and names the file.
//
// It is deliberately DUMB. It does not parse; it asks whether a file that posts
// to these endpoints mentions a source at all. That is enough to catch a whole
// file nobody converted, which is the failure that actually happened, and it
// cannot be fooled into a false pass by a clever body it does not understand.
func TestEveryCreateCallerNamesItsSource(t *testing.T) {
	root := repoRoot(t)

	// Directories whose files call the API over HTTP. internal/api's own tests
	// are excluded: they reach the handler through send() and
	// createDestination(), which fill the field in, and the refusal has its own
	// test that deliberately does not.
	dirs := []string{"scripts", filepath.Join("ui", "e2e")}

	// (?s) and a newline-tolerant gap, because the call is routinely SPLIT
	// across lines:
	//
	//	await api<{ rendition: Rendition }>(
	//	  page,
	//	  "POST",
	//	  "/api/v1/renditions",
	//
	// The first version of this pattern used [^\n]{0,80} and matched none of
	// those. It passed against a file with the source removed -- a guard that
	// cannot see the shape it is guarding, which is worse than no guard,
	// because it reports success. Found by mutation rather than by reading it.
	posts := regexp.MustCompile(`(?is)(MethodPost|"POST")[\s\S]{0,120}(/destinations|/renditions)"`)

	// A file may legitimately post without a source: a negative test asserting
	// the refusal, or one whose request is rejected earlier for another reason.
	// Each exemption names why, because an unexplained allowlist entry is how a
	// guard stops guarding.
	allowed := map[string]string{
		// Posts as an unauthenticated caller to prove auth rejects before the
		// body is ever validated. A source would change nothing.
		filepath.Join("scripts", "acceptance_docker_driver.go"): "security() posts unauthenticated on purpose; its other creates do name a source",
	}

	var missing []string
	for _, dir := range dirs {
		err := filepath.Walk(filepath.Join(root, dir), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			switch filepath.Ext(path) {
			case ".go", ".ts", ".tsx", ".sh":
			default:
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			body := string(b)
			if !posts.MatchString(body) {
				return nil
			}
			if strings.Contains(body, "sourceId") || strings.Contains(body, "SourceID") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			if _, ok := allowed[rel]; ok {
				return nil
			}
			missing = append(missing, rel)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}

	if len(missing) > 0 {
		t.Errorf("these files create a destination or rendition and never name a "+
			"programme, so every one of their creates is refused with 400 "+
			"source_required:\n  %s\n\nThe server no longer fills in an omitted "+
			"sourceId -- see requireNamedSource. Read the id back from GET /sources "+
			"rather than hardcoding 1; an id that is only ever right while nothing "+
			"has been deleted is the assumption this change removed.",
			strings.Join(missing, "\n  "))
	}
}

// repoRoot walks up from the test's working directory to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("no go.mod above the test's working directory, so the tree cannot be walked")
	return ""
}
