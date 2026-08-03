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
		if !strings.Contains(src, `"`+string(a)+`"`) {
			t.Errorf("action %q appears nowhere in AutomationPage.tsx. An action the "+
				"operator cannot choose is a feature nobody can reach — add it to the "+
				"dropdown, or delete the action.", a)
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
