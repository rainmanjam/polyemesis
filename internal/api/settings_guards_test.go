package api

import (
	"net/http"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// A RULE THAT CANNOT COMPILE IS REFUSED AT THE MOMENT IT IS TYPED.
//
// PUT /settings accepted an automod pattern like "(unclosed[" with a 200, and
// NewRuleSet is all-or-nothing at apply time: ApplyAutomod then set rules = nil
// and EVERY pattern rule stopped running, not only the broken one. The narrowest
// possible typo disarmed the whole checker, silently, while GET /settings
// listed the rules enabled and GET /automod/matrix reported the checker
// available.
//
// Mutation: drop the rulesFromSettings call from the settings handler. Observed
// to fail with "an automod rule whose regex does not compile was accepted".
func TestSettingsRefusesAnAutomodRuleThatCannotCompile(t *testing.T) {
	_, h, _, sign := managerServer(t, defaultTools())

	var cur db.Settings
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &cur)

	cur.Automod.Rules = []db.AutomodRule{{
		Name: "unclosed", Enabled: true, Pattern: "(unclosed[", Action: "delete",
	}}
	send(t, h, sign, http.MethodPut, "/api/v1/settings", cur, http.StatusBadRequest)

	// THE CONTROL. A handler that refused every automod document would satisfy
	// the assertion above and make the feature unusable.
	cur.Automod.Rules = []db.AutomodRule{{
		Name: "fine", Enabled: true, Pattern: "(?i)buy followers", Action: "delete",
	}}
	send(t, h, sign, http.MethodPut, "/api/v1/settings", cur, http.StatusOK)
}

// THE DISPLAY TIME ZONE HAS TO RESOLVE.
//
// A zone that cannot be loaded falls back to UTC everywhere with nothing
// saying why -- which reads as "the setting is broken" to somebody who notices
// and as a correct time to somebody who does not.
//
// Mutation: delete the LoadLocation check in Settings.Validate. Observed to
// fail with "an unresolvable display time zone was accepted".
func TestSettingsRefusesADisplayTimeZoneThatDoesNotResolve(t *testing.T) {
	_, h, _, sign := managerServer(t, defaultTools())

	var cur db.Settings
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &cur)

	cur.Display.TimeZone = "Mars/Olympus_Mons"
	send(t, h, sign, http.MethodPut, "/api/v1/settings", cur, http.StatusBadRequest)

	// A real zone is accepted, which is the whole point of the feature, and it
	// resolves in the shipped container because internal/scheduler compiles the
	// zone database in.
	cur.Display.TimeZone = "Europe/London"
	send(t, h, sign, http.MethodPut, "/api/v1/settings", cur, http.StatusOK)

	// And empty stays UTC, which is what every install did before this existed.
	cur.Display.TimeZone = ""
	send(t, h, sign, http.MethodPut, "/api/v1/settings", cur, http.StatusOK)
}
