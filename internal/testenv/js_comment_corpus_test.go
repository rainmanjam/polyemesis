package testenv

import (
	"encoding/json"
	"os"
	"testing"
)

/* ONE FUNCTION, TWO LANGUAGES, PINNED TO A SHARED CORPUS INSTEAD OF TO EACH
 * OTHER.
 *
 * StripJSComments' own comment records the problem and its own lack of a fix:
 * "A PORT OF THIS EXISTS IN TYPESCRIPT, in ui/src/lib/tour-drift.test.ts, and it
 * cannot be collapsed into this one: vitest cannot call Go. The two were
 * measured against each other when this was promoted -- both run over all 96 .ts
 * and .tsx files under ui/src, output hashed per file, byte-identical on every
 * one -- so they have not drifted. NOTHING ENFORCES THAT."
 *
 * A hand-comparison is evidence about one afternoon. What it cannot survive is
 * somebody fixing a bug in one copy, which is the only way this function ever
 * changes -- and the two copies guard DIFFERENT things (Go guards the Facebook
 * settings and capability mirrors, TypeScript guards the tour's anchors), so a
 * divergence shows up as one suite going quietly permissive rather than as a
 * conflict anybody sees.
 *
 * WHY A CORPUS RATHER THAN A CALL. The alternative -- have Go shell out to node,
 * or vitest shell out to go -- makes every guard downstream of it skip on a
 * machine missing the other toolchain, and StripJSComments' own comment already
 * rejects that trade: "a guard that skips silently is worse than no guard,
 * because the next person reads green and believes it."
 *
 * So testdata/js-comment-corpus.json is the SPEC, and both implementations are
 * measured against it independently. Neither is the oracle for the other;
 * changing the behaviour means changing the corpus, which makes the change
 * visible in a diff and forces the other language's suite to be updated in the
 * same commit or go red.
 *
 * The corpus is deliberately about the RULES rather than about real files:
 * block comments removed with newlines preserved, line comments removed only
 * when nothing before them on that line is quoted, and the two unterminated
 * cases where the rest of the file is swallowed.
 */

const jsCommentCorpusPath = "testdata/js-comment-corpus.json"

type jsCommentCase struct {
	Name  string `json:"name"`
	Input string `json:"input"`
	Want  string `json:"want"`
}

func loadJSCommentCorpus(t *testing.T) []jsCommentCase {
	t.Helper()
	raw, err := os.ReadFile(jsCommentCorpusPath)
	if err != nil {
		t.Fatalf("read %s: %v", jsCommentCorpusPath, err)
	}
	var cases []jsCommentCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("parse %s: %v", jsCommentCorpusPath, err)
	}
	if len(cases) == 0 {
		t.Fatal("the corpus is empty, so both implementations are pinned to nothing")
	}
	return cases
}

func TestStripJSCommentsMatchesTheSharedCorpus(t *testing.T) {
	for _, tc := range loadJSCommentCorpus(t) {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			if got := StripJSComments(tc.Input); got != tc.Want {
				t.Errorf("StripJSComments disagrees with the shared corpus.\n"+
					"  in:   %q\n  got:  %q\n  want: %q\n\n"+
					"The corpus is the SPEC for both this and its TypeScript port in "+
					"ui/src/lib/strip-js-comments.ts. If this change is intended, change "+
					"the corpus — which makes the TypeScript suite go red in the same "+
					"commit, which is the entire point.",
					tc.Input, got, tc.Want)
			}
		})
	}
}

// The corpus has to keep exercising the rules it was written for. A future edit
// that deletes the awkward cases would leave both implementations pinned to
// "does not crash on plain source".
func TestTheCorpusStillCoversTheRulesThatMatter(t *testing.T) {
	cases := loadJSCommentCorpus(t)
	var block, line, quoted, unterminated bool
	for _, c := range cases {
		switch {
		case c.Input == "":
			continue
		}
		if containsAll(c.Input, "/*", "*/") {
			block = true
		}
		if containsAll(c.Input, "//") && c.Want != c.Input {
			line = true
		}
		// A quoted URL that SURVIVES is the rule that stops a string literal
		// being truncated at its scheme separator.
		if containsAll(c.Input, "://") && containsAll(c.Want, "://") {
			quoted = true
		}
		if containsAll(c.Input, "/*") && !containsAll(c.Input, "*/") {
			unterminated = true
		}
	}
	for _, chk := range []struct {
		ok   bool
		what string
	}{
		{block, "a block comment"},
		{line, "a line comment that is actually removed"},
		{quoted, "a quoted URL that survives (the // -inside-a-string rule)"},
		{unterminated, "an unterminated block comment"},
	} {
		if !chk.ok {
			t.Errorf("the corpus no longer covers %s, so both implementations are "+
				"pinned to less than the rules they claim", chk.what)
		}
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
