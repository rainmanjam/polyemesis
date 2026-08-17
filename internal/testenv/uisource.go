package testenv

// UI SOURCE. Issue #379.
//
// A UI-drift guard reads TypeScript from disk and asserts a substring is in it.
// Two helpers make that honest, and until now only one package had them:
//
//	internal/db/facebook_ui_drift_test.go   readUI, stripJSComments
//
// COMMENTS ARE THE HOLE, and it is measured rather than theorised. #367: the
// real code was deleted out of AppLayout.tsx and its text left behind in a
// `// was: ...` comment, and the guard watching it stayed green. That is not a
// contrived mutation -- it is the honest way to keep a substring guard green
// while removing the thing it watches, which makes it the shape that actually
// happens. So the words a guard reads must not be the words in a comment.
//
// internal/oauth had four guards reading ui/src and no comment stripper, so all
// four were defeatable that way (probed and confirmed on #379). The repair is
// not a second forty-line copy of the stripper in that package: two copies of a
// rule about what a guard may see is two rules the day one of them is edited,
// and the guard that stops matching first is the one nobody notices. Hence one
// implementation, here, where the port helpers already live for the same reason
// (see ports.go on the four copies of freeUDPPort).
//
// WHAT THESE CANNOT DO, said out loud so a green run is not read as a stronger
// claim: reading source proves a control is WRITTEN, never that React puts it on
// screen. That needs a browser and ui/e2e/ has one. These are the cheap net
// under the class, run by `go test ./...` on every machine.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ReadUI reads a file under ui/src and returns its contents.
//
// THE ROOT IS FOUND, NOT ASSUMED. Every caller of this before it moved here
// spelled the path `filepath.Join("..", "..", "ui", "src", ...)`, which is
// correct only for a package exactly two directories below the module root.
// That was true of both callers and is not a property this helper can promise
// now that it is shared: a guard written in internal/api/media/ would resolve to
// a path that does not exist, and the failure -- "cannot read ..." -- reads like
// a missing UI file rather than like a helper counting wrong. Walking up to
// go.mod costs a few stats once and cannot be wrong about which tree it is in.
//
// AN EMPTY FILE IS A FAILURE, not an empty string handed to the caller. This is
// the one behaviour ReadUI takes from internal/testenv's own readRepoFile rather
// than from internal/db's readUI, which had no such check. The two had drifted,
// and the version with the check is the right one: a guard asserting a substring
// is ABSENT -- `if strings.Contains(src, "status?.ingest")` in the ingest-header
// guard is exactly that shape -- passes over an empty file while asserting
// nothing at all. Failing here names the file; passing vacuously names nothing.
func ReadUI(t *testing.T, parts ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{moduleRoot(t), "ui", "src"}, parts...)...)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	if len(raw) == 0 {
		t.Fatalf("%s is empty; every assertion that reads it would pass by "+
			"examining nothing", path)
	}
	return string(raw)
}

// StripJSComments blanks out comments so a marker left behind in one cannot
// satisfy a guard that is asking whether a control is wired.
//
// Block comments (`/* */`, and the `{/* */}` JSX form, which is a block comment
// inside an expression container) are removed outright. Line comments are
// removed only when nothing before the `//` on that line is quoted, so a URL in
// a string literal or a template literal is left alone rather than truncated at
// the scheme separator.
//
// Newlines are preserved so a line number quoted in a failure still means
// something to whoever goes to look, and so any guard that bounds itself by
// counting lines or by searching forward from an offset sees the same shape it
// would have seen in the file.
//
// IT IS NOT A PARSER and does not pretend to be. A `/*` inside a string literal
// is read as a comment opener, which would swallow source. That has not happened
// in this tree and the alternative is a TypeScript toolchain invoked from a Go
// test, which would make every one of these guards skip itself on any machine
// missing it -- and a guard that skips silently is worse than no guard, because
// the next person reads green and believes it. The same trade is written down in
// internal/oauth's parseUICapabilities for the same reason.
//
// A PORT OF THIS EXISTS IN TYPESCRIPT, in ui/src/lib/strip-js-comments.ts, and
// it cannot be collapsed into this one: vitest cannot call Go.
//
// THE TWO ARE NOW PINNED TO A SHARED CORPUS rather than to each other's word.
// This used to say they had been compared by hand when this was promoted --
// both run over all 96 .ts and .tsx files under ui/src, byte-identical -- and
// that nothing enforced it. A hand comparison is evidence about one afternoon,
// and it cannot survive somebody fixing a bug in one copy, which is the only way
// this function ever changes.
//
// testdata/js-comment-corpus.json is the spec. js_comment_corpus_test.go drives
// this implementation against it and ui/src/lib/strip-js-comments.test.ts drives
// the port against the SAME FILE, so changing the behaviour means changing the
// corpus and turning the other language's suite red in the same commit.
func StripJSComments(src string) string {
	var b strings.Builder
	b.Grow(len(src))
	for i := 0; i < len(src); {
		if strings.HasPrefix(src[i:], "/*") {
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				break // unterminated; the rest is comment
			}
			for _, r := range src[i : i+2+end+2] {
				if r == '\n' {
					b.WriteByte('\n')
				}
			}
			i += 2 + end + 2
			continue
		}
		if strings.HasPrefix(src[i:], "//") && !quotedBefore(src, i) {
			end := strings.IndexByte(src[i:], '\n')
			if end < 0 {
				break
			}
			i += end // leave the newline for the next iteration
			continue
		}
		b.WriteByte(src[i])
		i++
	}
	return b.String()
}

// quotedBefore reports whether a quote character appears between the start of
// the line containing i and i itself -- the cheap test for "this `//` is inside
// a string literal or JSX attribute rather than starting a comment".
func quotedBefore(src string, i int) bool {
	start := strings.LastIndexByte(src[:i], '\n') + 1
	return strings.ContainsAny(src[start:i], "\"'`")
}

// moduleRoot walks up from the test's working directory to the directory
// holding go.mod.
//
// `go test` runs each package in its own source directory, so the walk starts
// somewhere inside the tree and terminates at the module root or at the
// filesystem root. Reaching the filesystem root is a Fatal rather than a
// fallback: a helper that guessed would hand back a path that reads a file from
// some other checkout, and a drift guard measuring the wrong tree is the failure
// this whole family of tests exists to prevent.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("cannot resolve the working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s; ReadUI cannot tell which tree it is "+
				"reading, and a drift guard pointed at the wrong tree is worse "+
				"than one that is absent", dir)
		}
		dir = parent
	}
}
