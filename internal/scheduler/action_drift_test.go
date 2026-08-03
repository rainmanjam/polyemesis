package scheduler

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every Action must be offered by the operator's dropdown.
//
// This is the sibling of TestUITypesCanNameEverySettingsField, for a type that
// guard cannot see: it walks db.Settings and db.Destination, and a schedule is
// neither. So an Action added here and forgotten in the UI is a feature no
// operator can reach — which is the exact class roadmap item 0 existed to fix,
// and which this package had no guard against at all.
//
// It matches on the STRING VALUE, not the Go name, because the value is what
// crosses the wire and what the dropdown carries.
//
// AND IT MATCHES A `<SelectItem value="...">`, NOT THE FILE. The first version
// of this guard searched the whole source for the quoted value and could not
// have failed for the reason it exists: the action appears TWICE in that file —
// once in `type ScheduleAction = "start" | "stop" | ...` and once in the
// dropdown — so adding it to the type and forgetting the option satisfied the
// search while leaving the action unreachable. That is precisely the state the
// guard is meant to catch. Verified by deleting the SelectItem and watching the
// old version stay green.
//
// The settings drift guard carries the same documented limitation and gets away
// with it, because there the requirement IS declaration in types.ts. Here the
// requirement is "an operator can choose it", and declaring a union member is
// not that.
//
// What this still cannot see, recorded rather than closed: a `<SelectItem
// value="playlist.start">` inside a JSX comment would satisfy it, and a dropdown
// refactored to `.map()` over an array of options would fail it even though every
// action is reachable. The first needs a parser to catch and is not worth one;
// the second fails LOUDLY and safely, forcing whoever refactors the dropdown to
// re-derive what this guard should watch — which is the outcome we want anyway.
// A moved file is the same: os.ReadFile fails and t.Fatalf says so.
func TestEveryScheduleActionIsOfferedByTheUI(t *testing.T) {
	path := filepath.Join("..", "..", "ui", "src", "pages", "AutomationPage.tsx")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	src := string(raw)

	for _, a := range []Action{
		ActionStart, ActionStop, ActionPlaylistStart, ActionPlaylistStop,
	} {
		option := `<SelectItem value="` + string(a) + `">`
		if !strings.Contains(src, option) {
			t.Errorf("no %s in AutomationPage.tsx. An action the operator cannot choose "+
				"is a feature nobody can reach — add the option, or delete the action. "+
				"Adding it to the ScheduleAction type is NOT enough: the type is what the "+
				"code compiles against, the option is what a human can click.", option)
		}
	}
}

// The list above is exhaustive only if somebody remembers to extend it, so this
// DISCOVERS the actions from the source rather than restating them.
//
// The first version compared `len([]Action{a, b, c, d})` against `const 4` and
// could not fail: both sides were written by the same hand at the same moment,
// so it asserted that four equals four. A fifth action would have been added to
// the constant block, missed by the list above, taken the DESTINATION path in
// the runner — because Enables() knows nothing about targeting — and disabled
// every destination in the install, with this test green throughout.
//
// i18n's locale guard is the shape worth copying and the one that version only
// claimed to: it walks the locale directory with ReadDir and finds what is
// there. A guard that restates its input has no input.
//
// WHAT IT STILL CANNOT SEE, and this matters more here than the note above,
// because this guard is the only thing standing between a fifth action and the
// destination path: it reads scheduler.go alone, and matches only the
// `Name Action = "value"` form. An action declared in a sibling file of this
// package, or written `ActionPause = Action("pause")`, is invisible to it. The
// len(declared) == 0 tripwire below catches the constants MOVING wholesale; it
// does not catch one being ADDED somewhere else.
//
// Closing that needs go/ast over the package rather than a regexp over a file.
// Recorded rather than done because every action this package has ever had is
// declared in that one block in that one file, and a guard nobody can read is
// its own hazard -- but if a second declaration site ever appears, this is the
// note that says the guard stopped covering the thing it exists for.
func TestTheActionListInThisFileIsComplete(t *testing.T) {
	raw, err := os.ReadFile("scheduler.go")
	if err != nil {
		t.Fatalf("cannot read scheduler.go: %v", err)
	}
	// Every `Xxx Action = "..."` in the constant block, found rather than listed.
	declared := regexp.MustCompile(`(?m)^\s*\w+\s+Action\s*=\s*"([^"]+)"`).
		FindAllStringSubmatch(string(raw), -1)
	if len(declared) == 0 {
		t.Fatal("found no Action constants in scheduler.go; this guard is not looking " +
			"where they live any more, so it is asserting nothing")
	}

	checked := map[Action]bool{}
	for _, a := range []Action{
		ActionStart, ActionStop, ActionPlaylistStart, ActionPlaylistStop,
	} {
		checked[a] = true
	}
	for _, m := range declared {
		if a := Action(m[1]); !checked[a] {
			t.Errorf("action %q is declared in scheduler.go but the UI guard above does "+
				"not check it. An unchecked action can reach the runner's DESTINATION "+
				"path — Enables() answers false for anything that is not ActionStart — "+
				"and disable every destination in the install. Add it to that list, and "+
				"to AutomationPage.tsx.", a)
		}
	}
}
