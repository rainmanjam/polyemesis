package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// CHANGING playout.sourceId MUST CHANGE WHAT THE PUBLIC PAGE SERVES, AT ONCE.
//
// The setting is documented as resolved per request (engine/reload.go files it
// under ClassOnDemand: no child restarts), and the playout manager itself does
// read its settings per request. The mount above it did not: playoutHandler
// built its inner handler inside a sync.Once from whichever engine
// playoutManager named at the FIRST public request after boot, and served that
// engine's directory until the process restarted. An operator who switched the
// audience to Studio B saw the save succeed, the console report Studio B, and
// the audience keep watching Studio A. Measured in Linux Docker as exploratory
// row #4: the switch took effect only after a restart.
//
// So this goes through the real surfaces end to end: two running engines, the
// settings PUT an operator makes, and the anonymous /playout/ mount an audience
// hits. The public request is made BEFORE the switch on purpose -- that is what
// froze the old once-built handler, and a test that switched before the first
// request would have passed against it.
//
// The marker files are written at each source's playout ROOT, not in a variant
// directory, because a variant directory is the engine's to clear on a restart
// of its muxer and the settings PUT is exactly what may restart one.
func TestChangingThePublishedProgrammeTakesEffectWithoutARestart(t *testing.T) {
	s, h, _, sign := managerServer(t, defaultTools())
	first := s.eng()
	if first == nil {
		t.Fatal("no default engine in the fixture")
	}
	second := secondProgramme(t, s)

	send(t, h, sign, http.MethodPut, "/api/v1/playout/publish",
		map[string]any{"protection": string(PlayoutProtectOpen)}, http.StatusOK)

	publish := func(sourceID int64) {
		t.Helper()
		var set map[string]any
		if err := json.Unmarshal(send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &set); err != nil {
			t.Fatalf("decode settings: %v", err)
		}
		p, _ := set["playout"].(map[string]any)
		if p == nil {
			t.Fatalf("settings carry no playout object: %v", set)
		}
		p["enabled"] = true
		p["public"] = true
		p["sourceId"] = sourceID
		send(t, h, sign, http.MethodPut, "/api/v1/settings", set, http.StatusOK)
	}

	const probe = "which-programme.m3u8"
	mark := func(sourceID int64, body string) {
		t.Helper()
		dir := s.cfg.PlayoutDirFor(sourceID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, probe), []byte(body), 0o644); err != nil {
			t.Fatalf("write marker: %v", err)
		}
	}
	// Anonymous: no sign, no token. The audience is the whole point.
	watch := func() string {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, PlayoutPrefix+probe, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("public GET %s: status %d, body %q", probe, w.Code, w.Body.String())
		}
		return strings.TrimSpace(w.Body.String())
	}

	publish(first.SourceID())
	mark(first.SourceID(), "studio-a")
	mark(second.ID, "studio-b")
	if got := watch(); got != "studio-a" {
		t.Fatalf("with playout.sourceId naming the first programme the audience got %q, "+
			"want studio-a -- the fixture is not showing what it means to", got)
	}

	publish(second.ID)
	// Re-marked in case the PUT's reconcile touched either directory.
	mark(first.SourceID(), "studio-a")
	mark(second.ID, "studio-b")
	if got := watch(); got != "studio-b" {
		t.Fatalf("after PUT /settings switched playout.sourceId to the second programme "+
			"the audience still got %q, want studio-b: the public mount is serving the "+
			"engine it resolved on its first request, not the one the settings name", got)
	}

	// And back: the fix must not be a one-shot either.
	publish(first.SourceID())
	mark(first.SourceID(), "studio-a")
	if got := watch(); got != "studio-a" {
		t.Fatalf("switching back to the first programme served %q, want studio-a", got)
	}
}

// #2 of the 2026-11-04 lingering-state audit. Deleting the programme the
// public page is published from must take the public page down, not hand the
// audience a different programme nobody chose to publish.
func TestDeletingThePublishedProgrammeTakesThePublicPageDown(t *testing.T) {
	s, h, _, sign := managerServer(t, defaultTools())
	first := s.eng()
	if first == nil {
		t.Fatal("no default engine in the fixture")
	}
	second := secondProgramme(t, s)
	send(t, h, sign, http.MethodPut, "/api/v1/playout/publish",
		map[string]any{"protection": string(PlayoutProtectOpen)}, http.StatusOK)

	var set map[string]any
	if err := json.Unmarshal(send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &set); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	p, _ := set["playout"].(map[string]any)
	p["enabled"], p["public"], p["sourceId"] = true, true, second.ID
	send(t, h, sign, http.MethodPut, "/api/v1/settings", set, http.StatusOK)

	const probe = "which-programme.m3u8"
	mark := func(sourceID int64, body string) {
		t.Helper()
		dir := s.cfg.PlayoutDirFor(sourceID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, probe), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	anon := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, PlayoutPrefix+probe, nil))
		return w
	}
	mark(first.SourceID(), "studio-a")
	mark(second.ID, "studio-b")
	if w := anon(); w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "studio-b" {
		t.Fatalf("before the delete the audience got %d %q, want studio-b", w.Code, w.Body.String())
	}

	// The delete says so, at the delete: 200 with the sentence, where it used
	// to answer a bare 204 and leave the operator to hear it from the audience.
	var reply struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(send(t, h, sign, http.MethodDelete, "/api/v1/sources/"+itoa(second.ID),
		map[string]any{"confirm": true, "destinations": 0}, http.StatusOK), &reply); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(reply.Warnings, func(s string) bool {
		return strings.Contains(s, "public playout page was published from this programme")
	}) {
		t.Errorf("the delete's warnings %q do not say the public page was published from it", reply.Warnings)
	}
	mark(first.SourceID(), "studio-a")
	w := anon()
	if w.Code == http.StatusOK && strings.TrimSpace(w.Body.String()) == "studio-a" {
		t.Fatal("deleting the published programme silently published studio-a to the audience")
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("after deleting the published programme the public page answered %d %q, want 503",
			w.Code, w.Body.String())
	}
}

// The other half of #2, which must not change: a published programme that
// EXISTS but has no engine yet (mid-restart, or failed to start) still falls
// back to the default programme rather than taking the page down.
func TestAPublishedProgrammeThatIsNotRunningYetStillFallsBack(t *testing.T) {
	s, h, _, sign := managerServer(t, defaultTools())
	store := s.store
	first := s.eng()
	if first == nil {
		t.Fatal("no default engine in the fixture")
	}
	send(t, h, sign, http.MethodPut, "/api/v1/playout/publish",
		map[string]any{"protection": string(PlayoutProtectOpen)}, http.StatusOK)
	// Published from the running default first, through the route, so the
	// public mount is live; then pointed at the idle programme behind the
	// API's back, the way a manager that has not reconciled yet sees it.
	var set map[string]any
	if err := json.Unmarshal(send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &set); err != nil {
		t.Fatal(err)
	}
	p, _ := set["playout"].(map[string]any)
	p["enabled"], p["public"], p["sourceId"] = true, true, first.SourceID()
	send(t, h, sign, http.MethodPut, "/api/v1/settings", set, http.StatusOK)
	idle := &db.Source{Name: "Not started", Enabled: true, Ingest: db.DefaultSettings().Ingest}
	if err := store.CreateSource(idle); err != nil {
		t.Fatal(err)
	}
	if s.engineForSource(&idle.ID) != nil {
		t.Fatal("the fixture started an engine for the new programme; this case needs one with none")
	}
	if _, err := store.UpdateSettings(func(set *db.Settings) error {
		set.Playout.SourceID = &idle.ID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	const probe = "which-programme.m3u8"
	dir := s.cfg.PlayoutDirFor(first.SourceID())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, probe), []byte("studio-a"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, PlayoutPrefix+probe, nil))
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "studio-a" {
		t.Fatalf("a published programme with no engine yet answered %d %q, want the default's studio-a",
			w.Code, w.Body.String())
	}
}
