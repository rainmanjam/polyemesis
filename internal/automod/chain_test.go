package automod

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// openAIServer is a fake OpenAI-compatible secondary that counts calls.
func openAIServer(t *testing.T, abusive bool, confidence float64, category string) (*Model, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	h := respondCategorised(abusive, confidence, category, "fake")
	srv := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		h(w, r)
	})
	m := testModel(t, srv, func(c *ModelConfig) {
		c.Preset = "cerebras"
		c.CategoryActions = map[Category]Action{CategorySpam: ActionDelete}
	})
	return m, &calls
}

// A primary that answers decides, and the secondary is never called -- not
// for a hit, and NOT FOR A CLEAN ANSWER either. A clean answer is a decision;
// asking again would double the spend for a second opinion the matrix cannot
// weigh.
func TestChainPrimaryAnswerDecidesAndTheSecondaryIsNeverCalled(t *testing.T) {
	for name, a := range map[string]jevAnswer{
		"hit":       {pAct: 0.95, choice: "spam"},
		"flag band": {pAct: 0.6, choice: "spam"},
		"clean":     {pAct: 0.05, choice: "ok"},
	} {
		psrv, _ := jevServer(t, a)
		sec, secCalls := openAIServer(t, true, 0.99, "spam")
		c := NewChain(testJev(psrv, nil), sec)
		f, by, err := c.Decide(context.Background(), "a message")
		if err != nil || by != SlotPrimary {
			t.Errorf("%s: by=%q err=%v", name, by, err)
		}
		if secCalls.Load() != 0 {
			t.Errorf("%s: the secondary was called %d times after the primary answered", name, secCalls.Load())
		}
		for _, x := range f {
			if x.DecidedBy != SlotPrimary {
				t.Errorf("%s: finding not tagged as the primary's: %+v", name, x)
			}
		}
		if s := c.Stats(); s.Fallbacks != 0 || s.Primary.Decisions != 1 {
			t.Errorf("%s: stats %+v", name, s)
		}
	}
}

// EVERY FAILURE MODE HANDS THE SAME MESSAGE TO THE SECONDARY: timeout, error
// status, malformed reply, and the primary's own budget spent. The secondary's
// answer is acted on and tagged as the secondary's; the primary's failure is
// still counted against the primary.
func TestChainEachPrimaryFailureFallsBackToTheSecondary(t *testing.T) {
	cases := map[string]func(t *testing.T) ModelChecker{
		"timeout": func(t *testing.T) ModelChecker {
			srv := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-r.Context().Done():
				case <-time.After(2 * time.Second):
				}
			})
			return testJev(srv, func(c *JevConfig) { c.Timeout = 50 * time.Millisecond })
		},
		"error status": func(t *testing.T) ModelChecker {
			srv := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			})
			return testJev(srv, nil)
		},
		"malformed reply": func(t *testing.T) ModelChecker {
			srv, _ := jevServer(t, jevAnswer{omitAct: true, choice: "ok"})
			return testJev(srv, nil)
		},
		"budget spent": func(t *testing.T) ModelChecker {
			srv, _ := jevServer(t, jevAnswer{pAct: 0.01, choice: "ok"})
			j := testJev(srv, func(c *JevConfig) { c.MaxCallsPerHour = 1 })
			j.Check(context.Background(), "uses the only call")
			return j
		},
	}
	for name, primary := range cases {
		t.Run(name, func(t *testing.T) {
			p := primary(t)
			before := p.Stats().Failures
			sec, secCalls := openAIServer(t, true, 0.95, "spam")
			c := NewChain(p, sec)
			f, by, err := c.Decide(context.Background(), "buy followers at example.test")
			if err != nil {
				t.Fatalf("the secondary answered, but Decide failed: %v", err)
			}
			if by != SlotSecondary || secCalls.Load() != 1 {
				t.Fatalf("by=%q secondary calls=%d; want the secondary, once", by, secCalls.Load())
			}
			if len(f) != 1 || f[0].DecidedBy != SlotSecondary || f[0].Action != ActionDelete || f[0].Provider != "cerebras" {
				t.Fatalf("finding %+v; want the secondary's spam->delete", f)
			}
			s := c.Stats()
			if s.Fallbacks != 1 || s.Secondary.Decisions != 1 || s.LastDecidedBy != SlotSecondary {
				t.Fatalf("stats %+v", s)
			}
			// The budget case makes no call and so records no failure on the
			// connector -- the same as model.go's ceiling, which the stats
			// show as CallsThisHour == Ceiling instead.
			if name != "budget spent" && s.Primary.Failures != before+1 {
				t.Fatalf("the primary's failure was not counted: %+v", s.Primary)
			}
		})
	}
}

// Only when BOTH fail does the message pass, and the error names both.
func TestChainFailsOpenOnlyWhenBothCheckersFail(t *testing.T) {
	down := modelServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) })
	c := NewChain(testJev(down, nil), testModel(t, down, nil))
	f, by, err := c.Decide(context.Background(), "x")
	if err == nil || len(f) != 0 || by != "" {
		t.Fatalf("got %+v by=%q err=%v; want nothing and an error", f, by, err)
	}
	if !strings.Contains(err.Error(), "primary checker") || !strings.Contains(err.Error(), "secondary checker") {
		t.Fatalf("the error does not name both failures: %v", err)
	}
	if s := c.Stats(); s.FailedOpen != 1 || s.Fallbacks != 0 {
		t.Fatalf("stats %+v", s)
	}
}

// With no secondary configured -- nil, or present but switched off -- a
// primary failure fails open exactly as it did before there were two.
func TestChainWithTheSecondaryDisabledFailsOpenOnThePrimaryAlone(t *testing.T) {
	down := modelServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
	sec, secCalls := openAIServer(t, true, 0.99, "spam")
	sec.cfg.Enabled = false
	for name, s := range map[string]ModelChecker{"nil": nil, "disabled": sec} {
		c := NewChain(testJev(down, nil), s)
		if f, _, err := c.Decide(context.Background(), "x"); err == nil || len(f) != 0 {
			t.Errorf("%s: %+v %v", name, f, err)
		}
	}
	if secCalls.Load() != 0 {
		t.Fatal("a disabled secondary was called")
	}
}

// A primary switched off leaves the secondary as the only checker. Not a
// fallback -- there was nothing to fall back from -- so it is not counted as
// one.
func TestChainWithThePrimaryDisabledAsksTheSecondaryAlone(t *testing.T) {
	psrv, pCalls := jevServer(t, jevAnswer{pAct: 0.99, choice: "spam"})
	p := testJev(psrv, func(c *JevConfig) { c.Enabled = false })
	sec, secCalls := openAIServer(t, true, 0.99, "spam")
	c := NewChain(p, sec)
	_, by, err := c.Decide(context.Background(), "x")
	if err != nil || by != SlotSecondary || pCalls.Load() != 0 || secCalls.Load() != 1 {
		t.Fatalf("by=%q err=%v primary=%d secondary=%d", by, err, pCalls.Load(), secCalls.Load())
	}
	if c.Stats().Fallbacks != 0 {
		t.Fatal("a secondary answering alone was counted as a fallback")
	}
}

// Through the engine: the verdict says who decided, and the matrix still
// gates the secondary's finding under the one model column.
func TestEngineVerdictCarriesWhichCheckerDecided(t *testing.T) {
	down := modelServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(502) })
	sec, _ := openAIServer(t, true, 0.99, "spam")
	m := Matrix{Enabled: true}
	m.Set(Key{Platform: db.PlatformTwitch, Action: ActionDelete, Checker: CheckerModel}, true)
	e := New(m, PlatformCaps{}, nil, nil, NewChain(testJev(down, nil), sec))
	if !e.ModelEnabled() {
		t.Fatal("ModelEnabled is false with two checkers on")
	}
	v, err := e.CheckModel(context.Background(), db.PlatformTwitch, "x")
	if err != nil || v.DecidedBy != SlotSecondary || len(v.Act) != 1 {
		t.Fatalf("verdict %+v err %v", v, err)
	}
	b, _ := json.Marshal(v)
	if !strings.Contains(string(b), `"decidedBy":"secondary"`) {
		t.Fatalf("decidedBy is not on the wire: %s", b)
	}
	st := e.ModelStats()
	if st.Primary == nil || st.Secondary == nil || st.Fallbacks != 1 || st.Primary.Protocol != ProtocolJev {
		t.Fatalf("engine stats %+v", st)
	}
}
