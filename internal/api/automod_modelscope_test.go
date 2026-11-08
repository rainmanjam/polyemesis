package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/db/dbtest"
)

// jevCapture is a fake Jev endpoint that records the last request body and
// answers "ok" with a low probability.
func jevCapture(t *testing.T) (*httptest.Server, *atomic.Pointer[map[string]any]) {
	t.Helper()
	var last atomic.Pointer[map[string]any]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		_ = json.NewDecoder(r.Body).Decode(&got)
		last.Store(&got)
		_ = json.NewEncoder(w).Encode(map[string]any{"answers": map[string]any{
			"act":      map[string]any{"noul": 0.01},
			"category": map[string]any{"choice": "ok"},
		}})
	}))
	t.Cleanup(srv.Close)
	return srv, &last
}

func jevSettings(t *testing.T, endpoint string) db.AutomodSettings {
	t.Helper()
	a := db.DefaultSettings().Automod
	a.Enabled = true
	a.Model.Enabled = true
	a.Model.Endpoint = endpoint
	a.Protection.State = db.ProtectionOn
	a.Protection.ModelScope = db.ModelScopeHard
	return a
}

func applyJev(t *testing.T, a db.AutomodSettings) (*automod.Engine, *automod.Persistent) {
	t.Helper()
	store := dbtest.OpenCheap(t)
	if err := store.PutSettings(db.DefaultSettings()); err != nil {
		t.Fatal(err)
	}
	hub := newTestHub(t)
	ps := automod.NewPersistent()
	ApplyAutomod(hub, store, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), a,
		automod.NewBudget(), automod.NewBudget(), ps)
	e, ok := hub.Moderator().(*automod.Engine)
	if !ok {
		t.Fatalf("the moderator is %T", hub.Moderator())
	}
	return e, ps
}

// The stored scope reaches the engine, and the gate's state is the persistent
// one: a save does not hand a raid a fresh allowance.
func TestApplyAutomodAppliesTheModelScopeAndKeepsTheGate(t *testing.T) {
	srv, _ := jevCapture(t)
	a := jevSettings(t, srv.URL)
	e, ps := applyJev(t, a)
	if e.Scope() != automod.ScopeHard {
		t.Fatalf("scope = %q, want hard", e.Scope())
	}
	subj := automod.Subject{AuthorID: "u1", Text: "i will find you and kill you"}
	if e.WantsModel(db.PlatformTwitch, automod.Subject{AuthorID: "u2", Text: "gg"}, automod.Verdict{}) {
		t.Fatal("an ordinary line was asked about under hard")
	}
	if !e.WantsModel(db.PlatformTwitch, subj, automod.Verdict{}) {
		t.Fatal("a threat line was not asked about")
	}
	if ps.Gate.Stats().Asked != 1 {
		t.Fatalf("the persistent gate did not count the call: %+v", ps.Gate.Stats())
	}

	// All, and the empty value an upgraded blob could carry, ask about everything.
	for _, scope := range []string{db.ModelScopeAll, ""} {
		a.Protection.ModelScope = scope
		e2, _ := applyJev(t, a)
		if e2.Scope() != automod.ScopeAll || !e2.WantsModel(db.PlatformTwitch, automod.Subject{AuthorID: "u3", Text: "gg"}, automod.Verdict{}) {
			t.Fatalf("scope %q did not ask about every message", scope)
		}
	}
}

// D14 and 6.4 end to end: with the pack on the request carries self_harm; with
// it off the request is what it was before the pack.
func TestSelfHarmIsAskedOnlyWithProtectionOn(t *testing.T) {
	for _, on := range []bool{true, false} {
		srv, last := jevCapture(t)
		a := jevSettings(t, srv.URL)
		if !on {
			a.Protection.State = db.ProtectionOff
		}
		e, _ := applyJev(t, a)
		if _, err := e.CheckModel(context.Background(), db.PlatformTwitch, "hello there"); err != nil {
			t.Fatal(err)
		}
		body := last.Load()
		if body == nil {
			t.Fatal("the endpoint was never called")
		}
		crit := (*body)["questions"].(map[string]any)["category"].(map[string]any)["criteria"].(map[string]any)
		if _, has := crit["self_harm"]; has != on {
			t.Fatalf("protection on=%v but self_harm asked=%v (%v)", on, has, crit)
		}
	}
}

func TestHoldWhenUnavailableReachesTheEngine(t *testing.T) {
	srv, _ := jevCapture(t)
	a := jevSettings(t, srv.URL)
	e, _ := applyJev(t, a)
	if e.HoldOnModelError() {
		t.Fatal("hold-on-failure is on by default")
	}
	a.Model.HoldWhenUnavailable = true
	if e, _ = applyJev(t, a); !e.HoldOnModelError() {
		t.Fatal("the setting did not reach the engine")
	}
}

// self_harm is not an action category: a stored table naming it is refused.
func TestCategoryActionsRefuseSelfHarm(t *testing.T) {
	a := db.DefaultSettings().Automod
	a.Model.CategoryActions = map[string]string{"self_harm": "ban"}
	if err := automodProviderProblems(a); err == nil {
		t.Fatal("a self_harm action was accepted")
	}
}
