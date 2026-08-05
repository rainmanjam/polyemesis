package db

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

/* ===========================================================================
   These guards read source text, and that is a real limitation.

   What they can prove is that a control is written INSIDE the block whose
   condition gates it. What they cannot prove is that React puts it on screen
   for a given account -- that needs a browser, and ui/e2e/ has one
   (scripts/acceptance-browser.sh, Playwright against the shipped container).
   A DOM-level assertion belongs there and would be strictly better than any of
   this; it is not written here because a Go test in internal/db cannot render
   React, and because that suite is gated on a container build rather than on
   `go test`.

   What these guards used to do was weaker still: they matched a bare substring
   anywhere in the file. Every one of them stayed green when its block was
   switched off, because the label, the input id and the field name all remain
   in the source of a block that no longer renders. TestTheCardShowsTheBackupFeedsState
   was the one exception -- it matched the whole condition, and recorded why --
   and jsxBlockUnder below is that technique generalised and taken one step
   further: find the whole head of the conditional, walk to its matching close,
   and require the control to be inside THAT span rather than anywhere in the
   file.

   That catches both realistic shapes:

     - the block is disabled -- `{false && platform === "facebook" && (` no
       longer matches the head, so the block cannot be found at all;
     - the block is deleted and a reference survives elsewhere -- the head is
       gone, and a mention in the save payload or in a comment is outside any
       block and satisfies nothing.

   =========================================================================== */

// stripJSComments blanks out comments so a marker left behind in one cannot
// satisfy a guard that is asking whether a control renders. That is not
// hypothetical: the honest way to keep a substring guard green while deleting
// the thing it watches is to leave the words in a comment, and this branch's
// own audit records that temptation being declined by hand rather than by a
// guard.
//
// Block comments (`/* */`, and the `{/* */}` JSX form, which is a block comment
// inside an expression container) are removed outright. Line comments are
// removed only when nothing before the `//` on that line is quoted, so
// a URL in a string literal or a template literal is left alone rather than
// truncated at the scheme separator.
//
// Newlines are preserved so a line number quoted in a failure still means
// something to whoever goes to look.
func stripJSComments(src string) string {
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

// jsxBlockUnder returns the source of the subtree a JSX conditional renders,
// given the WHOLE head of that conditional including its opening paren -- e.g.
// `{platform === "facebook" && (`.
//
// The head is required to appear exactly once. A refactor that produces two
// blocks with the same condition is not a failure, but it does mean this guard
// no longer knows which one it is measuring, and saying so beats picking the
// first and reporting on the wrong one.
//
// It t.Fatals rather than returning an error because every failure mode here --
// head missing, head duplicated, parens that never balance -- means the guard
// has stopped watching what it claims to watch, which is exactly the state this
// whole file exists to make loud.
func jsxBlockUnder(t *testing.T, src, head, file string) string {
	t.Helper()
	if !strings.HasSuffix(head, "(") {
		t.Fatalf("guard bug: %q is not a whole conditional head; it must end with the "+
			"opening paren so the block can be bounded", head)
	}
	stripped := stripJSComments(src)
	switch n := strings.Count(stripped, head); {
	case n == 0:
		t.Fatalf("%s no longer contains %s\n\n"+
			"Either the block was deleted, or its condition changed. If a control "+
			"moved behind a new condition, that is a deliberate change to who can "+
			"reach it: update this head and say in the commit message which accounts "+
			"still see it. A guard that matched the field name alone would have "+
			"stayed green through both.", file, head)
	case n > 1:
		t.Fatalf("%s contains %s %d times; this guard can no longer tell which block "+
			"it is measuring", file, head, n)
	}
	open := strings.Index(stripped, head) + len(head) - 1
	depth := 0
	for j := open; j < len(stripped); j++ {
		switch stripped[j] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return stripped[open+1 : j]
			}
		}
	}
	t.Fatalf("%s: the parens opened by %s never close. Most likely an unbalanced "+
		"paren inside a string or JSX text, which this guard cannot see through -- "+
		"fix it here rather than weakening the guard", file, head)
	return ""
}

// readUI reads a file under ui/src, from internal/db.
func readUI(t *testing.T, parts ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{"..", "..", "ui", "src"}, parts...)...)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	return string(raw)
}

// The head of the Facebook create-time settings block. Everything the crosspost
// list, the donate field and the backup toggle need is inside it, so all three
// guards below bound themselves the same way.
const facebookBlockHead = `{platform === "facebook" && (`

// The Facebook block must have a control, not merely a name.
//
// This is the same failure the ledger already records twice: crosspost and
// donateCharityId appeared in ui/src/lib/types.ts and nowhere else, so
// TestUITypesCanNameEveryDestinationField was green the entire time nothing
// in the dialog could set either field. Declaring a type is what the code
// compiles against; an input the operator can type into is what makes a
// feature exist.
//
// MUTATION, run against a committed tree: wrapping the block in
// `{false && platform === "facebook" && (` -- the head stops matching, the
// block cannot be found, and this fails. The previous version of this guard
// searched the whole file for "Crosspost to Pages" and `id="dest-fb-donate"`,
// both of which survive that mutation untouched, and stayed green.
func TestFacebookCrosspostAndDonateAreOfferedByTheDestinationEditor(t *testing.T) {
	src := readUI(t, "components", "DestinationDialog.tsx")
	block := jsxBlockUnder(t, src, facebookBlockHead, "DestinationDialog.tsx")

	if !strings.Contains(block, `<Label>Crosspost to Pages</Label>`) {
		t.Error("no crosspost control inside the Facebook block in DestinationDialog.tsx. " +
			"A crosspost list an operator cannot build is a field that can only ever be " +
			"empty, and every line of Facebook crossposting behind it is unreachable.")
	}
	// The button, not just the label: a list with no way to add a row is a
	// heading over an empty space.
	if !strings.Contains(block, `crosspost: [...(facebook.crosspost ?? []), { pageId: "" }]`) {
		t.Error("the crosspost list has no add-a-Page control inside the Facebook block, " +
			"so the list can only ever stay at whatever length it loaded with.")
	}
	if !strings.Contains(block, `id="dest-fb-donate"`) {
		t.Error("no donate-charity-id input inside the Facebook block in DestinationDialog.tsx. " +
			"A charity id an operator cannot type is a field that can only ever be empty, " +
			"and donate_button_charity_id never leaves polyemesis.")
	}
}

// The Facebook block's control values must actually leave the dialog.
//
// A control that renders but is missing from save()'s payload literal is the
// same unreachable feature with a nicer disguise -- exactly what Task 4b
// found once already at the API layer (handlePushMetadata silently dropping
// tags) and what this branch's own C1 review finding was: the Audience
// select existed, the payload just never carried `facebook`. Matches the
// payload literal specifically, not the file, because "facebook" appears
// elsewhere (state, comments, the platform preset) for unrelated reasons.
//
// This one was already bounded to the right span and is left as it was.
func TestDestinationDialogSavePayloadCarriesTheFacebookBlock(t *testing.T) {
	src := readUI(t, "components", "DestinationDialog.tsx")

	marker := "const payload: Partial<Destination> = {"
	start := strings.Index(src, marker)
	if start < 0 {
		t.Fatal("cannot find the save payload literal in DestinationDialog.tsx; this " +
			"guard is no longer looking where the payload lives, so it asserts nothing")
	}
	end := strings.Index(src[start:], "};")
	if end < 0 {
		t.Fatal("save payload literal has no closing brace within the file; cannot bound the window")
	}
	window := src[start : start+end]
	if !strings.Contains(window, "facebook") {
		t.Error("the save payload does not carry facebook, so dest.Facebook is " +
			"permanently the zero value no matter what the crosspost and donate " +
			"controls are set to. Add it to the payload literal.")
	}
}

// A public page is created on the operator's behalf; giving them no way to
// reach it is half a feature. It also makes a dead broadcast legible -- when
// the link 404s they can see what the situation is for themselves.
//
// This watches the RENDERED link, not the type. types.ts declaring
// `broadcastId?: string` proves only that the type exists, which is the shape
// of mistake that shipped unsendable tags: every end of the wire named, and
// nothing carrying a value across it.
//
// MUTATION, run against a committed tree: `{dest.facebookBroadcastId && false && (`
// -- the head stops matching and this fails. The previous version searched the
// whole card for the href template and stayed green through it, because the
// href is still written inside the block that no longer renders.
func TestTheCardLinksToTheScheduledBroadcast(t *testing.T) {
	src := readUI(t, "components", "DestinationCard.tsx")
	block := jsxBlockUnder(t, src, "{dest.facebookBroadcastId && (", "DestinationCard.tsx")

	if !strings.Contains(block, "facebook.com/${dest.facebookBroadcastId}") {
		t.Error("the destination card does not link to the scheduled Facebook " +
			"broadcast, so an operator has no way to reach the event page " +
			"polyemesis created for them.")
	}
}

// A backup nobody can see is worse than no backup: the operator believes they
// have redundancy. Watches the RENDERED state, not the type -- types.ts
// declaring backupProcess proves only that the type exists.
//
// This is the guard that got it right first: it matched the whole condition
// `{dest.backupProcess && (` because matching "dest.backupProcess" alone was
// satisfied by the reference inside the block, so replacing the render with
// `{false && (` left it green -- measured. What it still could not say is that
// the state is rendered INSIDE the block it names, so it now bounds itself the
// same way its neighbours have been taught to.
//
// MUTATION, run against a committed tree: `{false && dest.backupProcess && (`
// -- head gone, block unfindable, fails.
func TestTheCardShowsTheBackupFeedsState(t *testing.T) {
	src := readUI(t, "components", "DestinationCard.tsx")

	state := jsxBlockUnder(t, src, "{dest.backupProcess && (", "DestinationCard.tsx")
	if !strings.Contains(state, "dest.backupProcess.state") {
		t.Error("the card does not render the backup feed's state, so a backup " +
			"that has been dead for an hour looks identical to a healthy one.")
	}

	failure := jsxBlockUnder(t, src, "{dest.backupError && (", "DestinationCard.tsx")
	if !strings.Contains(failure, "{dest.backupError}") {
		t.Error("the card does not render why a requested backup is missing, so " +
			"an operator who enabled redundancy and did not get it is never told.")
	}
}

// The toggle has to be reachable. A setting the server honours and the dialog
// never offers is the unreachable-feature shape this repo keeps finding.
//
// MUTATION, run against a committed tree: `{false && platform === "facebook" && (`
// -- fails. `backupIngest: e.target.checked` is still written in the disabled
// block, so the previous whole-file search passed.
func TestTheDialogOffersTheBackupIngestToggle(t *testing.T) {
	src := readUI(t, "components", "DestinationDialog.tsx")
	block := jsxBlockUnder(t, src, facebookBlockHead, "DestinationDialog.tsx")

	if !strings.Contains(block, "backupIngest: e.target.checked") {
		t.Error("the destination dialog has no control inside the Facebook block that " +
			"SETS backupIngest, so the backup feed can never be turned on from the UI.")
	}
	// The two costs are not guessable and an operator who learns about them
	// afterwards has already paid one of them.
	if !strings.Contains(block, "upload bandwidth") ||
		!strings.Contains(block, "reconnects the stream") {
		t.Error("the toggle does not state its costs: it doubles upload bandwidth " +
			"and reconnects the stream once when enabled.")
	}
}
