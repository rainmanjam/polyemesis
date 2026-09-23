package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/upgrade"
)

// A rollback swaps binaries and nothing else. Once the release it rolls back
// FROM has migrated the database to a schema the previous binary does not
// know, the previous binary's refuseNewerSchema stops it at boot -- so the
// rollback button, pressed at the moment something has already gone wrong,
// would turn a bad upgrade into a service that does not start at all. The
// server knows both numbers before it moves anything, so it refuses there,
// and says to restore the backup instead.
func TestRollbackIsRefusedOnceTheDatabaseIsNewerThanThePreviousBinary(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	path, _ := fakeInstall(t)
	s.upgradeMethod = upgrade.MethodSystemd
	s.execPath = path
	sign := login(t, h)
	seedUpdateCache(t, releaseTag)
	stubReleaseDownloads(t, releaseTag, releasedBytes, "")
	stubOnAir(t, engine.OnAir{})

	// This binary stages the release and becomes the rollback point.
	send(t, h, sign, http.MethodPost, "/api/v1/upgrade/stage",
		upgradeAction{Version: releaseTag}, http.StatusOK)

	// The release restarts and migrates the database past anything this
	// binary understands. Stamped directly: no newer schema exists yet, and
	// this is the first bump the guard has to be in place before.
	if _, err := store.SQL().Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}

	var plan upgradePlanView
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/upgrade/plan", nil, http.StatusOK), &plan)
	if plan.RollbackAvailable {
		t.Error("the plan offers a rollback to a binary that would refuse this database")
	}

	msg := mustJSONError(t, h, sign, http.MethodPost, "/api/v1/upgrade/rollback",
		upgradeAction{}, http.StatusConflict)
	if !strings.Contains(msg, "backup") {
		t.Errorf("refusal = %q, want it to point at restoring the backup", msg)
	}
	if got := readFile(t, path); got != releasedBytes {
		t.Errorf("a refused rollback changed the binary to %q", got)
	}
}

// Not knowing the database's schema is not knowing whether the previous binary
// would start on it, so the plan refuses rather than guesses.
func TestAnUnreadableSchemaVersionWithholdsTheRollback(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	path, _ := fakeInstall(t)
	s.upgradeMethod = upgrade.MethodSystemd
	s.execPath = path
	sign := login(t, h)
	seedUpdateCache(t, releaseTag)
	stubReleaseDownloads(t, releaseTag, releasedBytes, "")
	stubOnAir(t, engine.OnAir{})
	send(t, h, sign, http.MethodPost, "/api/v1/upgrade/stage",
		upgradeAction{Version: releaseTag}, http.StatusOK)

	if plan := s.upgradePlan(releaseTag); !plan.RollbackAvailable {
		t.Fatalf("no rollback offered after a stage (blocked: %q)", plan.RollbackBlocked)
	}
	store.Close()
	plan := s.upgradePlan(releaseTag)
	if plan.RollbackAvailable || !strings.Contains(plan.RollbackBlocked, "schema version") {
		t.Errorf("RollbackAvailable=%v RollbackBlocked=%q, want it withheld for an unreadable schema version",
			plan.RollbackAvailable, plan.RollbackBlocked)
	}
}
