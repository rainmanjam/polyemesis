package db

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

/* The onboarding tour's completion flag, and the two properties that are worth
   a test rather than a reading.

   BOTH of them are properties of the STORAGE choice, which is the whole reason
   the flag is not in localStorage: the point of putting it on the server is
   that it survives things a browser does not, and "survives" is a claim about
   reopening a file rather than about a line of Go.

   What is deliberately NOT tested here is that the tour renders. That is
   ui/src/lib/tour-drift.test.ts's job -- the selectors -- and no Go test in
   this package can render React. */

// TestTourMigrationIsIdempotent drives MigrateUserTourCompleted against a
// database that already has the column.
//
// Every startup calls it, so "runs twice" is not an edge case, it is the second
// boot. An ALTER TABLE ADD COLUMN without the has-column guard errors with
// "duplicate column name" and the server refuses to start -- which is the
// failure this guard exists for, and it would land on an operator's install
// rather than in CI.
func TestTourMigrationIsIdempotent(t *testing.T) {
	d := testDB(t)

	// testDB already opened through Open, which ran the migration once. These
	// are runs two and three.
	for i := range 2 {
		if err := d.MigrateUserTourCompleted(); err != nil {
			t.Fatalf("MigrateUserTourCompleted run %d: %v", i+2, err)
		}
	}

	// And the guard is not passing because the column is absent: prove the
	// column is there by writing through it.
	u, err := d.CreateUser("operator", "correct horse battery")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := d.SetTourCompleted(u.ID, time.Unix(1700000000, 0)); err != nil {
		t.Fatalf("SetTourCompleted: %v", err)
	}
	at, err := d.TourCompletedAt(u.ID)
	if err != nil {
		t.Fatalf("TourCompletedAt: %v", err)
	}
	if at != 1700000000 {
		t.Errorf("TourCompletedAt = %d, want 1700000000. The migration reported success "+
			"three times over a column that does not hold what is written to it.", at)
	}
}

// TestTourMigrationAddsTheColumnToAnOlderDatabase is the other half, and the
// half a guard-that-guards needs.
//
// The test above runs the migration against a database that already has the
// column, which is exactly the state in which a BROKEN migration -- one that
// returns nil without doing anything -- also passes. Here the column is dropped
// first, so the migration has to actually add it.
func TestTourMigrationAddsTheColumnToAnOlderDatabase(t *testing.T) {
	d := testDB(t)

	if _, err := d.sql.Exec(`ALTER TABLE users DROP COLUMN tour_completed_at`); err != nil {
		t.Fatalf("drop the column to simulate an older database: %v", err)
	}
	has, err := columnExists(d.sql, "users", "tour_completed_at")
	if err != nil {
		t.Fatalf("columnExists: %v", err)
	}
	if has {
		t.Fatal("the column is still there after DROP COLUMN, so this test proves nothing")
	}

	if err := d.MigrateUserTourCompleted(); err != nil {
		t.Fatalf("MigrateUserTourCompleted: %v", err)
	}
	has, err = columnExists(d.sql, "users", "tour_completed_at")
	if err != nil {
		t.Fatalf("columnExists after migrate: %v", err)
	}
	if !has {
		t.Error("MigrateUserTourCompleted returned nil and added nothing. An install " +
			"upgrading into this version would fail on the first read of the column.")
	}
}

// TestTourCompletionSurvivesAReopen is the reason the flag is on the server.
//
// The design note in schema.sql says an operator opening this install from a
// second machine should not be offered the tour again. That claim rests
// entirely on the value being DURABLE, and a value written to a WAL that never
// checkpoints is not -- which is not hypothetical in this package: testTemplate
// in db_test.go carries a paragraph about exactly that, having read an empty
// file because it read before closing.
//
// So this closes the database and opens the same path again, which is what a
// server restart is, and what "a different browser asks the same server" is
// downstream of.
func TestTourCompletionSurvivesAReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyemesis.db")

	d, err := Open(path, WithPasswordCost(bcrypt.MinCost))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	u, err := d.CreateUser("operator", "correct horse battery")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	// Zero before anything writes it, or "completed" would be true on a fresh
	// install and the tour would never be offered at all.
	if at, err := d.TourCompletedAt(u.ID); err != nil || at != 0 {
		t.Fatalf("TourCompletedAt on a new account = %d, %v; want 0, nil", at, err)
	}
	want := time.Unix(1712345678, 0)
	if err := d.SetTourCompleted(u.ID, want); err != nil {
		t.Fatalf("SetTourCompleted: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := Open(path, WithPasswordCost(bcrypt.MinCost))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { reopened.Close() })

	at, err := reopened.TourCompletedAt(u.ID)
	if err != nil {
		t.Fatalf("TourCompletedAt after reopen: %v", err)
	}
	if at != want.Unix() {
		t.Errorf("TourCompletedAt after reopen = %d, want %d. The completion did not "+
			"survive a restart, so the tour would be offered again on every boot -- "+
			"which is worse than the localStorage it was chosen over.", at, want.Unix())
	}

	// The reopen also ran MigrateUserTourCompleted a second time against a
	// populated column. If that path were destructive, the assertion above
	// would already have caught it; this states the intent.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
}

// TestTourHelpersReportAMissingUser keeps the "no admin yet" answer distinct
// from "an admin who has not taken the tour".
//
// Both would otherwise be zero, and the API handler branches on the difference:
// an install that has not completed first-run setup answers 404 rather than
// handing a client a plausible-looking state for a user who does not exist.
func TestTourHelpersReportAMissingUser(t *testing.T) {
	d := testDB(t)

	if _, err := d.TourCompletedAt(1); !errors.Is(err, ErrNoUser) {
		t.Errorf("TourCompletedAt on an empty users table returned %v, want ErrNoUser", err)
	}
	if err := d.SetTourCompleted(1, time.Now()); !errors.Is(err, ErrNoUser) {
		t.Errorf("SetTourCompleted on an empty users table returned %v, want ErrNoUser", err)
	}
}

// TestSetTourCompletedLeavesUpdatedAtAlone pins the decision recorded in
// SetTourCompleted's doc comment.
//
// users.updated_at describes the ACCOUNT. Letting a dismissed popover move it
// would make "when was this account last changed" -- the field an operator
// reads when they are asking whether somebody touched their credentials --
// answer a question about onboarding instead.
func TestSetTourCompletedLeavesUpdatedAtAlone(t *testing.T) {
	d := testDB(t)

	u, err := d.CreateUser("operator", "correct horse battery")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	var before int64
	if err := d.sql.QueryRow(`SELECT updated_at FROM users WHERE id = ?`, u.ID).Scan(&before); err != nil {
		t.Fatalf("read updated_at: %v", err)
	}

	// A timestamp well clear of "now", so an accidental `updated_at = ?` with
	// either value is visible rather than merely equal by luck.
	if err := d.SetTourCompleted(u.ID, time.Unix(2000000000, 0)); err != nil {
		t.Fatalf("SetTourCompleted: %v", err)
	}

	var after int64
	if err := d.sql.QueryRow(`SELECT updated_at FROM users WHERE id = ?`, u.ID).Scan(&after); err != nil {
		t.Fatalf("read updated_at: %v", err)
	}
	if after != before {
		t.Errorf("updated_at moved from %d to %d when the tour was dismissed. That column "+
			"describes the account, not the console's popovers.", before, after)
	}
}
