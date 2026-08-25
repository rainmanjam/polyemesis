package db

import "testing"

// An install that predates the SSRF guard must gain the column on open, keep
// the hooks it already had, and default them to refusing private targets.
//
// This is the test that was missing when the column was added. Every other
// hooks test builds a fresh database from schema.sql, where CREATE TABLE puts
// the column there and the gap is invisible -- so the whole suite passed while
// an upgraded install would have answered "no such column: allow_private_target"
// on its first hook read, with the hooks page empty and deliveries stopped.
func TestAnUpgradedInstallGainsTheAllowPrivateTargetColumn(t *testing.T) {
	d := testDB(t)
	box := testBox(t)

	// A hook written BEFORE the column existed. Created through the normal
	// path so it is a real row, then the column is taken away to reproduce
	// the shape of a database from the previous release.
	if _, _, err := d.CreateHook(box, validHook()); err != nil {
		t.Fatalf("create hook: %v", err)
	}
	if _, err := d.sql.Exec(`ALTER TABLE hooks DROP COLUMN allow_private_target`); err != nil {
		t.Fatalf("could not reproduce the old schema: %v", err)
	}
	if has, err := columnExists(d.sql, "hooks", "allow_private_target"); err != nil || has {
		t.Fatalf("the column is still there, so this test proves nothing (has=%v err=%v)", has, err)
	}
	// The state an upgrade actually starts from: reading hooks must fail.
	if _, err := d.ListHooks(box); err == nil {
		t.Fatal("reading hooks on the old schema succeeded; the premise of this test is wrong")
	}

	if err := d.MigrateHookAllowPrivateTarget(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	list, err := d.ListHooks(box)
	if err != nil {
		t.Fatalf("hooks unreadable after the migration: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("the migration lost rows: %d hooks, want 1", len(list))
	}
	if list[0].AllowPrivateTarget {
		t.Error("an existing hook came back allowed to reach private targets; " +
			"an upgrade must keep refusing them until an operator opts in")
	}
	// Idempotent: every later open runs this again.
	if err := d.MigrateHookAllowPrivateTarget(); err != nil {
		t.Fatalf("second run: %v", err)
	}
}
