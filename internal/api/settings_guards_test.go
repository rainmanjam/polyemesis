package api

import (
	"fmt"
	"net/http"
	"strings"
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

// AN INGEST LISTENER MAY NOT ASK FOR THE PORT THIS SERVER IS ANSWERING ON.
//
// PUT /settings {"listeners":{"rtmpPort":8099}} on an install serving HTTP on
// 8099 returned 200. The RTMP listener then could not bind on the next
// reconcile -- both are TCP -- which logged one ERROR and returned, and its own
// comment concedes the log is the only way anybody finds out. Ingest was dead,
// the settings page showed the port saved and green, and the operator debugged
// their encoder.
//
// Mutation: delete the TCPPortConflicts block from handlePutSettings. Observed
// to fail with "PUT /api/v1/settings: status 200, want 400".
func TestSettingsRefusesAnRTMPListenerOnTheServersOwnHTTPPort(t *testing.T) {
	s, h, _, sign := managerServer(t, defaultTools())

	// The fixture leaves Addr empty, which reserves nothing on purpose -- see
	// reservedTCPPorts. A port free for UDP so the SRT case below can really
	// bind it; TCP is never bound here because the handler answers directly.
	httpPort := freeUDPPort(t)
	s.cfg.Addr = fmt.Sprintf(":%d", httpPort)

	var cur db.Settings
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &cur)

	cur.Listeners.RTMPPort = httpPort
	msg := mustJSONError(t, h, sign, http.MethodPut, "/api/v1/settings", cur, http.StatusBadRequest)
	// Naming what holds the port is the difference between a refusal an
	// operator can act on and one that sends them to netstat.
	if !strings.Contains(msg, "web UI") {
		t.Fatalf("the refusal does not say what is holding port %d: %q", httpPort, msg)
	}

	// THE CONTROL. A guard that refused every listener document would satisfy
	// the assertion above and leave the ports unchangeable.
	cur.Listeners.RTMPPort = freeUDPPort(t)
	send(t, h, sign, http.MethodPut, "/api/v1/settings", cur, http.StatusOK)

	// AND SRT IS ALLOWED THERE, which is not an oversight. SRT is UDP and the
	// HTTP listener is TCP, so they share a number without colliding at the
	// kernel -- SRT on 443 beside HTTPS on 443 is how an install gets through a
	// firewall that allows nothing else. Refusing it would refuse a
	// configuration that works.
	cur.Listeners.SRTPort = httpPort
	send(t, h, sign, http.MethodPut, "/api/v1/settings", cur, http.StatusOK)
}

// UNTICKING A MATRIX CELL AND SAVING HAS TO TURN THE ACTION OFF.
//
// It returned 200 and GET /settings still reported the cell on, because the
// save decodes over the stored document and json.Unmarshal MERGES maps -- while
// the matrix is sparse, where absent means off. The ban kept firing until
// somebody restarted the server, and every screen said it was off.
//
// Mutation: delete the `if len(sent.On) > 0 { a.On = nil }` branch from
// db.AutomodSettings.UnmarshalJSON. Observed to fail with "twitch/ban/links is
// still on after a save that unticked it".
func TestSettingsUnticksAnAutomodMatrixCell(t *testing.T) {
	_, h, _, sign := managerServer(t, defaultTools())

	var cur db.Settings
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &cur)

	cur.Automod.On = map[string]bool{
		"twitch/ban/links":    true,
		"twitch/timeout/caps": true,
	}
	send(t, h, sign, http.MethodPut, "/api/v1/settings", cur, http.StatusOK)

	readBack := func() db.Settings {
		t.Helper()
		var got db.Settings
		decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &got)
		return got
	}
	if got := readBack(); !got.Automod.On["twitch/ban/links"] {
		t.Fatal("the cell never went on, so the untick below would pass for the wrong reason")
	}

	delete(cur.Automod.On, "twitch/ban/links")
	send(t, h, sign, http.MethodPut, "/api/v1/settings", cur, http.StatusOK)

	got := readBack()
	if got.Automod.On["twitch/ban/links"] {
		t.Fatal("twitch/ban/links is still on after a save that unticked it, " +
			"so the ban keeps firing while the settings page draws the cell empty")
	}
	// THE CONTROL. A save that cleared the whole matrix would satisfy the
	// assertion above and quietly disarm every other cell the operator left
	// alone.
	if !got.Automod.On["twitch/timeout/caps"] {
		t.Fatal("twitch/timeout/caps went off too; unticking one cell cleared the matrix")
	}

	// AND THE LAST CELL CAN BE TURNED OFF. With `on` omitempty an empty matrix
	// marshalled to nothing, nothing decoded as "not touching the matrix", and
	// the final untick was the one save that silently did not take.
	cur.Automod.On = map[string]bool{}
	send(t, h, sign, http.MethodPut, "/api/v1/settings", cur, http.StatusOK)
	if len(readBack().Automod.On) != 0 {
		t.Fatalf("cells survived a save that cleared every one of them: %v",
			readBack().Automod.On)
	}
}
