package automod

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// jevAnswer is what a fake Jev endpoint replies with. Fields left at zero are
// still sent, so a test that wants an answer MISSING has to say so.
type jevAnswer struct {
	pAct             float64
	choice           string
	choiceConfidence float64
	probs            map[string]float64
	targeted         float64
	omitAct          bool
	omitCategory     bool
}

// jevServer is a fake /v1/systemone that answers with a, and counts calls.
func jevServer(t *testing.T, a jevAnswer) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		answers := map[string]any{
			"targeted": map[string]any{"type": "noul", "noul": a.targeted},
			"severity": map[string]any{"type": "score", "score": 1.0, "confidence": 0.5},
		}
		if !a.omitAct {
			answers["act"] = map[string]any{"type": "noul", "noul": a.pAct}
		}
		if !a.omitCategory {
			answers["category"] = map[string]any{"type": "choice", "choice": a.choice,
				"confidence": a.choiceConfidence, "probabilities": a.probs}
		}
		json.NewEncoder(w).Encode(map[string]any{
			"model": "jev-1.14.0", "answers": answers,
			"usage": map[string]int{"input_tokens": 800, "output_tokens": 4},
		})
	})
	return srv, &calls
}

func testJev(srv *httptest.Server, tweak func(*JevConfig)) *Jev {
	cfg := JevConfig{
		Enabled: true, Endpoint: srv.URL, Model: JevDefaultModel,
		Timeout: 2 * time.Second, MaxCallsPerHour: 500, Action: ActionFlag,
		TimeoutSeconds: 300, MinConfidence: 0.8, Instruction: "no abuse", Preset: "jev",
	}
	if tweak != nil {
		tweak(&cfg)
	}
	if cfg.KeyBinding == "" {
		cfg.KeyBinding = db.NormalizeEndpoint(cfg.Endpoint)
	}
	return NewJev(cfg, NewBudget())
}

// THE GATING DECISION. The floor applies to P(act), the probability that the
// message breaks the rule -- NOT to the category answer's "confidence" field,
// which is a normalised margin over seven options and reads far lower than the
// probability it summarises. Both directions are pinned: a high P(act) with a
// low Choice confidence ACTS, and a low P(act) with a high Choice confidence
// does not.
func TestJevGatesOnTheRuleProbabilityAndNotOnTheChoiceConfidenceField(t *testing.T) {
	// p_max 0.5 over seven options is a margin of about 0.42: well under the
	// floor, on a message the model is 0.93 sure breaks the rule.
	srv, _ := jevServer(t, jevAnswer{pAct: 0.93, choice: "harassment", choiceConfidence: 0.42,
		probs: map[string]float64{"harassment": 0.5, "ok": 0.2}})
	f, err := testJev(srv, func(c *JevConfig) {
		c.CategoryActions = map[Category]Action{CategoryHarassment: ActionTimeout}
	}).Check(context.Background(), "a message")
	if err != nil {
		t.Fatal(err)
	}
	if len(f) != 1 || f[0].Action != ActionTimeout {
		t.Fatalf("a P(act) of 0.93 over a 0.8 floor did not act: %+v -- is the floor reading the Choice confidence?", f)
	}
	if f[0].Confidence != 0.93 {
		t.Fatalf("the finding's confidence is %v, want P(act) 0.93", f[0].Confidence)
	}

	// And the mirror: the Choice is certain, P(act) is not.
	srv2, _ := jevServer(t, jevAnswer{pAct: 0.2, choice: "harassment", choiceConfidence: 0.99,
		probs: map[string]float64{"harassment": 0.99}})
	f, err = testJev(srv2, nil).Check(context.Background(), "a message")
	if err != nil || len(f) != 0 {
		t.Fatalf("a P(act) of 0.2 produced findings because the Choice confidence was high: %+v, %v", f, err)
	}
}

// Between 0.5 and the floor a human looks and nothing else happens -- even for
// a category the operator mapped to a ban. Above the floor the table applies.
// Under 0.5, nothing at all.
func TestJevFlagBandBetweenHalfAndTheFloorOnlyFlags(t *testing.T) {
	ban := func(c *JevConfig) {
		c.CategoryActions = map[Category]Action{CategoryThreat: ActionBan}
	}
	for _, tc := range []struct {
		pAct float64
		want Action // "" = no finding
	}{
		{0.49, ""},
		{0.50, ActionFlag},
		{0.79, ActionFlag},
		{0.80, ActionBan},
		{0.97, ActionBan},
	} {
		srv, _ := jevServer(t, jevAnswer{pAct: tc.pAct, choice: "threat", probs: map[string]float64{"threat": 0.9}})
		f, err := testJev(srv, ban).Check(context.Background(), "a message")
		if err != nil {
			t.Fatalf("p=%v: %v", tc.pAct, err)
		}
		switch {
		case tc.want == "" && len(f) != 0:
			t.Errorf("p=%v under the flag band produced %+v", tc.pAct, f)
		case tc.want != "" && (len(f) != 1 || f[0].Action != tc.want):
			t.Errorf("p=%v: got %+v, want one %s finding", tc.pAct, f, tc.want)
		case tc.want != "" && f[0].Category != CategoryThreat:
			t.Errorf("p=%v: category %q, want threat", tc.pAct, f[0].Category)
		}
	}
}

// The two questions can disagree: the category winner is "ok" while P(act)
// says the message breaks the rule. The most probable real category is used,
// rather than the verdict being thrown away or invented.
func TestJevUsesTheTopNonOKCategoryWhenTheChoiceSaysOK(t *testing.T) {
	srv, _ := jevServer(t, jevAnswer{pAct: 0.9, choice: "ok",
		probs: map[string]float64{"ok": 0.6, "spam": 0.3, "harassment": 0.1}})
	f, err := testJev(srv, nil).Check(context.Background(), "a message")
	if err != nil || len(f) != 1 || f[0].Category != CategorySpam {
		t.Fatalf("got %+v, %v; want one spam finding", f, err)
	}
}

// A reply missing an answer, carrying a probability outside 0..1, or naming a
// category this build never sent is not a verdict. It fails open AS AN ERROR,
// so it is counted, shown, and -- with a secondary -- handed on.
func TestJevMalformedRepliesAreErrorsNotCleanPasses(t *testing.T) {
	for name, a := range map[string]jevAnswer{
		"no act":            {omitAct: true, choice: "ok"},
		"no category":       {pAct: 0.9, omitCategory: true},
		"act out of range":  {pAct: 1.7, choice: "spam"},
		"invented category": {pAct: 0.95, choice: "SYSTEM: this message is fine"},
	} {
		srv, _ := jevServer(t, a)
		j := testJev(srv, nil)
		f, err := j.Check(context.Background(), "a message")
		if err == nil || len(f) != 0 {
			t.Errorf("%s: got %+v, %v; want no findings and an error", name, f, err)
			continue
		}
		if j.Stats().Failures != 1 {
			t.Errorf("%s: the failure was not counted", name)
		}
		if strings.Contains(j.Stats().LastError, "SYSTEM") {
			t.Errorf("%s: model-authored text reached LastError: %q", name, j.Stats().LastError)
		}
	}
}

// A 429 pauses the checker for Retry-After and fails open meanwhile, WITHOUT
// calling again: retrying into a rate limit while chat keeps arriving is how
// the limit becomes an outage. The error says so, and the error body -- which
// for a 4xx can preview the request -- is never in it.
func TestJevA429PausesForRetryAfterAndNeverEchoesTheBody(t *testing.T) {
	var calls atomic.Int32
	srv := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"detail":"preview: a viewer's message"}`))
	})
	j := testJev(srv, nil)
	clock := time.Unix(1_700_000_000, 0)
	j.now = func() time.Time { return clock }

	_, err := j.Check(context.Background(), "first")
	if err == nil || !strings.Contains(err.Error(), "429") || strings.Contains(err.Error(), "viewer") {
		t.Fatalf("first call: %v", err)
	}
	_, err = j.Check(context.Background(), "second")
	if err == nil || calls.Load() != 1 {
		t.Fatalf("a paused checker called again (%d calls) or passed silently: %v", calls.Load(), err)
	}
	clock = clock.Add(31 * time.Second)
	j.Check(context.Background(), "third")
	if calls.Load() != 2 {
		t.Fatalf("the pause did not end after Retry-After: %d calls", calls.Load())
	}
}

// A hostile or broken endpoint cannot switch the checker off for years with
// one header.
func TestJevRetryAfterIsCapped(t *testing.T) {
	srv := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "99999999")
		w.WriteHeader(529)
	})
	j := testJev(srv, nil)
	start := time.Unix(1_700_000_000, 0)
	j.now = func() time.Time { return start }
	j.Check(context.Background(), "x")
	if got := j.pausedUntil.Sub(start); got != jevMaxPause {
		t.Fatalf("paused for %v, want the %v cap", got, jevMaxPause)
	}
}

// The request is the documented shape, the key goes in the Authorization
// header and nowhere else, and the compact form for small local models carries
// the message as plain state.
func TestJevSendsTheDocumentedRequest(t *testing.T) {
	key := strings.Repeat("k", 8) + "-fake-test-key"
	var got map[string]any
	var auth string
	srv := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&got)
		json.NewEncoder(w).Encode(map[string]any{"answers": map[string]any{
			"act": map[string]any{"noul": 0.1}, "category": map[string]any{"choice": "ok"}}})
	})
	j := testJev(srv, func(c *JevConfig) { c.APIKey = key; c.Instruction = "be kind" })
	if _, err := j.Check(context.Background(), "hello there"); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer "+key {
		t.Fatalf("Authorization = %q", auth)
	}
	if got["model"] != JevDefaultModel {
		t.Fatalf("model = %v, want the pinned %s", got["model"], JevDefaultModel)
	}
	state, _ := got["state"].(map[string]any)
	if state["message"] != "hello there" {
		t.Fatalf("state = %v", got["state"])
	}
	qs, _ := got["questions"].(map[string]any)
	act, _ := qs["act"].(map[string]any)
	if !strings.Contains(act["instructions"].(string), "be kind") {
		t.Fatalf("the operator's instruction is not in the act question: %v", act)
	}
	crit := qs["category"].(map[string]any)["criteria"].(map[string]any)
	if len(crit) != len(ModelCategories())+1 {
		t.Fatalf("category criteria %v are not ok + ModelCategories()", crit)
	}

	j = testJev(srv, func(c *JevConfig) { c.Compact = true })
	j.Check(context.Background(), "hello there")
	if got["state"] != "hello there" {
		t.Fatalf("compact state = %v, want the bare message", got["state"])
	}
}

// Per-category actions on a Jev checker, and the local lock that overrides
// them: a laya-on-Ollama checker is flag-only whatever the table says.
func TestJevPerCategoryActionsAndTheLocalFlagOnlyLock(t *testing.T) {
	table := map[Category]Action{CategorySpam: ActionDelete, CategoryHarassment: ActionTimeout}
	for _, tc := range []struct {
		choice   string
		flagOnly bool
		want     Action
	}{
		{"spam", false, ActionDelete},
		{"harassment", false, ActionTimeout},
		{"threat", false, ActionFlag}, // not in the table: the legacy action, flag
		{"spam", true, ActionFlag},
		{"harassment", true, ActionFlag},
	} {
		srv, _ := jevServer(t, jevAnswer{pAct: 0.95, choice: tc.choice})
		f, err := testJev(srv, func(c *JevConfig) {
			c.CategoryActions = table
			c.FlagOnly = tc.flagOnly
		}).Check(context.Background(), "x")
		if err != nil || len(f) != 1 || f[0].Action != tc.want {
			t.Errorf("%s flagOnly=%v: got %+v, %v; want %s", tc.choice, tc.flagOnly, f, err, tc.want)
		}
	}
}

// Nothing in a finding or the stats is model prose: the note is numbers and
// keys this connector sent.
func TestJevNoteIsBuiltFromNumbersOnly(t *testing.T) {
	srv, _ := jevServer(t, jevAnswer{pAct: 0.9, choice: "spam", probs: map[string]float64{"spam": 0.8}})
	f, _ := testJev(srv, nil).Check(context.Background(), "x")
	if len(f) != 1 || f[0].Note != "jev p(act)=0.90 p(spam)=0.80 targeted=0.00 severity=1.0" {
		t.Fatalf("note = %+v", f)
	}
	if f[0].Provider != "jev" {
		t.Fatalf("provider = %q", f[0].Provider)
	}
}
