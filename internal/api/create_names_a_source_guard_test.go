package api

import (
	"fmt"
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
// It checks EACH CALL SITE, not each file, and that distinction is the whole
// value. The first version asked only whether a file mentioned sourceId
// anywhere, and a HALF-CONVERTED file passed:
//
//	ui/e2e/video-treatment.spec.ts   makeRendition  named a source
//	                                 makeDestination did not
//
// The file mentioned sourceId, the guard was satisfied, and four browser tests
// went on failing. The same shape had already appeared in
// acceptance_docker_driver.go, whose destFor() named a source while dests() and
// rtmpDest() next door did not -- it was written up as a warning and then
// reproduced anyway. Twice is a pattern, so this now reads the body that
// follows each POST.
//
// It still does not parse. It takes a window after the endpoint and asks
// whether a source is named inside it, which is enough to catch a call site
// nobody converted and cannot be fooled by a sibling call site that was.
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

	// How much of the request body to read after the endpoint. Long enough for
	// a multi-line literal, short enough that the NEXT call site's source
	// cannot be mistaken for this one's.
	const bodyWindow = 700

	// The body is written INLINE at the call site when a literal opens right
	// after the endpoint, allowing for the argument separator and whitespace.
	inlineBody := regexp.MustCompile(`^[\s,)]*(map\[string\]any)?\{`)

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
			rel, _ := filepath.Rel(root, path)
			if _, ok := allowed[rel]; ok {
				return nil
			}
			// PER CALL SITE WHERE THE BODY IS WRITTEN INLINE, per file where it
			// is not. The two need different rules and mixing them is what made
			// the first two versions of this test wrong in opposite directions.
			//
			// An INLINE literal -- `{ name, kind, ... }` or `map[string]any{...}`
			// right after the endpoint -- is the body, so the source has to be
			// named inside it. That is the precise check, and it is the one that
			// catches a half-converted file: makeRendition and makeDestination
			// in the same spec, one converted and one not.
			//
			// A DELEGATED body -- `dest(...)`, `destBody(...)`, or a variable --
			// is built elsewhere, most often by a helper a few lines up that the
			// endpoint window cannot see. Demanding sourceId at the call site
			// there flagged nineteen sites that were all perfectly correct,
			// including driverlib.CreateDest, which fills the field on the line
			// BEFORE the post. For those the file-level question is the honest
			// one this test can answer without parsing Go and TypeScript.
			for _, loc := range posts.FindAllStringIndex(body, -1) {
				end := loc[1] + bodyWindow
				if end > len(body) {
					end = len(body)
				}
				window := body[loc[1]:end]
				line := 1 + strings.Count(body[:loc[0]], "\n")

				if inlineBody.MatchString(window) {
					if !namesASource(window) {
						missing = append(missing, fmt.Sprintf("%s:%d (inline body)", rel, line))
					}
					continue
				}
				if !namesASource(body) {
					missing = append(missing, fmt.Sprintf("%s:%d (body built elsewhere in this file, "+
						"which never names a source)", rel, line))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}

	if len(missing) > 0 {
		t.Errorf("these CALL SITES create a destination or rendition without naming a "+
			"programme, so each is refused with 400 source_required:\n  %s\n\n"+
			"A source named elsewhere in the same file does not answer for them -- "+
			"that is how a half-converted file passed this guard once already.\n\n"+
			"The server no longer fills in an omitted "+
			"sourceId -- see requireNamedSource. Read the id back from GET /sources "+
			"rather than hardcoding 1; an id that is only ever right while nothing "+
			"has been deleted is the assumption this change removed.",
			strings.Join(missing, "\n  "))
	}
}

// namesASource is the one question this test can answer without parsing two
// languages: is a programme named anywhere in this text.
func namesASource(text string) bool {
	return strings.Contains(text, "sourceId") || strings.Contains(text, "SourceID")
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
