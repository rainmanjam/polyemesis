package db

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/secrets"
)

// Encryption at rest for destination stream keys.
//
// internal/secrets already sealed every OAuth token and client secret, and said
// what that bought: "a leaked database file -- a backup, a snapshot, an errant
// scp -- is not a leaked set of live streaming credentials." Destination stream
// keys were the one credential class left out of that sentence, and they are
// the worst one to leave out: long-lived, rarely rotated, and worth exactly
// what an attacker wants, which is the ability to broadcast to the owner's
// channel. Issue #297 is what happens when they sit in a readable file.
//
// The tests below are in four groups, and the middle two are the ones that
// matter for an upgrade rather than for a fresh install: the ciphertext is
// really written, the row the PREVIOUS release wrote still reads, the backfill
// finishes the job, and a key that will not open disables the destination
// instead of publishing with a wrong one.

// keyPath opens a database at a named path, so a test can close it and open it
// again with a different key -- which is the whole subject of half this file
// and cannot be expressed against testDB's anonymous temporary directory.
func keyDB(t *testing.T, path string, opts ...Option) *DB {
	t.Helper()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		tmpl, err := testTemplate()
		if err != nil {
			t.Fatalf("build template: %v", err)
		}
		if err := os.WriteFile(path, tmpl, 0o600); err != nil {
			t.Fatalf("write template copy: %v", err)
		}
	}
	d, err := Open(path, opts...)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// otherBox is a box with a DIFFERENT key from testDB's, which is what a
// restored database on a machine carrying its own generated key file looks
// like. 0x2a is testBox's; anything else will do here.
func otherBox(t *testing.T) *secrets.Box {
	t.Helper()
	box, err := secrets.New(bytes.Repeat([]byte{0x5c}, 32))
	if err != nil {
		t.Fatalf("secrets.New: %v", err)
	}
	return box
}

// rawKeyCols reads the four storage columns straight out of SQLite, bypassing
// every helper this change added. Asserting through GetDestination alone would
// pass against an implementation that stored the key in the clear and simply
// handed it back, which is the exact bug being guarded against.
func rawKeyCols(t *testing.T, d *DB, id int64) (plain string, enc []byte, backupPlain string, backupEnc []byte) {
	t.Helper()
	err := d.sql.QueryRow(`SELECT stream_key, stream_key_enc,
		backup_stream_key, backup_stream_key_enc FROM destinations WHERE id = ?`, id).
		Scan(&plain, &enc, &backupPlain, &backupEnc)
	if err != nil {
		t.Fatalf("read raw key columns of destination %d: %v", id, err)
	}
	return plain, enc, backupPlain, backupEnc
}

// ------------------------------------------------------- the ciphertext side

// Mutation: in sealStreamKey, return `enc, key, nil` so both columns are
// written. Observed to fail with "the plaintext column still holds
// \"abc-123\"". Restored from a file backup; git diff --stat empty.
//
// Mutation: in sealStreamKey, return `nil, key, nil` unconditionally -- i.e.
// never seal at all. Observed to fail on the same assertion.
func TestASealedStreamKeyIsNotLeftInThePlaintextColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyemesis.db")
	d := keyDB(t, path, WithSecretBox(testBox(t)))

	want := validDest()
	want.BackupStreamKey = "backup-987"
	row, err := d.CreateDestination(want)
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}

	plain, enc, backupPlain, backupEnc := rawKeyCols(t, d, row.ID)
	if plain != "" {
		t.Errorf("the plaintext column still holds %q: a leaked database is "+
			"still a leaked stream key, so the encryption bought nothing", plain)
	}
	if backupPlain != "" {
		t.Errorf("the backup plaintext column still holds %q", backupPlain)
	}
	if len(enc) == 0 {
		t.Fatal("nothing was written to stream_key_enc: the key is stored nowhere at all")
	}
	if len(backupEnc) == 0 {
		t.Fatal("nothing was written to backup_stream_key_enc")
	}
	// Not merely "different bytes": the plaintext must not be recoverable by
	// reading the blob, which a broken seal that only reordered would allow.
	if bytes.Contains(enc, []byte("abc-123")) {
		t.Error("the ciphertext contains the stream key verbatim")
	}
	if bytes.Contains(backupEnc, []byte("backup-987")) {
		t.Error("the backup ciphertext contains the backup stream key verbatim")
	}

	// And it still round trips, or the encryption has cost the operator the
	// destination.
	got, err := d.GetDestination(row.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if got.StreamKey != "abc-123" {
		t.Errorf("stream key read back as %q, want %q", got.StreamKey, "abc-123")
	}
	if got.BackupStreamKey != "backup-987" {
		t.Errorf("backup stream key read back as %q, want %q", got.BackupStreamKey, "backup-987")
	}
	if got.KeyUnreadable != "" {
		t.Errorf("a destination whose key opened fine is flagged anyway: %q", got.KeyUnreadable)
	}
}

// A rotation has to reach the ciphertext. An update that sealed nothing would
// leave the OLD key in the database and the new one only in the response, and
// the destination would go on publishing to the endpoint the operator just
// changed away from.
//
// Mutation: in UpdateDestination, drop the sealStreamKey calls and pass
// dst.StreamKey through to the plaintext column. Observed to fail with the
// reread returning the old key.
func TestUpdatingAStreamKeyReSealsItRatherThanLeavingTheOldOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyemesis.db")
	d := keyDB(t, path, WithSecretBox(testBox(t)))

	row, err := d.CreateDestination(validDest())
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	row.StreamKey = "rotated-456"
	if _, err := d.UpdateDestination(row); err != nil {
		t.Fatalf("UpdateDestination: %v", err)
	}

	got, err := d.GetDestination(row.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if got.StreamKey != "rotated-456" {
		t.Errorf("stream key after rotation is %q, want %q", got.StreamKey, "rotated-456")
	}
	plain, enc, _, _ := rawKeyCols(t, d, row.ID)
	if plain != "" {
		t.Errorf("the update wrote the rotated key to the plaintext column as %q", plain)
	}
	if bytes.Contains(enc, []byte("rotated-456")) {
		t.Error("the ciphertext contains the rotated key verbatim")
	}
}

// ------------------------------------------------ the upgrade, in two halves

// The first half. A row written by the PREVIOUS release has its key in the
// plaintext column and nothing sealed, and it has to keep working from the very
// first read -- before the backfill, in fact, because Open backfills and then
// something reads, and a crash in between must not cost the operator a
// destination.
//
// Mutation: in openStreamKey, `return "", nil` instead of `return plain, nil`
// for the empty-ciphertext case. Observed to fail with the pre-upgrade key
// reading back as "". Restored from a file backup; git diff --stat empty.
func TestARowFromBeforeTheUpgradeStillReadsItsPlaintextKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyemesis.db")
	d := keyDB(t, path, WithSecretBox(testBox(t)))

	want := validDest()
	want.Enabled = true
	row, err := d.CreateDestination(want)
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	// Put the row back into the shape the previous release wrote: plaintext
	// present, ciphertext absent. Done in SQL rather than through the store
	// because the store is what no longer produces this shape.
	if _, err := d.sql.Exec(`UPDATE destinations SET stream_key = ?, stream_key_enc = NULL,
		backup_stream_key = ?, backup_stream_key_enc = NULL WHERE id = ?`,
		"legacy-key", "legacy-backup", row.ID); err != nil {
		t.Fatalf("write a pre-upgrade row: %v", err)
	}

	got, err := d.GetDestination(row.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if got.StreamKey != "legacy-key" {
		t.Errorf("a pre-upgrade row's stream key read back as %q, want %q. "+
			"Every destination on an upgraded install just stopped working.",
			got.StreamKey, "legacy-key")
	}
	if got.BackupStreamKey != "legacy-backup" {
		t.Errorf("a pre-upgrade row's backup key read back as %q, want %q",
			got.BackupStreamKey, "legacy-backup")
	}
	if got.KeyUnreadable != "" {
		t.Errorf("a pre-upgrade row is flagged as unreadable: %q", got.KeyUnreadable)
	}
	if !got.Enabled {
		t.Error("a pre-upgrade row was disabled by the read path")
	}
}

// The second half, and the one that makes the change worth making. The fallback
// above keeps an upgraded install working; on its own it would ALSO keep every
// stream key in the clear for ever.
//
// Mutation: delete the backfillDestinationStreamKeys call from Open. Observed
// to fail with "the plaintext column still holds \"legacy-key\" after an open
// with a key". Restored from a file backup; git diff --stat empty.
//
// Mutation: change the backfill's UPDATE to leave stream_key alone (drop
// `stream_key=”`). Observed to fail on the same assertion, which is the point
// of asserting the blanking separately from the sealing.
func TestOpeningWithAKeySealsThePlaintextItFindsAndBlanksIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyemesis.db")
	box := testBox(t)

	first := keyDB(t, path, WithSecretBox(box))
	row, err := first.CreateDestination(validDest())
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	if _, err := first.sql.Exec(`UPDATE destinations SET stream_key = ?, stream_key_enc = NULL,
		backup_stream_key = ?, backup_stream_key_enc = NULL WHERE id = ?`,
		"legacy-key", "legacy-backup", row.ID); err != nil {
		t.Fatalf("write a pre-upgrade row: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second := keyDB(t, path, WithSecretBox(box))
	plain, enc, backupPlain, backupEnc := rawKeyCols(t, second, row.ID)
	if plain != "" {
		t.Errorf("the plaintext column still holds %q after an open with a key: "+
			"the upgrade never finishes and the keys stay in the clear for ever", plain)
	}
	if backupPlain != "" {
		t.Errorf("the backup plaintext column still holds %q after the backfill", backupPlain)
	}
	if len(enc) == 0 || len(backupEnc) == 0 {
		t.Fatal("the backfill blanked the plaintext without sealing it: the key is gone")
	}
	got, err := second.GetDestination(row.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if got.StreamKey != "legacy-key" || got.BackupStreamKey != "legacy-backup" {
		t.Fatalf("the backfill changed the key: got %q/%q, want %q/%q",
			got.StreamKey, got.BackupStreamKey, "legacy-key", "legacy-backup")
	}

	// IDEMPOTENT. This runs on every open, for the life of the install, and the
	// second pass must leave the row exactly as usable as the first did.
	//
	// Deliberately NOT asserting that the ciphertext bytes are unchanged. That
	// reads like the stronger claim and is in fact an unfalsifiable one: with
	// the plaintext column already blank there is nothing for a second pass to
	// seal, so a backfill that selected every row on every open would write the
	// same bytes back and the assertion would hold. What IS falsifiable, and
	// what an operator would feel, is the row still being readable and still
	// being free of plaintext -- so that is what is asserted.
	if err := second.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	third := keyDB(t, path, WithSecretBox(box))
	againPlain, againEnc, _, _ := rawKeyCols(t, third, row.ID)
	if againPlain != "" {
		t.Errorf("a second open reintroduced plaintext %q", againPlain)
	}
	if len(againEnc) == 0 {
		t.Error("a second open left the row with no ciphertext: the backfill is not " +
			"idempotent and every restart is another chance to lose a key")
	}
	again, err := third.GetDestination(row.ID)
	if err != nil {
		t.Fatalf("GetDestination after a second open: %v", err)
	}
	if again.StreamKey != "legacy-key" {
		t.Errorf("after a second open the key reads %q, want %q", again.StreamKey, "legacy-key")
	}
}

// A row that has ALREADY been sealed must not be touched by the backfill, and
// the way that goes wrong is subtle: the WHERE clause matches on the backup
// column too, so a row with a sealed primary and a plaintext backup is selected
// -- and a backfill that sealed both columns blindly would seal "" over the
// primary's ciphertext and destroy a live credential.
//
// Mutation: in the backfill, drop the `if p.key != ""` guard so the primary is
// always resealed from its (empty) plaintext. Observed to fail with the primary
// key reading back as "". Restored from a file backup; git diff --stat empty.
func TestTheBackfillDoesNotSealEmptyOverAnAlreadySealedKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyemesis.db")
	box := testBox(t)

	first := keyDB(t, path, WithSecretBox(box))
	row, err := first.CreateDestination(validDest())
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	// Primary sealed (as created), backup left in the old plaintext shape.
	if _, err := first.sql.Exec(`UPDATE destinations SET backup_stream_key = ?,
		backup_stream_key_enc = NULL WHERE id = ?`, "legacy-backup", row.ID); err != nil {
		t.Fatalf("write a half-migrated row: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second := keyDB(t, path, WithSecretBox(box))
	got, err := second.GetDestination(row.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if got.StreamKey != "abc-123" {
		t.Errorf("the backfill destroyed the already-sealed primary key: read %q, want %q",
			got.StreamKey, "abc-123")
	}
	if got.BackupStreamKey != "legacy-backup" {
		t.Errorf("the backfill did not pick up the plaintext backup key: read %q, want %q",
			got.BackupStreamKey, "legacy-backup")
	}
}

// ------------------------------------------------------------- fail closed

// THE DECISION THIS FEATURE TURNS ON. The key file was lost, replaced, or the
// database was restored onto a different machine. Nothing can read the key, so
// nothing may publish with it -- an empty key is not a fallback, it is a
// connection to somebody's ingest with the wrong credential.
//
// Mutation: in scanDestination, delete `dst.Enabled = false` from the failure
// branch. Observed to fail with "a destination whose key cannot be read is
// still enabled". Restored from a file backup; git diff --stat empty.
//
// Mutation: delete `dst.StreamKey, dst.BackupStreamKey = "", ""`. Observed to
// fail with the flagged destination still carrying the plaintext column's
// value, which is the empty-key publish this exists to stop.
//
// Mutation: in openStreamKey, return `out, nil` on the Open error. Observed to
// fail with "not flagged at all".
func TestAKeyThatWillNotDecryptDisablesTheDestinationAndSaysWhy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyemesis.db")

	first := keyDB(t, path, WithSecretBox(testBox(t)))
	want := validDest()
	want.Enabled = true
	want.BackupStreamKey = "backup-987"
	row, err := first.CreateDestination(want)
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// The same database, a different key file. This is a restore onto a machine
	// that generated its own.
	second := keyDB(t, path, WithSecretBox(otherBox(t)))
	got, err := second.GetDestination(row.ID)
	if err != nil {
		t.Fatalf("GetDestination refused the row outright: %v. A lost key file "+
			"must not be a dashboard that will not load.", err)
	}
	if got.KeyUnreadable == "" {
		t.Fatal("a destination whose stream key cannot be decrypted is not flagged " +
			"at all: the operator has no way to know why it stopped")
	}
	if got.Enabled {
		t.Error("a destination whose key cannot be read is still enabled: it will " +
			"be started, and it will publish with an empty stream key")
	}
	if got.StreamKey != "" {
		t.Errorf("a destination whose key cannot be read still carries %q", got.StreamKey)
	}
	if got.BackupStreamKey != "" {
		t.Errorf("a destination whose key cannot be read still carries backup key %q",
			got.BackupStreamKey)
	}
	// It stays in the list, with everything else intact. A destination that
	// vanishes is one the operator cannot fix, and cannot even see to delete.
	if got.Name != "Main" || got.Kind != DestRTMP || got.URL != "rtmp://ingest.example/live" {
		t.Errorf("the flagged destination lost its identity: %q / %q / %q",
			got.Name, got.Kind, got.URL)
	}
	list, err := second.ListDestinations()
	if err != nil {
		t.Fatalf("ListDestinations: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("the destination list has %d rows, want 1: a flagged destination "+
			"was dropped from the list instead of being shown as needing attention", len(list))
	}
	// The reason has to be actionable, not "decryption failed".
	if !strings.Contains(got.KeyUnreadable, "re-enter") {
		t.Errorf("the reason shown to the operator is %q, which does not say what to do",
			got.KeyUnreadable)
	}
	// And it must not leak the ciphertext or anything else into the message,
	// because DestStatus warnings reach a retained MQTT topic.
	if strings.Contains(got.KeyUnreadable, "abc-123") {
		t.Error("the reason shown to the operator contains the stream key")
	}
}

// A row carrying BOTH a plaintext key and a ciphertext, which is not a corrupt
// row: it is what a DOWNGRADE produces. The operator upgrades, rolls back to
// the previous release for an evening, and edits a destination there -- that
// binary writes stream_key and has never heard of stream_key_enc, so the row
// ends up with a fresh plaintext key beside a stale sealed one.
//
// The next open with a key resolves it IN FAVOUR OF THE PLAINTEXT, and that is
// the correct way round rather than an accident of ordering. A non-empty
// plaintext column can only have been written by a binary that does not seal,
// so it is by construction the more recent of the two; the ciphertext beside it
// is whatever the row held before the rollback. Preferring the ciphertext here
// would quietly restore a key the operator changed away from.
//
// Mutation: in the backfill, skip columns that already carry a ciphertext
// (`if p.key != "" && len(p.keyEnc) == 0`). Observed to fail with the key
// reading back as the pre-rollback "abc-123". Restored from a file backup;
// verified byte-identical with cmp.
func TestAKeyWrittenByADowngradedBinaryWinsOverTheStaleCiphertext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyemesis.db")
	box := testBox(t)

	first := keyDB(t, path, WithSecretBox(box))
	want := validDest()
	want.Enabled = true
	row, err := first.CreateDestination(want)
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	// What the older binary's UPDATE does: the plaintext column, and nothing
	// said about the sealed one, which keeps whatever was already in it.
	if _, err := first.sql.Exec(
		`UPDATE destinations SET stream_key = ? WHERE id = ?`,
		"typed-during-the-rollback", row.ID); err != nil {
		t.Fatalf("write a downgraded row: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second := keyDB(t, path, WithSecretBox(box))
	got, err := second.GetDestination(row.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if got.StreamKey != "typed-during-the-rollback" {
		t.Errorf("the key reads back as %q, want %q: the rollback's edit was "+
			"discarded in favour of the key it replaced", got.StreamKey,
			"typed-during-the-rollback")
	}
	if got.KeyUnreadable != "" {
		t.Errorf("the healed row is flagged: %q", got.KeyUnreadable)
	}
	if plain, _, _, _ := rawKeyCols(t, second, row.ID); plain != "" {
		t.Errorf("the plaintext the rollback wrote is still there as %q", plain)
	}
}

// The same downgraded row, read by a caller with NO key configured at all --
// an embedded user of this package, or any process that opens the database
// without a box. The backfill does not run, so both columns survive to the
// read, and this is the one configuration in which the blanking is observable.
//
// It must publish NEITHER. The ciphertext is authoritative when it is present
// and this reader cannot open it; the plaintext beside it is the older binary's
// and may or may not still be current. Handing back the plaintext would be a
// guess at a live credential, and handing back the struct with StreamKey still
// populated would break what KeyUnreadable promises -- that a flagged
// destination carries no key for anything downstream to publish with.
//
// Mutation: in scanDestination's failure branch, delete
// `dst.StreamKey, dst.BackupStreamKey = "", ""`. Observed to fail with the
// flagged destination still carrying "typed-during-the-rollback". Restored
// from a file backup; verified byte-identical with cmp.
func TestAFlaggedRowCarriesNoKeyEvenWhenAPlaintextColumnSurvives(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyemesis.db")

	first := keyDB(t, path, WithSecretBox(testBox(t)))
	want := validDest()
	want.Enabled = true
	want.BackupStreamKey = "backup-987"
	row, err := first.CreateDestination(want)
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	if _, err := first.sql.Exec(
		`UPDATE destinations SET stream_key = ?, backup_stream_key = ? WHERE id = ?`,
		"typed-during-the-rollback", "backup-during-the-rollback", row.ID); err != nil {
		t.Fatalf("write a downgraded row: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second := keyDB(t, path)
	got, err := second.GetDestination(row.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if got.KeyUnreadable == "" {
		t.Fatal("a row whose ciphertext cannot be opened is not flagged because " +
			"there happened to be a plaintext column beside it")
	}
	if got.StreamKey != "" {
		t.Errorf("the flagged destination still carries %q from the plaintext column. "+
			"The two columns disagree, nothing here can say which is current, and "+
			"KeyUnreadable promises the caller there is no key to publish with.",
			got.StreamKey)
	}
	if got.BackupStreamKey != "" {
		t.Errorf("the flagged destination still carries backup key %q", got.BackupStreamKey)
	}
	if got.Enabled {
		t.Error("the flagged destination is still enabled")
	}
}

// Sealed bytes and NO box at all. Same failure, different route: an install
// that had encryption configured and no longer does, or a data directory
// mounted after the process started. It must fail closed too -- falling back to
// the plaintext column here is a fallback onto a column that is blank for
// exactly these rows, which publishes an empty key.
//
// Mutation: in openStreamKey, `return plain, nil` for the nil-box case.
// Observed to fail with "a sealed destination read without a key is not
// flagged". Restored from a file backup; git diff --stat empty.
func TestASealedRowReadWithoutAnyKeyFailsClosedRatherThanFallingBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyemesis.db")

	first := keyDB(t, path, WithSecretBox(testBox(t)))
	want := validDest()
	want.Enabled = true
	row, err := first.CreateDestination(want)
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second := keyDB(t, path)
	got, err := second.GetDestination(row.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if got.KeyUnreadable == "" {
		t.Fatal("a sealed destination read without a key is not flagged")
	}
	if got.Enabled || got.StreamKey != "" {
		t.Errorf("a sealed destination read without a key is enabled=%v with key %q: "+
			"it would publish with an empty stream key", got.Enabled, got.StreamKey)
	}
}

// The failure is NOT destructive, and this is what makes "put the key file
// back" a real recovery rather than advice. Nothing about the flagged read
// writes to the row: enabled is still 1 and the ciphertext is untouched, so the
// correct key brings every destination back by itself.
//
// Mutation: make scanDestination persist the disable with a
// SetDestinationEnabled call. Observed to fail with the destination still
// disabled after the right key returned. Restored from a file backup; git diff
// --stat empty.
func TestPuttingTheRightKeyBackRestoresTheDestination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyemesis.db")
	box := testBox(t)

	first := keyDB(t, path, WithSecretBox(box))
	want := validDest()
	want.Enabled = true
	row, err := first.CreateDestination(want)
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	wrong := keyDB(t, path, WithSecretBox(otherBox(t)))
	if got, err := wrong.GetDestination(row.ID); err != nil || got.KeyUnreadable == "" {
		t.Fatalf("the wrong key did not flag the destination: %v", err)
	}
	if err := wrong.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	restored := keyDB(t, path, WithSecretBox(box))
	got, err := restored.GetDestination(row.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if got.KeyUnreadable != "" {
		t.Errorf("the destination is still flagged after the right key came back: %q",
			got.KeyUnreadable)
	}
	if got.StreamKey != "abc-123" {
		t.Errorf("the stream key is %q after the right key came back, want %q",
			got.StreamKey, "abc-123")
	}
	if !got.Enabled {
		t.Error("the destination is still disabled after the right key came back: " +
			"the failure was written to the row, so recovery needs a manual re-enable " +
			"of every destination on the install")
	}
}

// The data-loss guard. An operator whose key file is missing renames a
// destination -- or the API's update handler decodes a partial body over the
// row it just read, which is the same thing -- and the merged row carries an
// empty stream key. Sealing that over the ciphertext would destroy a credential
// that the right key file would have recovered, and nobody was asked.
//
// Mutation: delete the keepsSealedKey branch from UpdateDestination so the
// empty key is always sealed. Observed to fail with "the sealed key was
// destroyed by an unrelated edit". Restored from a file backup; git diff --stat
// empty.
func TestAnUnrelatedEditDoesNotDestroyAKeyItCannotRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyemesis.db")
	box := testBox(t)

	first := keyDB(t, path, WithSecretBox(box))
	row, err := first.CreateDestination(validDest())
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	wrong := keyDB(t, path, WithSecretBox(otherBox(t)))
	stale, err := wrong.GetDestination(row.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	stale.Name = "renamed while the key was missing"
	if _, err := wrong.UpdateDestination(stale); err != nil {
		t.Fatalf("UpdateDestination: %v", err)
	}
	if err := wrong.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	restored := keyDB(t, path, WithSecretBox(box))
	got, err := restored.GetDestination(row.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if got.StreamKey != "abc-123" {
		t.Errorf("the sealed key was destroyed by an unrelated edit: read %q, want %q. "+
			"Restoring the key file no longer recovers this destination.",
			got.StreamKey, "abc-123")
	}
	// The edit itself still has to have landed, or the guard has turned an
	// unreadable key into a read-only destination.
	if got.Name != "renamed while the key was missing" {
		t.Errorf("the rename was discarded: name is %q", got.Name)
	}
}

// The exit from the flagged state, and the one the operator is told to take.
// Typing a key in must overwrite the unopenable ciphertext -- the guard above
// must not have made the destination permanently unfixable.
//
// Mutation: drop the `dst.StreamKey == ""` condition from keepsSealedKey so a
// supplied key is preserved away too. Observed to fail with the re-entered key
// reading back as "". Restored from a file backup; git diff --stat empty.
func TestReEnteringTheKeyClearsTheFlagAndEnablesTheDestination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyemesis.db")

	first := keyDB(t, path, WithSecretBox(testBox(t)))
	row, err := first.CreateDestination(validDest())
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second := keyDB(t, path, WithSecretBox(otherBox(t)))
	stale, err := second.GetDestination(row.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if stale.KeyUnreadable == "" {
		t.Fatal("the destination was not flagged, so this test proves nothing")
	}
	stale.StreamKey = "typed-in-again"
	stale.Enabled = true
	if _, err := second.UpdateDestination(stale); err != nil {
		t.Fatalf("UpdateDestination: %v", err)
	}

	got, err := second.GetDestination(row.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if got.KeyUnreadable != "" {
		t.Errorf("the destination is still flagged after the key was re-entered: %q",
			got.KeyUnreadable)
	}
	if got.StreamKey != "typed-in-again" {
		t.Errorf("the re-entered key reads back as %q, want %q. The operator was told "+
			"to re-enter it and re-entering it does not work.", got.StreamKey, "typed-in-again")
	}
	if !got.Enabled {
		t.Error("the destination could not be re-enabled after its key was re-entered")
	}
}

// ------------------------------------------------------------- no box at all

// An embedded caller, and every test in this repo that does not pass a box, has
// no key file to give. That configuration is supported and must behave exactly
// as it did before this change: plaintext in, plaintext out, nothing sealed and
// nothing flagged.
//
// Mutation: in sealStreamKey, drop the `d.box == nil` guard and return
// `nil, "", nil` -- which is what an implementation that assumed a box always
// exists would store. Observed to fail with the plaintext column holding ""
// instead of "abc-123". Restored from a file backup; git diff --stat empty.
//
// Mutation: in openStreamKey, flag every row whose ciphertext is empty rather
// than falling back. Observed to fail with "a destination stored without a key
// is flagged". Restored from a file backup; git diff --stat empty.
func TestWithNoKeyConfiguredTheStreamKeyIsStoredAsItAlwaysWas(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyemesis.db")
	d := keyDB(t, path)

	want := validDest()
	want.Enabled = true
	want.BackupStreamKey = "backup-987"
	row, err := d.CreateDestination(want)
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}

	plain, enc, backupPlain, backupEnc := rawKeyCols(t, d, row.ID)
	if plain != "abc-123" {
		t.Errorf("with no key configured the plaintext column holds %q, want %q: "+
			"an embedded caller's destinations are stored somewhere it cannot read",
			plain, "abc-123")
	}
	if backupPlain != "backup-987" {
		t.Errorf("with no key configured the backup plaintext column holds %q", backupPlain)
	}
	if len(enc) != 0 || len(backupEnc) != 0 {
		t.Error("something was sealed with no key configured")
	}

	got, err := d.GetDestination(row.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if got.StreamKey != "abc-123" || got.BackupStreamKey != "backup-987" {
		t.Errorf("read back %q/%q, want %q/%q", got.StreamKey, got.BackupStreamKey,
			"abc-123", "backup-987")
	}
	if got.KeyUnreadable != "" {
		t.Errorf("a destination stored without a key is flagged: %q", got.KeyUnreadable)
	}
	if !got.Enabled {
		t.Error("a destination stored without a key was disabled")
	}
}

// ---------------------------------------------- the bytes, not the SQL values

// rawResidue counts occurrences of needle in the raw bytes of the database file
// and its write-ahead log.
//
// THE POINT IS THAT IT DOES NOT GO THROUGH SQLITE. Every other test in this
// file asks SQLite what the row says, and SQLite answers from the current
// b-tree cell -- which is exactly the view that cannot see a freed page, a
// shortened cell's tail, or a stale frame in the -wal. A test that reads the
// row back through SQL "confirms" the plaintext is gone while `grep` on the
// same file still finds it, which is the vacuous shape this repo keeps
// catching. os.ReadFile and bytes.Count is the whole method, deliberately.
func rawResidue(t *testing.T, path, needle string) (db, wal int) {
	t.Helper()
	count := func(p string) int {
		b, err := os.ReadFile(p)
		if err != nil {
			// No -wal is a legitimate state, and a clean one.
			return 0
		}
		return bytes.Count(b, []byte(needle))
	}
	return count(path), count(path + "-wal")
}

// residueRows is the size of the pre-upgrade fixture below, and it is not
// arbitrary. The residue lives in freed pages and in shortened cells' tails,
// and a database with a SINGLE destination in it has neither: the sealed value
// overwrites the plaintext in place, `grep` finds nothing, and the test passes
// against the completely unfixed code -- measured, before this was widened.
// Sixty rows plus some churn is what makes the b-tree move cells around, which
// is what a real install's file looks like by the time it is upgraded.
const residueRows = 60

func residueNeedle(i int) string {
	return fmt.Sprintf("live_%03d_ZZplaintextstreamkeymaterial", i)
}

// residueTotal sums the raw-byte occurrences of every fixture key.
func residueTotal(t *testing.T, path string) (db, wal int) {
	t.Helper()
	for i := 0; i < residueRows; i++ {
		d, w := rawResidue(t, path, residueNeedle(i))
		db += d
		wal += w
	}
	return db, wal
}

// THE CLEAN-SHUTDOWN HALF, which is what secure_delete is for.
//
// The previous release closed cleanly, so SQLite checkpointed and removed the
// -wal: every plaintext key is in the main database file. The backfill's UPDATE
// shortens each stream_key cell to the empty string, and without secure_delete
// SQLite unlinks those bytes without zeroing them, leaving them legible in the
// freed space.
//
// Mutation: drop `&_pragma=secure_delete(ON)` from Open's DSN in db.go.
// Observed to fail with "2 copies of the plaintext stream key are still
// greppable ... (db=2 wal=0)". Restored with `command cp -f` from a file
// backup; `diff` against the backup reported IDENTICAL afterwards.
//
// Mutation: delete the `PRAGMA wal_checkpoint(TRUNCATE)` at the end of
// backfillDestinationStreamKeys. Observed to fail on the same assertion with
// db=60 wal=0 -- every key, not a residual two, because until the checkpoint
// runs the sealed pages are only in the -wal and the main file still holds the
// pre-upgrade ones verbatim. Restored; `diff` reported IDENTICAL.
//
// Mutation: weaken the checkpoint to `PRAGMA wal_checkpoint(PASSIVE)`. THIS
// TEST STILL PASSES -- stated rather than omitted. With one connection and no
// competing reader, PASSIVE does copy everything back, so this fixture cannot
// tell the two apart. The one below can, and does.
func TestTheBackfillLeavesNoPlaintextInTheRawDatabaseBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyemesis.db")
	box := testBox(t)

	const rows = residueRows
	needle := residueNeedle

	first := keyDB(t, path)
	ids := make([]int64, 0, rows)
	for i := 0; i < rows; i++ {
		d := validDest()
		d.Name = fmt.Sprintf("dest-%03d", i)
		row, err := first.CreateDestination(d)
		if err != nil {
			t.Fatalf("CreateDestination: %v", err)
		}
		ids = append(ids, row.ID)
		// Straight to the columns: CreateDestination on a box-less database
		// already stores plaintext, but writing it explicitly says what the
		// fixture is and survives a change to that default.
		if _, err := first.sql.Exec(
			`UPDATE destinations SET stream_key = ?, stream_key_enc = NULL WHERE id = ?`,
			needle(i), row.ID); err != nil {
			t.Fatalf("write a pre-upgrade row: %v", err)
		}
	}
	for i, id := range ids {
		if i%3 != 0 {
			continue
		}
		if _, err := first.sql.Exec(`UPDATE destinations SET name = ? WHERE id = ?`,
			fmt.Sprintf("renamed-%03d", i), id); err != nil {
			t.Fatalf("churn a row: %v", err)
		}
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// The fixture has to be a real one. If the plaintext is not in the file
	// BEFORE the upgrade, the assertion after it proves nothing at all.
	var before int
	for i := 0; i < rows; i++ {
		db, wal := rawResidue(t, path, needle(i))
		before += db + wal
	}
	if before < rows {
		t.Fatalf("only %d plaintext keys are in the raw file bytes before the upgrade, "+
			"want at least %d: the fixture is not a pre-0.7.0 install and the "+
			"assertion below would pass vacuously", before, rows)
	}

	// The upgrade. Nothing else -- no VACUUM, no manual checkpoint, no second
	// open. This is the state an operator is in the moment their upgraded
	// server finishes starting.
	second := keyDB(t, path, WithSecretBox(box))

	var totalDB, totalWAL int
	for i := 0; i < rows; i++ {
		db, wal := rawResidue(t, path, needle(i))
		totalDB += db
		totalWAL += wal
	}
	if totalDB+totalWAL != 0 {
		t.Errorf("%d copies of the plaintext stream key are still greppable out of the "+
			"raw file bytes after the seal-at-rest backfill (db=%d wal=%d): the "+
			"stream_key columns read back empty, so every SQL-level check passes "+
			"while a leaked database file is still a leaked set of live streaming "+
			"credentials -- which is the one thing package secrets says it buys",
			totalDB+totalWAL, totalDB, totalWAL)
	}

	// And the keys survived the scrubbing, which is the half that makes the
	// assertion above worth passing rather than merely easy to pass.
	for i, id := range ids {
		got, err := second.GetDestination(id)
		if err != nil {
			t.Fatalf("GetDestination(%d): %v", id, err)
		}
		if got.StreamKey != needle(i) {
			t.Fatalf("destination %d reads back %q, want %q: the residue was removed "+
				"by losing the key", id, got.StreamKey, needle(i))
		}
	}
}

// THE UNCLEAN-SHUTDOWN HALF, which is what wal_checkpoint(TRUNCATE) is for.
//
// A clean Close checkpoints and unlinks the -wal, so the test above never sees
// a -wal carrying plaintext. A server that was SIGKILLed, OOM-killed, or lost
// its host does not get that: the -wal stays on disk with the plaintext frames
// in it, and the upgrade opens on top of it. That file is the one the fsperm
// pass in Open had to be extended to cover, for the same reason -- a reader who
// cannot open the database can still read recent writes out of the log.
//
// The fixture leaves the pre-upgrade handle OPEN rather than closing it, which
// is how a -wal is made to survive from inside one process. It also makes the
// pre-upgrade connection an active reader, which is precisely the condition
// under which a PASSIVE checkpoint copies nothing at all.
//
// Mutation: weaken the checkpoint to `PRAGMA wal_checkpoint(PASSIVE)`. Observed
// to fail with "122 copies ... (db=0 wal=122)" -- every key, untouched, because
// PASSIVE declined to checkpoint against the live reader and returned no error
// while doing so. Restored with `command cp -f`; `diff` against the backup
// reported IDENTICAL.
//
// Mutation: delete the checkpoint entirely. Observed to fail the same way,
// db=0 wal=122.
//
// Mutation: drop `&_pragma=secure_delete(ON)` from the DSN. THIS TEST STILL
// FAILS but for the -wal, not the freed pages; the two tests overlap there and
// only the one above isolates secure_delete.
func TestTheBackfillLeavesNoPlaintextInASurvivingWriteAheadLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyemesis.db")
	box := testBox(t)

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
			t.Fatalf("write a pre-upgrade row: %v", err)
		}
	}
	// NO Close. The handle stays open for the rest of the test, which is what
	// keeps the -wal on disk and keeps a reader active against it.

	if _, wal := residueTotal(t, path); wal == 0 {
		t.Fatal("no plaintext in the -wal before the upgrade: the fixture did not " +
			"reproduce a killed pre-0.7.0 server and the assertion below is vacuous")
	}

	second := keyDB(t, path, WithSecretBox(box))

	db, wal := residueTotal(t, path)
	if db+wal != 0 {
		t.Errorf("%d copies of the plaintext stream key are still greppable out of the "+
			"raw file bytes after the seal-at-rest backfill (db=%d wal=%d). The -wal "+
			"is a leak in its own right: a reader who cannot open the database still "+
			"sees recent writes out of the log, and on an install that predates the "+
			"fsperm pass it is world-readable besides",
			db+wal, db, wal)
	}

	for i, id := range ids {
		got, err := second.GetDestination(id)
		if err != nil {
			t.Fatalf("GetDestination(%d): %v", id, err)
		}
		if got.StreamKey != residueNeedle(i) {
			t.Fatalf("destination %d reads back %q, want %q: the residue was removed "+
				"by losing the key", id, got.StreamKey, residueNeedle(i))
		}
	}
}

// secure_delete is set in the DSN rather than executed as a statement, and the
// difference is not cosmetic: it governs writes made while it is on, so the
// backfill has to run under it. Asserting the pragma's value is the only way to
// see that the DSN spelling actually took -- a misspelt `_pragma=` parameter is
// not an error, it is silently ignored.
//
// Mutation: drop `&_pragma=secure_delete(ON)` from the DSN. Observed to fail
// with "secure_delete is 0". Restored from a file backup; git diff --stat empty.
func TestTheDatabaseIsOpenedWithSecureDeleteOn(t *testing.T) {
	d := keyDB(t, filepath.Join(t.TempDir(), "polyemesis.db"), WithSecretBox(testBox(t)))
	var mode int
	if err := d.sql.QueryRow(`PRAGMA secure_delete`).Scan(&mode); err != nil {
		t.Fatalf("read PRAGMA secure_delete: %v", err)
	}
	if mode == 0 {
		t.Error("secure_delete is 0, so SQLite unlinks a shortened cell without " +
			"zeroing it and the plaintext stream key stays legible in the freed " +
			"page. The driver's default IS 0 -- CPython's bundled SQLite compiles " +
			"it to 1, so the same check written in Python reports a false pass.")
	}
}
