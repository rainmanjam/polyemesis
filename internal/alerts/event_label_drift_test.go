package alerts

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/testenv"
)

/* EVERY EVENT TYPE THIS PACKAGE CAN EMIT MUST HAVE A LABEL IN THE UI, AND
 * NOTHING CHECKED THAT.
 *
 * upgrade.staged and upgrade.rolled_back were added to this package and never
 * added to ui/src/pages/AutomationPage.tsx's EVENT_LABELS. The UI's fallback is:
 *
 *     name in EVENT_LABELS ? t(EVENT_LABELS[name]) : name
 *
 * so an operator sees the literal string "upgrade.staged" where every other
 * event shows a translated sentence. It fails OPEN and SILENT -- nothing errors,
 * nothing logs, one row of one page just looks unfinished. Found by diffing the
 * two lists by hand; nothing would have found it otherwise.
 *
 * WHY THE EXISTING GUARDS DO NOT COVER THIS. internal/hooks/hooks_test.go
 * asserts len(AllTriggers()) is a literal number, and its comment is honest
 * about why: "a test that asks AllTriggers what is in AllTriggers cannot fail".
 * That catches an ADDITION and prompts a docs row. It cannot catch a type added
 * to Go and missing from the TypeScript, which is exactly what happened here --
 * and the alerts side has no count guard at all.
 *
 * This is the same shape as the capability-matrix gap closed earlier this week:
 * four surfaces describing the same thing, every comparison document-to-document,
 * none of them to the code.
 */

// eventLabelSource is the file holding the map an operator's labels come from.
var eventLabelSource = []string{"pages", "AutomationPage.tsx"}

// eventLabelSourceName is the same path for messages.
const eventLabelSourceName = "ui/src/pages/AutomationPage.tsx"

// notLabelled is the set of Types deliberately absent from the UI map, each with
// the reason. An entry here is a claim, so the test checks BOTH directions: a
// type listed here that turns out to be labelled fails too, because a stale
// exemption is how the next real omission hides behind a sentence nobody
// re-read.
var notLabelled = map[Type]string{
	// The test button's event. Same reasoning hooks_test.go applies to
	// TriggerTest: a test event must not appear in a picker of things to
	// subscribe to, so it deliberately has no operator-facing label.
	TypeTest: "the test button's own event; it must not appear as a subscribable type",
}

// liveEventTypes is every Type declared in this package, read from the source so
// a new constant cannot be added without this test seeing it.
func liveEventTypes(t *testing.T) []Type {
	t.Helper()
	// Relative: `go test` runs each package in its own source directory, so
	// alerts.go is beside this file.
	raw, err := os.ReadFile("alerts.go")
	if err != nil {
		t.Fatalf("read alerts.go: %v", err)
	}
	// `TypeX Type = "some.event"` — the declaration form every one of these uses.
	re := regexp.MustCompile(`Type[A-Za-z0-9_]*\s+Type\s*=\s*"([a-z0-9._]+)"`)
	var out []Type
	for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
		out = append(out, Type(m[1]))
	}
	if len(out) < 10 {
		t.Fatalf("found only %d event types in alerts.go; the pattern has stopped "+
			"matching and this test is asserting about almost nothing", len(out))
	}
	return out
}

// labelledInUI reads the EVENT_LABELS map, with comments stripped so a type
// mentioned in prose cannot satisfy the check.
func labelledInUI(t *testing.T) map[string]bool {
	t.Helper()
	src := testenv.StripJSComments(testenv.ReadUI(t, eventLabelSource...))
	start := strings.Index(src, "EVENT_LABELS")
	if start < 0 {
		t.Fatalf("%s no longer contains EVENT_LABELS. If the map was renamed, rename "+
			"it here too; if labels moved elsewhere, point this test at the new home "+
			"rather than deleting it.", eventLabelSourceName)
	}
	end := strings.Index(src[start:], "\n};")
	if end < 0 {
		t.Fatalf("could not find the end of EVENT_LABELS in %s", eventLabelSourceName)
	}
	body := src[start : start+end]

	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`"([a-z0-9._]+)"\s*:`).FindAllStringSubmatch(body, -1) {
		out[m[1]] = true
	}
	return out
}

func TestEveryAlertTypeHasAUILabel(t *testing.T) {
	labels := labelledInUI(t)

	for _, ty := range liveEventTypes(t) {
		ty := ty
		t.Run(string(ty), func(t *testing.T) {
			why, exempt := notLabelled[ty]
			labelled := labels[string(ty)]

			if exempt && labelled {
				t.Fatalf("%q is recorded here as deliberately unlabelled (%q) but "+
					"%s now labels it. Delete the exemption: an excuse nobody has to "+
					"re-read is where the next real omission hides.",
					ty, why, eventLabelSourceName)
			}
			if exempt {
				return
			}
			if !labelled {
				t.Errorf("%q has no entry in EVENT_LABELS in %s.\n\n"+
					"The UI falls back to rendering the raw name, so an operator sees "+
					"the literal string %q where every other event shows a translated "+
					"sentence — it fails open and silent, and one row of one page just "+
					"looks unfinished.\n\n"+
					"Add a catalogue key, add it to ALL 15 locales (internal/web's "+
					"i18n drift tests will tell you), and map it here. If the type is "+
					"deliberately not operator-facing, add it to notLabelled with the "+
					"reason.", ty, eventLabelSourceName, ty)
			}
		})
	}
}

// The UI must not label something this package cannot emit: a picker offering an
// event that never arrives is a rule an operator can write and then wait on
// forever.
func TestTheUILabelsNoEventThatCannotBeEmitted(t *testing.T) {
	live := map[string]bool{}
	for _, ty := range liveEventTypes(t) {
		live[string(ty)] = true
	}
	for name := range labelledInUI(t) {
		if !live[name] {
			t.Errorf("%s labels %q, which is not a Type in internal/alerts. Either the "+
				"constant was renamed and the UI was not updated — so an operator can "+
				"subscribe to an event that will never arrive — or the label is left "+
				"over from one that was removed.", eventLabelSourceName, name)
		}
	}
}
