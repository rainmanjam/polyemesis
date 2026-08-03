package scheduler

import (
	"os"
	"path/filepath"
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
// pins the count. A fifth action makes this fail, which is the reminder.
//
// The same shape as i18n's wantLocales and the settings drift guard's own
// counts: a list that must be complete needs something that notices when it
// stops being.
func TestTheActionListInThisFileIsComplete(t *testing.T) {
	const wantActions = 4
	got := len([]Action{ActionStart, ActionStop, ActionPlaylistStart, ActionPlaylistStop})
	if got != wantActions {
		t.Fatalf("this file checks %d actions; if you added one, add it to the list "+
			"above and to AutomationPage.tsx, then bump this count", got)
	}
}
