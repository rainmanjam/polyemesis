package db

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

/* "backup verified" used to mean "two files exist".
 *
 * Migrations run forward only, so the copy update.sh takes is the single way
 * back from an upgrade. Every way it can exist without being usable leaves a
 * file of plausible size, and none of them is noticed until the restore. So
 * these tests are mostly about what must FAIL: a good backup passing is one
 * assertion, and the rest are the shapes that used to pass. #643.
 */

func goodBackup(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	d, err := Open(filepath.Join(dir, "polyemesis.db"))
	if err != nil {
		t.Fatalf("seeding a real database: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret.key"), []byte("00"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestVerifyBackupAcceptsARealBackup(t *testing.T) {
	// The control. Without this the whole file could pass by rejecting
	// everything, which is the failure mode of a check nobody can satisfy.
	if err := VerifyBackup(goodBackup(t)); err != nil {
		t.Fatalf("a real backup was rejected: %v", err)
	}
}

func TestVerifyBackupRefusesAMissingSecretKey(t *testing.T) {
	dir := goodBackup(t)
	if err := os.Remove(filepath.Join(dir, "secret.key")); err != nil {
		t.Fatal(err)
	}
	err := VerifyBackup(dir)
	if err == nil {
		t.Fatal("accepted a backup with no secret.key")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Errorf("error does not say what restoring without it costs: %v", err)
	}
}

func TestVerifyBackupRefusesATruncatedDatabase(t *testing.T) {
	// What a copy interrupted by a full disk leaves behind.
	dir := goodBackup(t)
	p := filepath.Join(dir, "polyemesis.db")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	// Not a t.Skip. The skip ratchet in internal/testenv counts bare skips and
	// it caught this one -- correctly, because a test that declines to run
	// prints ok and is counted as coverage. The seeded schema is ~260 KiB and
	// deterministic, so this branch cannot fire; if it ever does, the seeding
	// changed underneath the test and passing quietly would be the wrong
	// answer.
	if len(b) < 8192 {
		t.Fatalf("seeded database is %d bytes, too small to truncate meaningfully -- "+
			"the schema this test relies on has changed", len(b))
	}
	if err := os.WriteFile(p, b[:len(b)/3], 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBackup(dir); err == nil {
		t.Fatal("accepted a truncated database -- this is the shape a copy " +
			"interrupted mid-write leaves, and it is exactly the file size a " +
			"reader would call plausible")
	}
}

func TestVerifyBackupRefusesCorruptedPages(t *testing.T) {
	dir := goodBackup(t)
	p := filepath.Join(dir, "polyemesis.db")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	// A whole page in the middle of the file, not a hole near the header.
	// Measured while writing this: scribbling bytes 2000-3500 of a freshly
	// seeded database changes nothing integrity_check can see, because that
	// range is unused space inside page 1. That is SQLite behaving correctly
	// and a test aimed there would have passed only by accident. A 4 KiB page
	// at the midpoint is real data and is caught.
	start := len(b) / 2
	for i := start; i < len(b) && i < start+4096; i++ {
		b[i] = 0x5A
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBackup(dir); err == nil {
		t.Fatal("accepted a database with corrupted pages")
	}
}

func TestVerifyBackupRefusesAnEmptyDatabaseSQLiteJustCreated(t *testing.T) {
	// `cp` of a path that does not exist, then something opens the
	// destination: a valid, empty, useless SQLite file. It passes
	// integrity_check, which is why existence and integrity are not enough.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secret.key"), []byte("00"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "polyemesis.db")
	if err := os.WriteFile(empty, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBackup(dir); err == nil {
		t.Fatal("accepted a zero-byte database")
	}
}

func TestVerifyBackupRefusesSomeoneElsesDatabase(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secret.key"), []byte("00"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "polyemesis.db")
	sqldbSeed(t, other)
	err := VerifyBackup(dir)
	if err == nil {
		t.Fatal("accepted a valid SQLite file that is not this server's database")
	}
	if !strings.Contains(err.Error(), "not this server's database") {
		t.Errorf("error does not say what is wrong with it: %v", err)
	}
}

func TestVerifyBackupRefusesABackupWhereTheCopyNeverRan(t *testing.T) {
	// The most ordinary failure of the lot, and the one the old existence
	// check was closest to catching: the directory is there, secret.key is
	// there, and polyemesis.db simply is not -- a copy that was never issued,
	// or was issued against a path that did not exist. update.sh then reports
	// success on a directory holding no database at all.
	//
	// Kept distinct from the truncated and corrupted cases because the remedy
	// differs: nothing here can be salvaged, so the operator has to take the
	// backup again before upgrading, and the message has to say so rather than
	// implying the file needs repair.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secret.key"), []byte("00"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := VerifyBackup(dir)
	if err == nil {
		t.Fatal("accepted a backup directory with no polyemesis.db in it")
	}
	if !strings.Contains(err.Error(), "no polyemesis.db") {
		t.Errorf("the error does not name the missing file, so the operator cannot "+
			"tell it apart from a corrupt one: %v", err)
	}
}

func TestVerifyBackupRefusesADatabaseSQLiteCannotEvenWalk(t *testing.T) {
	// A distinct failure path from the single-corrupted-page case, and worth
	// separating: damage spread across every page does not come back as a list
	// of integrity_check problems at all. SQLite refuses the query itself with
	// "database disk image is malformed", so the code never reaches the
	// problem-collecting branch. This test was originally written as if it did,
	// and asserted the truncation conditionally -- which is to say it asserted
	// nothing while looking thorough. It is named for what it actually proves.
	dir := goodBackup(t)
	p := filepath.Join(dir, "polyemesis.db")

	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	const page = 4096
	if len(b) < page*4 {
		t.Fatalf("seeded database is only %d bytes; too small to damage several pages", len(b))
	}
	for off := page; off+page <= len(b); off += page {
		copy(b[off+16:off+64], strings.Repeat("\xa5", 48))
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}

	err = VerifyBackup(dir)
	if err == nil {
		t.Fatal("accepted a database damaged on every page")
	}
	if !strings.Contains(err.Error(), "could not be read") {
		t.Errorf("wholesale damage should be reported as unreadable rather than as a "+
			"list of individual problems, so the operator knows there is nothing to "+
			"salvage: %v", err)
	}
}

// NOT TESTED, DELIBERATELY, RECORDED SO THE GAP IS NOT MISTAKEN FOR AN
// OVERSIGHT: the branch in VerifyBackup that keeps the first five
// integrity_check problems and counts the rest. Reaching it needs damage mild
// enough that SQLite still walks the file but bad enough that it reports more
// than five distinct problems, and how many rows it emits for a given byte
// pattern is SQLite's business. A test pinning that would assert SQLite's
// internals, break on a version bump, and tell us nothing about this function.
