package db

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/secrets"
)

/* -verify-backup WROTE TO THE BACKUP IT WAS CHECKING.
 *
 * It opened polyemesis.db read-write, so SQLite recovered the -wal into the
 * main file on open and deleted both sidecars on close: the copy an operator
 * keeps as their only way back came out of the check with different bytes, and
 * INSTALL.md says the check "writes nothing". On a read-only mount -- which is
 * how the Docker update.sh hands it the backup, `-v "$verify_dir:/backup:ro"`
 * -- the same open cannot create the -shm and fails outright, so every Docker
 * upgrade was refused.
 */

// hexKey returns a well-formed key file body and the Box it opens.
func hexKey(t *testing.T, fill byte) (string, *secrets.Box) {
	t.Helper()
	raw := bytes.Repeat([]byte{fill}, 32)
	box, err := secrets.New(raw)
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(raw), box
}

// liveWALBackup builds a backup the way a copy of a RUNNING server looks: the
// database is still open when its files are copied, so the last writes -- here,
// the sealed MQTT password -- exist only in the -wal. A check that ignored the
// -wal, or that could not open it, would miss exactly that data.
func liveWALBackup(t *testing.T, seal *secrets.Box, keyBody string) string {
	t.Helper()
	live := t.TempDir()
	d, err := Open(filepath.Join(live, "polyemesis.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if err := d.PutMQTTPassword(seal, "broker-password"); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	for _, name := range []string{"polyemesis.db", "polyemesis.db-wal"} {
		src, err := os.ReadFile(filepath.Join(live, name))
		if err != nil {
			t.Fatalf("copying %s from a live database: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), src, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if st, err := os.Stat(filepath.Join(dir, "polyemesis.db-wal")); err != nil || st.Size() == 0 {
		t.Fatalf("the fixture has no -wal content, so it cannot test WAL handling: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret.key"), []byte(keyBody), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// snapshot hashes every file in dir by name.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range ents {
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = hex.EncodeToString(h.Sum(nil))
	}
	return out
}

func TestVerifyBackupWritesNothingToTheBackupItChecks(t *testing.T) {
	keyBody, box := hexKey(t, 0x11)
	dir := liveWALBackup(t, box, keyBody)
	before := snapshot(t, dir)

	if err := VerifyBackup(dir); err != nil {
		t.Fatalf("a good live-copied WAL backup was rejected: %v", err)
	}

	after := snapshot(t, dir)
	if len(after) != len(before) {
		t.Errorf("the check changed which files the backup holds: before %v, after %v",
			keys(before), keys(after))
	}
	for name, sum := range before {
		if after[name] != sum {
			t.Errorf("the check rewrote %s in the backup it was verifying "+
				"(INSTALL.md promises it writes nothing)", name)
		}
	}
}

func TestVerifyBackupOpensAWALBackupInAReadOnlyDirectory(t *testing.T) {
	// The Docker update.sh shape: the backup is mounted :ro. Under root the
	// chmod below does not bind, and this test then proves only what the
	// byte-identity test above proves; as the ordinary user CI and developers
	// run as, it reproduces the refusal exactly (SQLite cannot create -shm).
	keyBody, box := hexKey(t, 0x22)
	dir := liveWALBackup(t, box, keyBody)
	for _, name := range []string{"polyemesis.db", "polyemesis.db-wal", "secret.key"} {
		if err := os.Chmod(filepath.Join(dir, name), 0o444); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	if err := VerifyBackup(dir); err != nil {
		t.Fatalf("a good backup in a read-only directory was rejected -- the "+
			"Docker update.sh mounts it exactly like this: %v", err)
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
