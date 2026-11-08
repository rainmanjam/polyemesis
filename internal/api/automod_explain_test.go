package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/automod"
)

type explainResponse struct {
	Protection    bool `json:"protection"`
	Held          bool `json:"held"`
	WouldHoldIfOn bool `json:"wouldHoldIfOn"`
	Windowed      []string
	Detections    []struct {
		Checker  string `json:"checker"`
		Category string `json:"category"`
		Detector string `json:"detector"`
		Tier     string `json:"tier"`
		Form     string `json:"form"`
		Reason   string `json:"reason"`
		Hold     bool   `json:"hold"`
		Action   string `json:"action"`
		Act      bool   `json:"act"`
		Live     bool   `json:"live"`
	} `json:"detections"`
}

func TestAutomodExplainRunsThePackAndNamesDetectorsOnly(t *testing.T) {
	h, _, sign := sourceServer(t)
	secret := "someone.private@example.com"
	raw := send(t, h, sign, http.MethodPost, "/api/v1/automod/explain", map[string]any{
		"text": "mail me at " + secret + " from kestrel_demo's twin", "authorName": "kestrel_demo_official",
		"platform": "Twitch", "protect": []string{"kestrel_demo"},
	}, http.StatusOK)
	if strings.Contains(string(raw), secret) {
		t.Fatalf("the response carries the matched value: %s", raw)
	}
	var got explainResponse
	decodeInto(t, raw, &got)
	// The pack is not on in the live engine, so chat would hold nothing for it:
	// the test box must say so and keep the pack's view apart.
	if got.Protection || got.Held || !got.WouldHoldIfOn {
		t.Fatalf("explanation claims live behaviour the engine does not have: %s", raw)
	}
	seen := map[string]bool{}
	for _, d := range got.Detections {
		seen[d.Detector] = true
		if d.Checker != "protection" || d.Action != "flag" || !d.Hold {
			t.Errorf("detection %+v", d)
		}
		if d.Live {
			t.Errorf("a pack detection is marked live while the pack is off: %+v", d)
		}
		// Act follows the stored matrix, where a fresh install arms nothing:
		// a flag is recorded whatever the cell says.
		if d.Act {
			t.Errorf("nothing is armed on a fresh install, yet %+v would act", d)
		}
	}
	if !seen["doxxing/email"] || !seen["impersonation"] {
		t.Errorf("detections: %v", seen)
	}
}

func TestAutomodExplainIsRefusedForBadInput(t *testing.T) {
	h, _, sign := sourceServer(t)
	for _, body := range map[string]any{
		"too long":       map[string]any{"text": strings.Repeat("a", explainMaxRunes+1)},
		"unknown":        map[string]any{"text": "x", "platform": "myspace"},
		"too many names": map[string]any{"text": "x", "protect": make([]string, 51)},
		"unknown field":  map[string]any{"text": "x", "strikes": 3},
		"not an object":  []int{1},
	} {
		send(t, h, sign, http.MethodPost, "/api/v1/automod/explain", body, http.StatusBadRequest)
	}
}

// The test box records nothing: a message that would raid, repeat or flood if it
// were observed leaves no window behind however often it is sent.
func TestAutomodExplainDoesNotObserve(t *testing.T) {
	h, _, sign := sourceServer(t)
	for i := 0; i < 25; i++ {
		var got explainResponse
		decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/automod/explain", map[string]any{
			"text": "everyone type this exact message in chat right now", "authorName": "viewer",
		}, http.StatusOK), &got)
		for _, d := range got.Detections {
			if d.Detector == "raid" || d.Detector == "spam_near_duplicate" || d.Detector == "repeats" {
				t.Fatalf("a windowed detector answered a single pretend message: %+v", d)
			}
		}
	}
}

func TestAProtectionCellCannotBeArmedYet(t *testing.T) {
	h, _, sign := sourceServer(t)
	var doc map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &doc)
	am := doc["automod"].(map[string]any)
	on, _ := am["on"].(map[string]any)
	if on == nil {
		on = map[string]any{}
		am["on"] = on
	}
	on["twitch/ban/protection"] = true
	body := send(t, h, sign, http.MethodPut, "/api/v1/settings", doc, http.StatusBadRequest)
	if !strings.Contains(string(body), "twitch/ban/protection") || !strings.Contains(string(body), "recommended protection is not on") {
		t.Errorf("the refusal does not name the cell and why: %s", body)
	}
}

func TestTheMatrixCarriesTheProtectionColumnAndNothingIsArmed(t *testing.T) {
	h, _, sign := sourceServer(t)
	var got struct {
		Checkers []string `json:"checkers"`
		Summary  map[string]int
		Cells    []struct {
			Checker string `json:"checker"`
			Action  string `json:"action"`
			Auto    bool   `json:"auto"`
		} `json:"cells"`
	}
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/automod/matrix", nil, http.StatusOK), &got)
	has := false
	for _, c := range got.Checkers {
		has = has || c == string(automod.CheckerProtection)
	}
	if !has {
		t.Errorf("checkers: %v", got.Checkers)
	}
	for _, c := range got.Cells {
		if c.Checker == "protection" && c.Auto && c.Action != "flag" {
			t.Errorf("a fresh install has %s/protection armed", c.Action)
		}
	}
	for p, n := range got.Summary {
		if n != 0 {
			t.Errorf("%s: %d automatic actions on a fresh install", p, n)
		}
	}
}

// The test box is SESSION-ONLY (spec 7.3): it answers from the operator's own
// rule names and pack policy, so no API token, admin or not, may read it.
func TestAnAPITokenCannotUseTheExplainTestBox(t *testing.T) {
	h, _, sign := sourceServer(t)
	plaintext := createToken(t, h, sign, "automation")
	r := jsonRequest(t, http.MethodPost, "/api/v1/automod/explain", map[string]any{"text": "hello there"})
	r.Header.Set("Authorization", "Bearer "+plaintext)
	if w := do(t, h, r); w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
		t.Fatalf("an API token used the test box: %d %s", w.Code, w.Body.String())
	}
}
