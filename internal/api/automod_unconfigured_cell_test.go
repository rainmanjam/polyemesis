package api

import (
	"net/http"
	"strings"
	"testing"
)

// A cell whose checker is not configured cannot be armed.
//
// THE BUG: PUT automod.on={"twitch/ban/model":true} with the model switched off
// answered 200 and stored the cell. The console renders that column inert --
// "nothing automatic" -- while the server's summary counted the ban and the
// banner above said an irreversible action was armed. And the stored cell was
// not dead: the moment somebody configured the model, it went live as a ban
// nobody had knowingly chosen.
func TestACellOnAnUnconfiguredCheckerCannotBeArmed(t *testing.T) {
	h, _, sign := sourceServer(t)

	settingsWith := func(edit func(am map[string]any)) map[string]any {
		var doc map[string]any
		decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &doc)
		am := doc["automod"].(map[string]any)
		am["enabled"] = true
		edit(am)
		return doc
	}

	// The model is off on a fresh install.
	body := send(t, h, sign, http.MethodPut, "/api/v1/settings", settingsWith(func(am map[string]any) {
		am["on"] = map[string]any{"twitch/ban/model": true}
	}), http.StatusBadRequest)
	if !strings.Contains(string(body), "twitch/ban/model") {
		t.Errorf("the refusal does not name the cell: %s", body)
	}

	// No rule is written on a fresh install either -- and a DISABLED rule is
	// no rule, because it matches nothing.
	send(t, h, sign, http.MethodPut, "/api/v1/settings", settingsWith(func(am map[string]any) {
		am["rules"] = []map[string]any{
			{"id": 1, "name": "off", "enabled": false, "pattern": "spam", "action": "delete"},
		}
		am["on"] = map[string]any{"twitch/delete/rules": true}
	}), http.StatusBadRequest)

	// Configuring the checker in the SAME save is what the console does, and
	// it has to work.
	send(t, h, sign, http.MethodPut, "/api/v1/settings", settingsWith(func(am map[string]any) {
		model := am["model"].(map[string]any)
		model["enabled"] = true
		model["endpoint"] = "https://model.example/v1/chat/completions"
		am["on"] = map[string]any{"twitch/ban/model": true}
	}), http.StatusOK)

	// Switching the model OFF with that ban armed is not refused -- the cell is
	// kept, so turning the model back on restores what the operator chose --
	// but while the model is off the cell cannot fire, and the summary the
	// banner is drawn from must not count it.
	var doc map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &doc)
	doc["automod"].(map[string]any)["model"].(map[string]any)["enabled"] = false
	send(t, h, sign, http.MethodPut, "/api/v1/settings", doc, http.StatusOK)

	var got struct {
		Summary map[string]int `json:"summary"`
	}
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/automod/matrix", nil, http.StatusOK), &got)
	if got.Summary["twitch"] != 0 {
		t.Errorf("the summary counts a ban over a switched-off model as armed (%d): the "+
			"banner says an irreversible action is armed while the console shows nothing "+
			"automatic, and neither is what the engine does", got.Summary["twitch"])
	}
}
