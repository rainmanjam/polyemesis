package db

import (
	"path/filepath"
	"testing"
)

// Retyping ONE key must not destroy the other one's ciphertext.
//
// The recovery this protects is the ordinary one: a database restored without
// its secret.key. Every destination reads back with an empty StreamKey -- the
// fail-closed rule -- and is flagged KeyUnreadable. The sealed bytes are still
// there, and putting the right key file back returns every destination.
//
// The guard that keeps those bytes required BOTH halves to be empty. So an
// operator who gave up on the primary and retyped it took the re-sealing branch
// for both columns, and sealStreamKey("") returns nil bytes: the BACKUP's
// ciphertext became NULL. Recovering one half destroyed the other, silently,
// and no later secret.key could bring it back.
//
// Mutation: restore keepsSealedKey's `&&` form at both call sites in
// UpdateDestination. Observed to fail with the backup ciphertext gone.
func TestRetypingThePrimaryKeyLeavesTheBackupCiphertextAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "polyemesis.db")
	d := keyDB(t, path, WithSecretBox(testBox(t)))

	src := &Source{Name: "Main", Enabled: true, Ingest: DefaultSettings().Ingest}
	if err := d.CreateSource(src); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}

	saved, err2 := d.CreateDestination(&Destination{
		Name: "has both halves", Kind: DestRTMP, URL: "rtmp://example.invalid/live",
		StreamKey: "primary-secret", BackupStreamKey: "backup-secret",
		Enabled: false, AudioBitrate: 160,
	})
	if err2 != nil {
		t.Fatalf("CreateDestination: %v", err2)
	}

	sealedBefore := rawCol(t, d, "backup_stream_key_enc", saved.ID)
	if len(sealedBefore) == 0 {
		t.Fatal("the backup key was not sealed at all, so this test cannot show it surviving")
	}

	// The state after a restore with no key file: both halves read back empty
	// and the row is flagged. The operator retypes the PRIMARY only.
	row := *saved
	row.KeyUnreadable = "the stored key could not be opened with this secret.key"
	row.StreamKey = "primary-retyped"
	row.BackupStreamKey = ""
	if _, err := d.UpdateDestination(&row); err != nil {
		t.Fatalf("UpdateDestination: %v", err)
	}

	sealedAfter := rawCol(t, d, "backup_stream_key_enc", saved.ID)
	if len(sealedAfter) == 0 {
		t.Fatal("retyping the primary key destroyed the BACKUP key's ciphertext. " +
			"That is the one thing a restored secret.key could still have recovered, " +
			"and the operator was not asked and not told.")
	}
	if string(sealedAfter) != string(sealedBefore) {
		t.Error("the backup ciphertext was rewritten rather than left alone; " +
			"whatever it now holds was not sealed from the operator's backup key")
	}

	// The control: the half the operator DID retype must actually change, or
	// this test would pass on a write that did nothing at all.
	if got, err := d.GetDestination(saved.ID); err != nil {
		t.Fatalf("GetDestination: %v", err)
	} else if got.StreamKey != "primary-retyped" {
		t.Errorf("StreamKey = %q, want the retyped value -- the write was skipped "+
			"entirely, so the assertion above proves nothing", got.StreamKey)
	}
}

func rawCol(t *testing.T, d *DB, col string, id int64) []byte {
	t.Helper()
	var b []byte
	// A literal column name from this file, not from a caller.
	if err := d.sql.QueryRow(`SELECT `+col+` FROM destinations WHERE id=?`, id).Scan(&b); err != nil {
		t.Fatalf("read %s: %v", col, err)
	}
	return b
}
