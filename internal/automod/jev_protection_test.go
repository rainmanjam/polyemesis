package automod

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func criteriaOf(t *testing.T, selfHarm, compact bool) map[string]any {
	t.Helper()
	body := jevRequestBody("m", "rule", "hi", compact, selfHarm)
	b, _ := json.Marshal(body)
	var got map[string]any
	_ = json.Unmarshal(b, &got)
	return got["questions"].(map[string]any)["category"].(map[string]any)["criteria"].(map[string]any)
}

// D14: self_harm is asked about only when asked for, in both request shapes,
// and the criterion carries the spec's wording.
func TestJevAsksSelfHarmOnlyForAProtectedInstall(t *testing.T) {
	for _, compact := range []bool{false, true} {
		off := criteriaOf(t, false, compact)
		if _, ok := off[string(CategorySelfHarm)]; ok || len(off) != len(ModelCategories())+1 {
			t.Fatalf("compact=%v: an unprotected request asks about self_harm: %v", compact, off)
		}
		on := criteriaOf(t, true, compact)
		if len(on) != len(ModelCategories())+2 {
			t.Fatalf("compact=%v: protected criteria = %v", compact, on)
		}
		want := "Expresses intent or wish to self-harm"
		if compact {
			want = "self-harm"
		}
		if got, _ := on[string(CategorySelfHarm)].(string); !strings.Contains(got, want) {
			t.Fatalf("compact=%v: self_harm criterion = %q", compact, got)
		}
	}
}

// The compact request must stay inside laya's window: the self_harm line is a
// handful of tokens, and the whole compact body stays short.
func TestJevCompactProtectedRequestStaysSmall(t *testing.T) {
	plain, _ := json.Marshal(jevRequestBody("m", "", "x", true, false))
	prot, _ := json.Marshal(jevRequestBody("m", "", "x", true, true))
	if d := len(prot) - len(plain); d <= 0 || d > 60 {
		t.Fatalf("the compact self_harm addition is %d bytes, want 1-60 (about 4 tokens)", d)
	}
}

func TestJevParsesSelfHarmOnlyWhenOffered(t *testing.T) {
	v := JevVerdict{PAct: 0.95, Choice: "self_harm"}
	if _, _, err := jevDecide(v, 0.8, ModelCategories()); err == nil {
		t.Fatal("an unoffered category was accepted")
	}
	cat, band, err := jevDecide(v, 0.8, ModelCategoriesFor(true))
	if err != nil || cat != CategorySelfHarm || band != jevBandAct {
		t.Fatalf("got %v %v %v", cat, band, err)
	}
	// "ok" with a self_harm lead picks the offered category with the top probability.
	v = JevVerdict{PAct: 0.9, Choice: "ok", PCategory: map[string]float64{"self_harm": 0.7, "harassment": 0.2}}
	if cat, _, _ := jevDecide(v, 0.8, ModelCategoriesFor(true)); cat != CategorySelfHarm {
		t.Fatalf("top-probability fallback = %v", cat)
	}
	if cat, _, _ := jevDecide(v, 0.8, ModelCategories()); cat != CategoryHarassment {
		t.Fatalf("unprotected fallback = %v", cat)
	}
	if !KnownCategory(CategorySelfHarm) {
		t.Fatal("self_harm is not a known category")
	}
}

func jevWith(t *testing.T, a jevAnswer, tweak func(*JevConfig)) *Jev {
	t.Helper()
	srv, _ := jevServer(t, a)
	return testJev(srv, tweak)
}

// A self_harm finding is flag-only whatever the checker's action says, and a
// hold at or above the floor.
func TestJevSelfHarmIsNeverAboveFlag(t *testing.T) {
	a := jevAnswer{pAct: 0.97, choice: "self_harm", probs: map[string]float64{"self_harm": 0.9}}
	j := jevWith(t, a, func(c *JevConfig) {
		c.Action = ActionBan
		c.CategoryActions = map[Category]Action{CategorySelfHarm: ActionBan}
		c.SelfHarm, c.HoldOverlay = true, true
	})
	fs, err := j.Check(context.Background(), "i want to die")
	if err != nil || len(fs) != 1 {
		t.Fatalf("%v %v", fs, err)
	}
	if fs[0].Action != ActionFlag || !fs[0].Hold || fs[0].Category != CategorySelfHarm {
		t.Fatalf("finding = %+v", fs[0])
	}
}

func TestJevHoldFollowsTheBandAndTheCategory(t *testing.T) {
	cases := []struct {
		name string
		pAct float64
		cat  string
		hold bool
		on   bool
	}{
		{"above the floor holds", 0.95, "harassment", true, true},
		{"the flag band does not hold", 0.6, "harassment", false, true},
		{"other does not hold", 0.95, "other", false, true},
		{"spam holds", 0.95, "spam", true, true},
		{"pack off never holds", 0.95, "hate_speech", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			j := jevWith(t, jevAnswer{pAct: c.pAct, choice: c.cat}, func(cfg *JevConfig) { cfg.HoldOverlay = c.on })
			fs, err := j.Check(context.Background(), "x")
			if err != nil || len(fs) != 1 {
				t.Fatalf("%v %v", fs, err)
			}
			if fs[0].Hold != c.hold {
				t.Fatalf("hold = %v, want %v (%+v)", fs[0].Hold, c.hold, fs[0])
			}
		})
	}
}

func statusJev(t *testing.T, code int, body string, tweak func(*JevConfig)) error {
	t.Helper()
	srv := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	})
	_, err := testJev(srv, tweak).Check(context.Background(), "hello")
	if err == nil {
		t.Fatal("no error")
	}
	return err
}

// The operator reads these on the Test button. They are fixed sentences and
// never the reply body.
func TestJevOperatorErrorsAreFriendlyAndNeverEchoTheBody(t *testing.T) {
	marker := strings.Repeat("z", 8) + "-fake-body-marker"
	err := statusJev(t, http.StatusUnauthorized, `{"error":"bad key `+marker+`"}`, nil)
	if err.Error() != "Jev rejected the API key -- paste a new one" {
		t.Fatalf("401 = %q", err)
	}

	err = statusJev(t, http.StatusBadRequest, `{"error":{"message":"unknown model `+marker+`"}}`,
		func(c *JevConfig) { c.Model = "jev-9.9" })
	if got := err.Error(); got != `Jev does not recognise model "jev-9.9" -- check the model name` || strings.Contains(got, marker) {
		t.Fatalf("400 model = %q", got)
	}

	// A 400 that does not mention the model keeps the status-only sentence.
	err = statusJev(t, http.StatusBadRequest, `{"error":"state too long `+marker+`"}`, nil)
	if err.Error() != "Jev API returned 400" {
		t.Fatalf("400 other = %q", err)
	}
	// A 400 that mentions the model for another reason is NOT a wrong model name.
	for _, body := range []string{
		`{"error":{"message":"input exceeds the model's context window ` + marker + `"}}`,
		`{"error":"text is too long for model jev-1"}`,
		`{"error":"the model is busy, try again"}`,
	} {
		err = statusJev(t, http.StatusBadRequest, body, func(c *JevConfig) { c.Model = "jev-1" })
		if err.Error() != "Jev API returned 400" {
			t.Fatalf("400 about something else = %q", err)
		}
	}
	// And the real wordings still are.
	for _, body := range []string{
		`{"error":"model not found"}`, `{"detail":"Model 'x' does not exist"}`,
		`{"error":"invalid model"}`, `{"error":{"code":"model_not_found"}}`, `{"error":"Unknown model: x"}`,
	} {
		err = statusJev(t, http.StatusBadRequest, body, func(c *JevConfig) { c.Model = "jev-1" })
		if !strings.HasPrefix(err.Error(), "Jev does not recognise model") {
			t.Fatalf("%s = %q", body, err)
		}
	}
	if err := statusJev(t, http.StatusInternalServerError, marker, nil); err.Error() != "Jev API returned 500" {
		t.Fatalf("500 = %q", err)
	}
}

func TestShownModelIsBoundedAndCleaned(t *testing.T) {
	got := shownModel("  a\nb\x00c" + strings.Repeat("x", 100))
	if strings.ContainsAny(got, "\n\x00") || len([]rune(got)) > 70 || !strings.HasSuffix(got, `..."`) {
		t.Fatalf("shownModel = %q", got)
	}
}

func TestOpenAIModelHoldFollowsTheConfig(t *testing.T) {
	for _, c := range []struct {
		on   bool
		cat  Category
		want bool
	}{{true, CategoryHarassment, true}, {true, CategoryOther, false}, {false, CategoryHarassment, false}} {
		srv := modelServer(t, respondCategorised(true, 0.95, string(c.cat), "x"))
		m := testModel(t, srv, func(cfg *ModelConfig) { cfg.HoldOverlay = c.on })
		fs, err := m.Check(context.Background(), "hello")
		if err != nil || len(fs) != 1 {
			t.Fatalf("%+v: %v %v", c, fs, err)
		}
		if fs[0].Hold != c.want {
			t.Fatalf("%+v: hold = %v", c, fs[0].Hold)
		}
	}
}
