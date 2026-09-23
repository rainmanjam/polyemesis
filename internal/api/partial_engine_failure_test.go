package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// ONE PROGRAMME OF TWO WITH NO ENGINE IS A PARTIAL OUTAGE, AND BOTH PROBES
// USED TO CALL IT HEALTHY.
//
// Manager.Sync logs and carries on when engine.New or Engine.Start fails for a
// source, and Manager.Start refuses to boot only when EVERY source failed. That
// is right -- one misconfigured ingest must not take the others off the air --
// but it leaves a state neither probe could see: /health counted "1 of 2
// source(s) running" as ok, and the scrape walked running engines only, so the
// programme that was off the air had no ingest series at all. The alert
// MONITORING.md tells you to write, `ingest_bitrate == 0 and on() sources > 0`,
// cannot fire on a series that does not exist, and neither can `ingest_up ==
// 0`. And the alert watcher is per engine, so ingest.lost is never raised for
// it either: nothing anywhere said a programme was missing.
//
// The source is created WITHOUT a manager sync, which is the fixture
// TestADestinationOnAProgrammeWithNoEngineIsStillReported uses for the same
// reason: the observable state -- a source row with no entry in m.engines -- is
// identical to one whose engine failed to build, and it needs no broken FFmpeg.
func engineless(t *testing.T, s *Server) *db.Source {
	t.Helper()
	if s.eng() == nil {
		t.Fatal("no engine in the fixture, so there is no running programme beside " +
			"the missing one and this test would measure a total outage instead")
	}
	src := &db.Source{Name: "failed to build", Enabled: true, Ingest: db.DefaultSettings().Ingest}
	if err := s.store.CreateSource(src); err != nil {
		t.Fatalf("create source: %v", err)
	}
	if s.mgr.Engine(src.ID) != nil {
		t.Fatalf("source %d has an engine without a sync, so the partial failure "+
			"this test is named for is not the state it built", src.ID)
	}
	return src
}

// Degraded, not unhealthy. The status code is what an orchestrator restarts on,
// and a restart would take the programme that IS on air off it to retry the one
// that is not -- the "503 only for what a restart could fix" rule health.go sets
// out. So it is a 200 whose status field says what is wrong.
//
// Mutation: drop the `running < sources` case from handleHealth. Observed to
// fail with `status field = "ok"`.
func TestHealthIsDegradedWhenOneProgrammeOfTwoHasNoEngine(t *testing.T) {
	s, h, _, _ := managerServer(t, defaultTools())
	engineless(t, s)

	code, raw, body := getHealth(t, h)
	if code != http.StatusOK {
		t.Errorf("status = %d, want 200: one programme is still on air and a restart "+
			"would take it off (body %s)", code, raw)
	}
	if body.Status != "degraded" {
		t.Fatalf("status field = %q, want %q -- a programme that is not running must "+
			"show up somewhere a monitor looks (body %s)", body.Status, "degraded", raw)
	}
	for _, c := range body.Checks {
		if c.Name == "engine" {
			if c.OK {
				t.Errorf("the engine check is ok with a programme missing: %+v", c)
			}
			if !strings.Contains(c.Detail, "1 of 2") {
				t.Errorf("engine detail = %q, want it to say 1 of 2 running", c.Detail)
			}
			return
		}
	}
	t.Errorf("no engine check in %+v", body.Checks)
}

// And the scrape carries the missing programme as STOPPED, plus a gauge that
// says why -- so `ingest_up == 0` fires for it, and the operator can tell "the
// encoder is not sending" from "there is nothing here to receive it".
//
// Mutation: drop the ListSources sweep from ingestSnapshots. Observed to fail
// with "no polyemesis_ingest_up series for the programme with no engine".
func TestTheScrapeReportsAProgrammeWithNoEngineAsStopped(t *testing.T) {
	s, h, _, auth := managerServer(t, defaultTools())
	src := engineless(t, s)
	id := itoa(src.ID)
	running := itoa(s.eng().SourceID())

	r := jsonRequest(t, http.MethodGet, "/api/v1/metrics", nil)
	auth(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/metrics: %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	for _, want := range []string{
		`polyemesis_ingest_up{id="` + id + `",name="failed to build"} 0`,
		`polyemesis_ingest_state{id="` + id + `",name="failed to build",state="stopped"} 1`,
		`polyemesis_source_engine_up{id="` + id + `",name="failed to build"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("no %q in the scrape: no polyemesis_ingest_up series for the "+
				"programme with no engine means no alert can fire for it", want)
		}
	}
	// The control: the programme that is running must not be dragged down with
	// it, or the gauge would be a constant 0 and the test above would pass on it.
	if want := `polyemesis_source_engine_up{id="` + running + `",`; !strings.Contains(body, want) ||
		strings.Contains(body, want+`name="`+s.eng().SourceName()+`"} 0`) {
		t.Errorf("the running programme %s is not reported with an engine up", running)
	}
}

// A sources table that will not read loses the sweep, not the scrape. The
// engines' own series are still worth returning, the same choice
// DestinationStatuses makes, and an empty list would silence every ingest
// alert at once over what is probably a momentary sqlite error.
func TestIngestSnapshotsKeepTheRunningProgrammesWhenSourcesWillNotRead(t *testing.T) {
	s, _, _, _ := managerServer(t, defaultTools())
	if s.eng() == nil {
		t.Fatal("no engine in the fixture, so there is nothing to keep")
	}
	want := s.eng().SourceID()
	if err := s.store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	got := s.ingestSnapshots()
	for _, in := range got {
		if in.ID == want && !in.NoEngine {
			return
		}
	}
	t.Errorf("the running programme %d is missing once the sources table will not read: %+v", want, got)
}
