package db

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
)

func TestStorageTargetSecretIsSealedAndRoundTrips(t *testing.T) {
	box := testBox(t)
	d := keyDB(t, filepath.Join(t.TempDir(), "polyemesis.db"))
	const plain = "fake-secret-value-not-real"
	id, err := d.CreateStorageTarget(box, StorageTarget{Name: "m", Provider: StorageProviderMinIO, Endpoint: "http://h", Bucket: "b",
		AccessKeyID: "fake-access-key", SecretAccessKey: plain, Mode: OffloadMirror, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	var enc []byte
	if err := d.sql.QueryRow(`SELECT secret_enc FROM storage_targets WHERE id=?`, id).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if len(enc) == 0 || bytes.Contains(enc, []byte(plain)) {
		t.Fatalf("secret not sealed at rest: %q", enc)
	}
	got, err := d.GetStorageTarget(box, id)
	if err != nil || got.SecretAccessKey != plain || !got.HasSecret {
		t.Fatalf("%+v %v", got, err)
	}
	// An update with no secret keeps the stored one.
	got.SecretAccessKey = ""
	got.Bucket = "b2"
	if err := d.UpdateStorageTarget(box, got); err != nil {
		t.Fatal(err)
	}
	again, _ := d.GetStorageTarget(box, id)
	if again.SecretAccessKey != plain || again.Bucket != "b2" {
		t.Fatalf("%+v", again)
	}
	if _, err := d.GetStorageTarget(box, id+99); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestStorageTargetWithoutABoxCannotStoreASecret(t *testing.T) {
	d := keyDB(t, filepath.Join(t.TempDir(), "polyemesis.db"))
	if _, err := d.CreateStorageTarget(nil, StorageTarget{Name: "m", Endpoint: "http://h", Bucket: "b", SecretAccessKey: "x"}); err == nil {
		t.Fatal("stored a secret with no box to seal it")
	}
}

func TestATargetHoldingTheOnlyCopyRefusesDeletion(t *testing.T) {
	box := testBox(t)
	d := keyDB(t, filepath.Join(t.TempDir(), "polyemesis.db"))
	a, _ := d.CreateStorageTarget(box, StorageTarget{Name: "a", Endpoint: "http://h", Bucket: "b", Mode: OffloadMove, Enabled: true})
	b, _ := d.CreateStorageTarget(box, StorageTarget{Name: "b", Endpoint: "http://h", Bucket: "c", Mode: OffloadMirror, Enabled: true})
	if err := d.PutOffloadItem(OffloadItem{TargetID: a, RelPath: "rec.mkv", Kind: OffloadKindRecording, Key: "k", State: OffloadVerified, Evicted: true}); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteStorageTarget(a, nil); !errors.Is(err, ErrStorageTargetHoldsOnlyCopies) || !errors.Is(err, ErrStateConflict) {
		t.Fatalf("delete of the only copy: %v", err)
	}
	// Once another target holds a verified copy, the first may go.
	_ = d.PutOffloadItem(OffloadItem{TargetID: b, RelPath: "rec.mkv", Kind: OffloadKindRecording, Key: "k", State: OffloadVerified, Evicted: true})
	if err := d.DeleteStorageTarget(a, nil); err != nil {
		t.Fatal(err)
	}
	if ev, _ := d.EvictedRecordings(); !ev["rec.mkv"] {
		t.Fatal("evicted state lost with the other target")
	}
	if err := d.DeleteStorageTarget(a, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

// Review #8: the offload schema is stamped past 1, the version every earlier
// release accepts, so a rollback to one of them refuses this database instead
// of scanning it and dropping the rows of recordings moved to a bucket.
func TestTheOffloadSchemaIsStampedSoAnOlderBinaryRefusesIt(t *testing.T) {
	d := keyDB(t, filepath.Join(t.TempDir(), "polyemesis.db"))
	var v int
	if err := d.sql.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v < 2 {
		t.Fatalf("user_version %d: a 0.9.x binary (which accepts 1) would open this database and drop evicted rows", v)
	}
}
