package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

/* THE UPGRADE THAT WAS INTERRUPTED BETWEEN THE COMMIT AND THE CHECKPOINT.
 *
 * 0.7.0's Security note says the seal-at-rest migration no longer leaves the
 * plaintext keys it replaced legible, and names the two halves of the fix:
 * secure_delete for the freed pages, and wal_checkpoint(TRUNCATE) for the
 * write-ahead log. TestUpgradeLeavesNoPlaintextAfterAnUncleanShutdown pins
 * that for the run where the backfill DOES work.
 *
 * IT IS THE SECOND BOOT THAT WAS NEVER TESTED. backfillDestinationStreamKeys
 * returns at `if len(todo) == 0` long before it reaches the checkpoint. So a
 * process that commits the sealed rows and then dies -- power loss, an OOM
 * kill, a systemctl restart landing in the wrong second -- comes back to a
 * database where every row is already sealed, finds nothing to do, returns
 * early, and NEVER TRUNCATES THE LOG AGAIN. The plaintext sits in the -wal
 * for the life of the install.
 *
 * That is not a narrower version of the bug 0.7.0 fixed. It is the same bug,
 * reached by the one path the fix does not cover, and it is permanent rather
 * than transient.
 */

// TestAnInterruptedUpgradeStillTruncatesTheLogOnTheNextBoot is the regression.
func TestAnInterruptedUpgradeStillTruncatesTheLogOnTheNextBoot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "polyemesis.db")
	box := testBox(t)

	// A pre-0.7.0 install: plaintext keys, some churn, and a CLEAN close, so
	// the plaintext starts out in the main database file.
	first := keyDB(t, path)
	ids := make([]int64, 0, residueRows)
	for i := 0; i < residueRows; i++ {
		d := validDest()
		d.Name = fmt.Sprintf("dest-%03d", i)
		row, err := first.CreateDestination(d)
		if err != nil {
			t.Fatalf("CreateDestination: %v", err)
		}
		ids = append(ids, row.ID)
		if _, err := first.sql.Exec(
			`UPDATE destinations SET stream_key = ?, stream_key_enc = NULL WHERE id = ?`,
			residueNeedle(i), row.ID); err != nil {
			t.Fatalf("seed plaintext: %v", err)
		}
	}
	for i, id := range ids {
		if i%3 != 0 {
			continue
		}
		if _, err := first.sql.Exec(`UPDATE destinations SET name = ? WHERE id = ?`,
			fmt.Sprintf("renamed-%03d", i), id); err != nil {
			t.Fatalf("churn: %v", err)
		}
	}

	// The backfill's transaction, exactly as backfillDestinationStreamKeys
	// writes it -- and then nothing. No checkpoint: this is where the process
	// died. The handle is deliberately left OPEN, which is how a -wal survives
	// from inside one process; the shipped unclean-shutdown test uses the same
	// trick.
	tx, err := first.sql.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	for i, id := range ids {
		enc, err := box.Seal(residueNeedle(i))
		if err != nil {
			t.Fatalf("seal: %v", err)
		}
		if _, err := tx.Exec(`UPDATE destinations SET stream_key='', stream_key_enc=?,
			backup_stream_key='', backup_stream_key_enc=NULL WHERE id=?`, enc, id); err != nil {
			t.Fatalf("backfill update: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Snapshot the on-disk state a SIGKILL at this instant would leave.
	crash := filepath.Join(dir, "crashed.db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		b, err := os.ReadFile(path + suffix)
		if err != nil {
			continue
		}
		if err := os.WriteFile(crash+suffix, b, 0o600); err != nil {
			t.Fatalf("copy %s: %v", suffix, err)
		}
	}

	before := 0
	for i := 0; i < residueRows; i++ {
		db, wal := rawResidue(t, crash, residueNeedle(i))
		before += db + wal
	}
	if before == 0 {
		t.Fatal("fixture: the crashed files carry no plaintext, so this proves nothing")
	}
	t.Logf("plaintext copies in the crashed files before the restart: %d", before)

	// The operator's next boot.
	second := keyDB(t, crash, WithSecretBox(box))
	if _, err := second.ListDestinations(); err != nil {
		t.Fatalf("ListDestinations: %v", err)
	}

	totalDB, totalWAL := 0, 0
	for i := 0; i < residueRows; i++ {
		db, wal := rawResidue(t, crash, residueNeedle(i))
		totalDB += db
		totalWAL += wal
	}
	if totalDB+totalWAL != 0 {
		t.Errorf("after the restart %d plaintext stream keys are still greppable "+
			"(db=%d wal=%d). The backfill found nothing to do and returned before "+
			"the checkpoint, so the log was never truncated -- and never will be.",
			totalDB+totalWAL, totalDB, totalWAL)
	}
}

// A CHECKPOINT THAT REPORTS `busy` IS A CHECKPOINT THAT DID NOT HAPPEN.
//
// PRAGMA wal_checkpoint(TRUNCATE) does not fail with a SQL error when it cannot
// get the lock. It RETURNS A ROW -- (busy, log, checkpointed) -- with busy=1 and
// the log left in place. Exec discards result rows, so the fatal-on-failure
// guarantee the code claims for itself was never armed for the one failure that
// actually occurs.
func TestABusyCheckpointIsNotMistakenForASuccessfulOne(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "polyemesis.db")
	d := keyDB(t, path)

	// An idle database checkpoints cleanly.
	if err := checkpointTruncate(d); err != nil {
		t.Fatalf("checkpointTruncate on an idle database: %v", err)
	}

	// NOW HOLD THE LOCK FROM A SECOND CONNECTION, which is the only way this
	// fails in the field: another polyemesis on the same file, a backup tool, an
	// operator with the sqlite3 CLI open. Exec would discard the busy row and
	// report success.
	other, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("second handle: %v", err)
	}
	defer other.Close()

	if _, err := d.sql.Exec(`CREATE TABLE IF NOT EXISTS wal_probe(x)`); err != nil {
		t.Fatalf("probe table: %v", err)
	}
	if _, err := d.sql.Exec(`INSERT INTO wal_probe(x) VALUES (1)`); err != nil {
		t.Fatalf("probe write: %v", err)
	}

	tx, err := other.Begin()
	if err != nil {
		t.Fatalf("reader begin: %v", err)
	}
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM wal_probe`).Scan(&n); err != nil {
		tx.Rollback()
		t.Fatalf("reader select: %v", err)
	}

	err = checkpointTruncate(d)
	tx.Rollback()

	// ASSERTED, NOT SKIPPED. The first version of this test skipped when the
	// checkpoint succeeded, on the theory that some SQLite build might allow a
	// truncate with a reader open. That made it unable to fail: reverting the fix
	// to Exec leaves `busy` at its zero value, so the error is nil, so the test
	// skipped — and a skip reads as a pass. Mutation-checked, and the skip is why
	// the mutation survived. This build provokes the busy path reliably, so the
	// test demands it.
	if err == nil {
		t.Fatal("the checkpoint reported success while another connection held a read " +
			"transaction. The busy row is being discarded — an Exec cannot see it — so " +
			"a log that was never truncated is reported as truncated.")
	}
	if !strings.Contains(err.Error(), "busy") {
		t.Errorf("checkpoint refused with %q, want an error naming the busy checkpoint "+
			"so the operator knows the log was not truncated", err)
	}
}
