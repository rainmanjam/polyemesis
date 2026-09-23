package db

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

/* -verify-backup ACCEPTED ANY secret.key.
 *
 * The check was os.Stat. A key from another install, an empty file, a
 * directory: all passed, and the restore then came back with every destination
 * keyUnreadable -- the exact outcome the missing-key message already warned
 * about, reached by a different door. The fixtures live in
 * verifybackup_readonly_test.go.
 */

func TestVerifyBackupRefusesASecretKeyThatIsNotThisDatabases(t *testing.T) {
	// Well-formed, 32 bytes of hex -- and from another install. The only way
	// to tell is to try it on something this database sealed.
	_, sealedWith := hexKey(t, 0x33)
	foreign, _ := hexKey(t, 0x44)
	dir := liveWALBackup(t, sealedWith, foreign)

	err := VerifyBackup(dir)
	if err == nil {
		t.Fatal("accepted a secret.key that opens none of the backup's sealed values; " +
			"the restore would bring every credential back unreadable")
	}
	if !strings.Contains(err.Error(), "mqtt_creds") {
		t.Errorf("the error does not name what the key failed to open: %v", err)
	}
}

func TestVerifyBackupChecksTheKeyAgainstEverySealedTable(t *testing.T) {
	// A key that opens one table and not another is still the wrong key for a
	// restore: the check is per sealed column, not "any value anywhere".
	keyBody, a := hexKey(t, 0x55)
	_, b := hexKey(t, 0x66)
	dir := liveWALBackup(t, a, keyBody)

	// Add a platform secret sealed under a DIFFERENT key, straight into the
	// backup's own files (this test is about the key, not about writes).
	d, err := Open(filepath.Join(dir, "polyemesis.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.PutPlatformCreds(b, PlatformYouTube, "client", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	err = VerifyBackup(dir)
	if err == nil {
		t.Fatal("accepted a key that opens mqtt_creds but not platform_creds")
	}
	if !strings.Contains(err.Error(), "platform_creds") {
		t.Errorf("the error does not name the table the key failed on: %v", err)
	}
}

func TestVerifyBackupToleratesOneRowItsOwnKeyCannotOpen(t *testing.T) {
	// The other side of the per-column check, and why it asks "does the key
	// open ANY value here" rather than "does it open every one". A live
	// database can already carry a row its own key cannot open -- a leftover
	// of an earlier bad restore -- and refusing the backup over it would
	// refuse the only good copy the operator has.
	keyBody, own := hexKey(t, 0x88)
	_, stray := hexKey(t, 0x99)
	dir := liveWALBackup(t, own, keyBody)
	d, err := Open(filepath.Join(dir, "polyemesis.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.PutPlatformCreds(stray, PlatformTwitch, "client", "unreadable-already"); err != nil {
		t.Fatal(err)
	}
	if err := d.PutPlatformCreds(own, PlatformYouTube, "client", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBackup(dir); err != nil {
		t.Fatalf("refused a backup whose key opens its platform_creds, over one stray row: %v", err)
	}
}

func TestVerifyBackupRefusesAMalformedSecretKey(t *testing.T) {
	// Each of these was accepted, and each makes the restored server refuse to
	// boot -- found on the day of the restore, not the day of the backup.
	_, box := hexKey(t, 0x77)
	for name, write := range map[string]func(path string) error{
		"empty":        func(p string) error { return os.WriteFile(p, nil, 0o600) },
		"not hex":      func(p string) error { return os.WriteFile(p, []byte(strings.Repeat("zz", 32)), 0o600) },
		"wrong length": func(p string) error { return os.WriteFile(p, []byte("deadbeef"), 0o600) },
		"a directory":  func(p string) error { return os.Mkdir(p, 0o700) },
	} {
		t.Run(name, func(t *testing.T) {
			dir := liveWALBackup(t, box, "placeholder")
			p := filepath.Join(dir, "secret.key")
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if err := write(p); err != nil {
				t.Fatal(err)
			}
			err := VerifyBackup(dir)
			if err == nil {
				t.Fatalf("accepted a secret.key that is %s", name)
			}
			if !strings.Contains(err.Error(), "secret.key") {
				t.Errorf("the error does not say the key is the problem: %v", err)
			}
		})
	}
}

// TestVerifyBackupKnowsEverySealedColumn is the drift guard for the list the
// key check walks. A new sealed column that the list does not name would be a
// credential a foreign key could fail to open with verify still saying yes.
// The census is the same one TestNoNewSecretColumnEscapesSealing takes: every
// BLOB column whose name reads as a credential, from schema.sql and from every
// ALTER TABLE ... ADD COLUMN in this package.
func TestVerifyBackupKnowsEverySealedColumn(t *testing.T) {
	secretName := regexp.MustCompile(`(?i)(key|secret|password|token|credential)`)
	cols, _ := loadSecretColumnCandidates(t, secretName)
	var want []string
	for table, byCol := range cols {
		for col, typ := range byCol {
			if typ == "BLOB" {
				want = append(want, table+"."+col)
			}
		}
	}
	var got []string
	for _, c := range sealedColumns {
		got = append(got, c.table+"."+c.column)
	}
	sort.Strings(want)
	sort.Strings(got)
	if strings.Join(want, ",") != strings.Join(got, ",") {
		t.Errorf("sealedColumns in verifybackup.go is out of step with the schema.\n"+
			"schema has: %v\nverify checks: %v", want, got)
	}
}

func TestVerifyBackupSaysHowToEscapeAValueTheServerCannotOpenEither(t *testing.T) {
	// The case "opens none, not fails one" does not cover. mqtt_creds is a
	// singleton (CHECK id = 1): when the one password in it was sealed under a
	// key this server no longer has -- an earlier bad restore -- the server's
	// own key opens none of the column, exactly as a foreign key would, and
	// nothing in the database can tell the two apart. It stays refused: a
	// restore would bring that credential back unreadable either way.
	//
	// What must not happen is the old advice. "Take the backup again with this
	// server's own secret.key" is what the operator just did, and the next
	// backup fails the same way -- update.sh refuses every upgrade and the
	// message sends them round the loop. The way out is to re-seal the value,
	// so the error has to say so, and say where.
	_, stranded := hexKey(t, 0xaa)
	own, _ := hexKey(t, 0xbb)
	dir := liveWALBackup(t, stranded, own)

	err := VerifyBackup(dir)
	if err == nil {
		t.Fatal("accepted a key that opens none of mqtt_creds; the restore would bring " +
			"the broker password back unreadable")
	}
	msg := err.Error()
	for _, want := range []string{
		"mqtt_creds.password_enc",
		"another install",            // cause 1: a foreign key
		"cannot open",                // cause 2: this server cannot open it either
		"MQTT broker password",       // the remedy names the credential...
		"Settings",                   // ...and where to re-enter or clear it
		"then take the backup again", // and only then back up again
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not say %q:\n%s", want, msg)
		}
	}
}

// TestEverySealedColumnSaysHowToReseal keeps the refusal's way out from going
// missing: a column added to sealedColumns without a remedy would print an
// empty instruction to exactly the operator who has no other way to find one.
func TestEverySealedColumnSaysHowToReseal(t *testing.T) {
	for _, c := range sealedColumns {
		if strings.TrimSpace(c.remedy) == "" {
			t.Errorf("%s.%s has no remedy for a value this server cannot open", c.table, c.column)
		}
	}
}
