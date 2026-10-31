package db

// TROVO SHUT DOWN ON 30 JUNE 2026, and a destination pointed at it is now a
// destination pointed at nothing.
//
// Three rungs, and this file pins all three:
//
//   - CONTROL AT THE DOOR. CreateDestination refuses the platform, and the
//     refusal names the shutdown rather than "unknown platform" -- the operator
//     may well be reading an old guide that still lists it.
//   - THE ROWS THAT ALREADY EXIST. They are NOT deleted: the settings are the
//     operator's, and an upgrade that silently removed a row would be the one
//     surprise worse than a row that cannot run. MigrateRetiredPlatforms turns
//     them off at Open, drops the Trovo OAuth tokens and app credentials, and
//     every read carries the reason. They can still be renamed and deleted.
//   - NO START. SetDestinationEnabled(true) is the one choke point the API's
//     start, the bulk start-all, MCP and the scheduler all pass through, so the
//     refusal lives there and arrives as a sentence, not as FFmpeg failing to
//     resolve a hostname that no longer exists.
//
// NO TEST HERE PRINTS A TOKEN. The sentinels are synthetic and named so that
// gitleaks' identifier match does not fire on them.

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

const (
	retiredAccessSentinel  = "SENTINEL-trovo-access-4b19e0"
	retiredRefreshSentinel = "SENTINEL-trovo-refresh-77ac21"
	retiredClientSentinel  = "SENTINEL-trovo-client-secret-0d3f"
)

// TestCreatingATrovoDestinationIsRefusedAndTheRefusalNamesTheShutdown is the
// Control rung.
func TestCreatingATrovoDestinationIsRefusedAndTheRefusalNamesTheShutdown(t *testing.T) {
	d := testDB(t)
	dst := validDest()
	dst.Platform = PlatformTrovo

	_, err := d.CreateDestination(dst)
	if err == nil {
		t.Fatal("a Trovo destination was created after Trovo shut down")
	}
	if !errors.Is(err, ErrPlatformRetired) {
		t.Errorf("the refusal is not ErrPlatformRetired, so the API cannot map it: %v", err)
	}
	if !strings.Contains(err.Error(), "Trovo shut down on 30 June 2026") {
		t.Errorf("the refusal does not name the shutdown: %v", err)
	}
	list, err := d.ListDestinations()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("the refused create still wrote %d row(s)", len(list))
	}
}

// TestADestinationCannotBeRetypedToTrovo closes the side door: an update that
// switches an existing YouTube destination to Trovo is a create by another name.
func TestADestinationCannotBeRetypedToTrovo(t *testing.T) {
	d := testDB(t)
	saved, err := d.CreateDestination(validDest())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	saved.Platform = PlatformTrovo
	if _, err := d.UpdateDestination(saved); !errors.Is(err, ErrPlatformRetired) {
		t.Fatalf("retyping a destination to Trovo was not refused as retired: %v", err)
	}
}

// plantTrovoRow writes the state an upgrading install arrives with: an enabled
// Trovo destination linked to a connected Trovo account, plus the operator's
// Trovo developer-app credentials. Behind CreateDestination's back, because the
// release that produced it is not the one running the test.
func plantTrovoRow(t *testing.T, d *DB) (destID, acctID int64) {
	t.Helper()
	box := testBox(t)
	acct, err := d.UpsertPlatformAccount(box, &PlatformAccount{
		Platform: PlatformTrovo, AccountName: "trovan", AccountRef: "100000021",
		AccessToken: retiredAccessSentinel, RefreshToken: retiredRefreshSentinel,
	})
	if err != nil {
		t.Fatalf("plant account: %v", err)
	}
	if err := d.PutPlatformCreds(box, PlatformTrovo, "trovo-client-id", retiredClientSentinel); err != nil {
		t.Fatalf("plant app credentials: %v", err)
	}
	if err := d.PutOAuthState("state-trovo-pending", PlatformTrovo, ""); err != nil {
		t.Fatalf("plant oauth state: %v", err)
	}
	saved, err := d.CreateDestination(validDest())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := d.sql.Exec(`UPDATE destinations SET platform=?, account_id=?, enabled=1,
		url='rtmp://livepush.trovo.example/live' WHERE id=?`,
		string(PlatformTrovo), acct.ID, saved.ID); err != nil {
		t.Fatalf("retype to trovo: %v", err)
	}
	return saved.ID, acct.ID
}

// TestAnExistingTrovoDestinationIsDisabledWithAReasonOnOpenAndNotDeleted is the
// migration rung.
func TestAnExistingTrovoDestinationIsDisabledWithAReasonOnOpenAndNotDeleted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyemesis.db")
	first := keyDB(t, path)
	id, _ := plantTrovoRow(t, first)
	// A neighbour that must come through untouched: the sweep is about one
	// platform, and a sweep that disabled everything would take an install
	// off the air on upgrade.
	other, err := first.CreateDestination(validDest())
	if err != nil {
		t.Fatalf("create neighbour: %v", err)
	}
	if err := first.SetDestinationEnabled(other.ID, true); err != nil {
		t.Fatalf("enable neighbour: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second := keyDB(t, path)
	back, err := second.GetDestination(id)
	if err != nil {
		t.Fatalf("the Trovo destination did not survive Open, which is the operator's settings deleted: %v", err)
	}
	if back.Enabled {
		t.Error("the Trovo destination is still enabled after Open")
	}
	if !strings.Contains(back.Retired, "Trovo shut down on 30 June 2026") {
		t.Errorf("the destination carries no shutdown reason: %q", back.Retired)
	}
	// Readable: what the operator configured is still there to look at.
	if back.URL != "rtmp://livepush.trovo.example/live" || back.Name != "Main" {
		t.Errorf("the settings were not preserved: name=%q url=%q", back.Name, back.URL)
	}
	if back.AccountID != nil {
		t.Error("the destination is still linked to a Trovo account whose tokens were dropped")
	}

	// The column, not just the reader: the reader forces Enabled off anyway,
	// so only the column proves the migration ran.
	var enabled bool
	if err := second.sql.QueryRow(`SELECT enabled FROM destinations WHERE id=?`, id).Scan(&enabled); err != nil {
		t.Fatalf("read column: %v", err)
	}
	if enabled {
		t.Error("the enabled column is still 1; only the reader is hiding it")
	}

	// Tokens and app credentials: gone, not merely ignored.
	for _, q := range []string{
		`SELECT COUNT(*) FROM platform_accounts WHERE platform='trovo'`,
		`SELECT COUNT(*) FROM platform_creds WHERE platform='trovo'`,
		`SELECT COUNT(*) FROM oauth_states WHERE platform='trovo'`,
	} {
		var n int
		if err := second.sql.QueryRow(q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if n != 0 {
			t.Errorf("%s = %d after Open, want 0", q, n)
		}
	}

	nb, err := second.GetDestination(other.ID)
	if err != nil {
		t.Fatalf("neighbour: %v", err)
	}
	if !nb.Enabled || nb.Retired != "" {
		t.Errorf("the sweep touched a non-Trovo destination: enabled=%v retired=%q", nb.Enabled, nb.Retired)
	}

	// Still the operator's: renameable, and deletable.
	back.Name = "Old Trovo channel"
	if _, err := second.UpdateDestination(back); err != nil {
		t.Fatalf("a retired destination could not be renamed: %v", err)
	}
	if err := second.DeleteDestination(id); err != nil {
		t.Fatalf("a retired destination could not be deleted: %v", err)
	}
}

// TestStartingARetiredDestinationIsRefusedWithTheReason is the no-start rung,
// at the one write every start path shares.
func TestStartingARetiredDestinationIsRefusedWithTheReason(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyemesis.db")
	first := keyDB(t, path)
	id, _ := plantTrovoRow(t, first)
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	second := keyDB(t, path)

	err := second.SetDestinationEnabled(id, true)
	if !errors.Is(err, ErrPlatformRetired) {
		t.Fatalf("starting a Trovo destination was not refused as retired: %v", err)
	}
	if !strings.Contains(err.Error(), "Trovo shut down on 30 June 2026") {
		t.Errorf("the refusal does not say why: %v", err)
	}
	// Stopping is always allowed: it can only ever put less on the air.
	if err := second.SetDestinationEnabled(id, false); err != nil {
		t.Errorf("stopping a retired destination was refused: %v", err)
	}
	// And the update path cannot be used to switch it on either.
	back, err := second.GetDestination(id)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	back.Enabled = true
	if _, err := second.UpdateDestination(back); !errors.Is(err, ErrPlatformRetired) {
		t.Fatalf("an update enabling a Trovo destination was not refused: %v", err)
	}
	// A missing row is still a missing row, not a retirement.
	if err := second.SetDestinationEnabled(id+1000, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("enabling a missing row = %v, want ErrNotFound", err)
	}
}

// TestTheRetirementSweepIsIdempotent: Open runs it on every start, so the
// second run must find nothing to do and break nothing.
func TestTheRetirementSweepIsIdempotent(t *testing.T) {
	d := testDB(t)
	plantTrovoRow(t, d)
	for i := range 2 {
		if err := d.MigrateRetiredPlatforms(); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
}
