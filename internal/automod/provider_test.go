package automod

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Every preset resolves to itself under its own protocol, a preset named
// under the wrong protocol falls back to that protocol's Custom (its JSON mode
// and lock describe a different wire), and only the two local presets are
// flag-only.
func TestPresetsResolveAndOnlyLocalOnesAreFlagOnly(t *testing.T) {
	local := map[string]bool{}
	for _, p := range Presets() {
		if got := ResolvePreset(p.Protocol, p.ID); got.ID != p.ID {
			t.Errorf("%s resolved to %s", p.ID, got.ID)
		}
		if p.Local {
			local[p.ID] = true
			if p.Privacy != PrivacyLocal {
				t.Errorf("%s is local but its privacy is %s", p.ID, p.Privacy)
			}
		}
		if p.Protocol == ProtocolOpenAI && p.JSONMode == "" {
			t.Errorf("%s has no JSON mode", p.ID)
		}
	}
	if len(local) != 2 || !local["ollama"] || !local["jev-ollama"] {
		t.Fatalf("local presets = %v", local)
	}
	if got := ResolvePreset(ProtocolJev, "cerebras"); got.ID != "jev-custom" {
		t.Fatalf("a Jev checker on the cerebras preset resolved to %s", got.ID)
	}
	if got := ResolvePreset(ProtocolOpenAI, ""); got.ID != "custom" {
		t.Fatalf("an empty preset resolved to %s", got.ID)
	}
	if p, _ := PresetByID("cerebras"); p.ReasoningEffort != "none" || p.JSONMode != JSONModeSchema {
		t.Fatalf("cerebras preset %+v", p)
	}
}

// An OpenAI-compatible preset's JSON mode and reasoning setting reach the
// wire: a strict json_schema whose category enum is ModelCategories(), and
// reasoning_effort only when the preset names one.
func TestOpenAIPresetSendsItsJSONModeAndReasoningSetting(t *testing.T) {
	var got map[string]any
	srv := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = nil
		json.NewDecoder(r.Body).Decode(&got)
		respondVerdict(false, 0.1, "")(w, r)
	})
	m := testModel(t, srv, func(c *ModelConfig) { c.JSONMode = JSONModeSchema; c.ReasoningEffort = "none" })
	if _, err := m.Check(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if got["reasoning_effort"] != "none" {
		t.Fatalf("reasoning_effort = %v", got["reasoning_effort"])
	}
	rf := got["response_format"].(map[string]any)
	if rf["type"] != "json_schema" {
		t.Fatalf("response_format = %v", rf)
	}
	js := rf["json_schema"].(map[string]any)
	if js["strict"] != true {
		t.Fatalf("schema is not strict: %v", js)
	}
	enum := js["schema"].(map[string]any)["properties"].(map[string]any)["category"].(map[string]any)["enum"].([]any)
	if len(enum) != len(ModelCategories()) {
		t.Fatalf("category enum %v is not ModelCategories()", enum)
	}

	// The legacy shape, unchanged: json_object and no reasoning_effort.
	m = testModel(t, srv, nil)
	m.Check(context.Background(), "x")
	if _, ok := got["reasoning_effort"]; ok {
		t.Fatal("reasoning_effort sent with no preset asking for it")
	}
	if got["response_format"].(map[string]any)["type"] != "json_object" {
		t.Fatalf("legacy response_format = %v", got["response_format"])
	}
}

// Per-category actions on the OpenAI-compatible checker, the legacy single
// action as the fallback for a category the table does not name, and the
// local lock.
func TestOpenAICategoryActionsLegacyFallbackAndLocalLock(t *testing.T) {
	for _, tc := range []struct {
		cat      string
		legacy   Action
		flagOnly bool
		want     Action
	}{
		{"spam", ActionFlag, false, ActionDelete},
		{"threat", ActionFlag, false, ActionTimeout},
		{"other", ActionFlag, false, ActionFlag},
		// An upgraded install that had chosen timeout keeps it for every
		// category the table does not name.
		{"other", ActionTimeout, false, ActionTimeout},
		{"spam", ActionFlag, true, ActionFlag},
	} {
		srv := modelServer(t, respondCategorised(true, 0.95, tc.cat, ""))
		f, err := testModel(t, srv, func(c *ModelConfig) {
			c.Action = tc.legacy
			c.FlagOnly = tc.flagOnly
			c.CategoryActions = map[Category]Action{CategorySpam: ActionDelete, CategoryThreat: ActionTimeout}
		}).Check(context.Background(), "x")
		if err != nil || len(f) != 1 || f[0].Action != tc.want {
			t.Errorf("%+v: got %+v, %v", tc, f, err)
		}
	}
	// An unknown action in the table is not honoured.
	if a := categoryAction(ActionFlag, map[Category]Action{CategorySpam: "teleport"}, false, CategorySpam); a != ActionFlag {
		t.Fatalf("an unknown table action was honoured: %s", a)
	}
}

// The Test button: passes a working checker, and names the first problem for
// each of the ways a checker can look healthy while being useless.
func TestProbePassesAWorkingCheckerAndNamesEachFailure(t *testing.T) {
	// A checker that flags only the abusive line.
	smart := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		abusive := strings.Contains(body.Messages[1].Content, "worthless")
		respondVerdict(abusive, 0.95, "")(w, r)
	})
	if res := Probe(context.Background(), testModel(t, smart, nil)); !res.Passed || len(res.Cases) != len(ProbeCases) {
		t.Fatalf("a working checker failed the probe: %+v", res)
	}

	for _, tc := range []struct {
		want ProbeProblem
		h    http.HandlerFunc
	}{
		{ProbeErrored, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }},
		{ProbeHarmlessFlagged, respondVerdict(true, 0.99, "")},
		{ProbeAbusiveMissed, respondVerdict(false, 0.99, "")},
		// The guard-model shape the spike found: valid JSON, wrong object.
		{ProbeErrored, respondContent(`{"No": null}`)},
	} {
		want, h := tc.want, tc.h
		res := Probe(context.Background(), testModel(t, modelServer(t, h), nil))
		if res.Passed || res.Problem != want {
			t.Errorf("want %s, got %+v", want, res)
		}
	}
}
