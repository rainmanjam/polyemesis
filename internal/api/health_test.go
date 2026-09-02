package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

type healthBody struct {
	Status string `json:"status"`
	Checks []struct {
		Name   string `json:"name"`
		OK     bool   `json:"ok"`
		Detail string `json:"detail"`
	} `json:"checks"`
}

func getHealth(t *testing.T, h http.Handler) (int, string, healthBody) {
	t.Helper()
	w := do(t, h, jsonRequest(t, http.MethodGet, "/api/v1/health", nil))
	var out healthBody
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode health: %v (body %s)", err, w.Body.String())
	}
	return w.Code, strings.TrimRight(w.Body.String(), "\n"), out
}

// THE HEALTHY ANSWER IS BYTE-IDENTICAL TO THE CONSTANT IT REPLACED.
//
// scripts/acceptance-tls.sh compares this body to the exact string
// `{"status":"ok"}` in three places, and the container images bake
// `wget -qO- .../api/v1/health` into HEALTHCHECK. Enriching the happy path
// would break every one of those to say something none of them read.
func TestAHealthyServerAnswersHealthExactlyAsItAlwaysDid(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})

	code, raw, _ := getHealth(t, h)
	if code != http.StatusOK {
		t.Errorf("status = %d, want 200", code)
	}
	if raw != `{"status":"ok"}` {
		t.Errorf("body = %s, want exactly {\"status\":\"ok\"} -- the installer and the "+
			"container healthcheck compare this string", raw)
	}
}

// The finding: this endpoint was a constant, and three separate mechanisms
// treated it as proof of a working install. A database that has gone away is
// the plainest thing it could not say, and it is the one a restart might fix,
// so it is the one that answers 503.
func TestHealthAnswers503WhenTheDatabaseHasGoneAway(t *testing.T) {
	_, h, store := testServer(t, config.Config{})

	// Closing the handle is what an unmounted volume or a deleted file looks
	// like from in here: the store is still wired, and every query fails.
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	code, raw, body := getHealth(t, h)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 -- health cannot be false, so it proves nothing "+
			"(body %s)", code, raw)
	}
	if body.Status != "unhealthy" {
		t.Errorf("status field = %q, want %q", body.Status, "unhealthy")
	}
	var found bool
	for _, c := range body.Checks {
		if c.Name == "database" {
			found = true
			if c.OK {
				t.Error("the database check reported ok on a closed store")
			}
			if c.Detail == "" {
				t.Error("the database check failed with no detail; a monitor gets a red light and no cause")
			}
		}
	}
	if !found {
		t.Errorf("no database check in %+v", body.Checks)
	}
}

// The other fatal condition, and it is the same one Manager.Start refuses to
// boot on: sources configured and not one engine running means nothing is being
// published, however cheerfully the port answers.
//
// Driven through a server whose manager is wired but has no engine for the
// source planted underneath it.
func TestHealthAnswers503WhenSourcesAreConfiguredAndNoEngineRuns(t *testing.T) {
	srv, h, store, _ := engineServer(t, defaultTools(), Options{})

	// engineServer's fixture has a source and an engine, which is the healthy
	// shape. Take the engines away without touching the sources table.
	srv.mgr.Stop()

	if n, err := store.CountSources(); err != nil || n == 0 {
		t.Fatalf("fixture has %d sources (err %v); this test needs at least one", n, err)
	}

	code, raw, body := getHealth(t, h)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 with sources configured and no engine (body %s)", code, raw)
	}
	for _, c := range body.Checks {
		if c.Name == "engine" && !c.OK && strings.Contains(c.Detail, "no engine running") {
			return
		}
	}
	t.Errorf("no engine check saying nothing is being published: %+v", body.Checks)
}

// A HALTED RECORDER IS NOT A 503, AND THAT IS DELIBERATE. The status code is
// what an orchestrator restarts on, a restart does not add disk, and dropping a
// live programme over a full recording volume would make this endpoint cause
// the outage it exists to detect. It is reported and it is not fatal.
func TestAFullRecordingVolumeIsReportedWithoutTakingTheServerDown(t *testing.T) {
	srv, h, _, _ := engineServer(t, defaultTools(), Options{})

	engines := srv.mgr.Engines()
	if len(engines) == 0 {
		t.Skip("fixture built no engine, so there is no recorder to halt")
	}
	engines[0].Recordings().CheckFreeSpace(db.RecordingSettings{MinFreeGB: 1 << 40})

	code, raw, body := getHealth(t, h)
	if code != http.StatusOK {
		t.Errorf("status = %d, want 200: a full recording volume must not restart a live server (body %s)",
			code, raw)
	}
	if body.Status != "degraded" {
		t.Errorf("status field = %q, want %q", body.Status, "degraded")
	}
	for _, c := range body.Checks {
		if c.Name == "recordingDisk" && !c.OK {
			return
		}
	}
	t.Errorf("the halted recorder is not in the health body: %+v", body.Checks)
}
