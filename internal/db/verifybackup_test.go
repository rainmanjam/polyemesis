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
