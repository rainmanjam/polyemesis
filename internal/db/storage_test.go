package db

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestStorageTargetSecretIsSealedAndRoundTrips(t *testing.T) {
	box := testBox(t)
	d := keyDB(t, filepath.Join(t.TempDir(), "polyemesis.db"))
	const plain = "fake-secret-value-not-real"
	id, err := d.CreateStorageTarget(box, StorageTarget{Name: "m", Provider: StorageProviderMinIO, Endpoint: "http://h", Bucket: "b",
		AccessKeyID: "fake-access-key", SecretAccessKey: plain, Enabled: true})
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

func TestDeletingATargetDropsItsRowsAndItsMultipartProgress(t *testing.T) {
	box := testBox(t)
	d := keyDB(t, filepath.Join(t.TempDir(), "polyemesis.db"))
	a, _ := d.CreateStorageTarget(box, StorageTarget{Name: "a", Endpoint: "http://h", Bucket: "b", Enabled: true})
	_ = d.PutOffloadItem(OffloadItem{TargetID: a, RelPath: "rec.mkv", Kind: OffloadKindRecording, Key: "k", State: OffloadVerified})
	_ = d.PutOffloadMultipart(OffloadMultipart{TargetID: a, RelPath: "big.mkv", Key: "k2", UploadID: "u", PartSize: 1, Bytes: 2, SHA256: "x"})
	if err := d.DeleteStorageTarget(a); err != nil {
		t.Fatal(err)
	}
	if its, _ := d.ListOffloadItems(); len(its) != 0 {
		t.Fatalf("rows left: %+v", its)
	}
	if mps, _ := d.ListOffloadMultipart(0); len(mps) != 0 {
		t.Fatalf("multipart left: %+v", mps)
	}
	if err := d.DeleteStorageTarget(a); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

// A write an upload makes lands only while the target's binding generation
// is the one its pass started with: a move resets the rows, and the stale
// upload's "verified" for the old bucket is refused, not written over them.
func TestAnUploadResultFromBeforeAMoveIsRefused(t *testing.T) {
	box := testBox(t)
	d := keyDB(t, filepath.Join(t.TempDir(), "polyemesis.db"))
	id, _ := d.CreateStorageTarget(box, StorageTarget{Name: "a", Endpoint: "http://h", Bucket: "old", Enabled: true,
		AccessKeyID: "fake-access-key", SecretAccessKey: "fake-secret-value-not-real"})
	tg, _ := d.GetStorageTarget(box, id)
	gen := tg.BindingGen
	it := OffloadItem{TargetID: id, RelPath: "rec.mkv", Kind: OffloadKindRecording, Key: "k", State: OffloadUploading}
	if err := d.PutOffloadItemIfBinding(it, gen); err != nil {
		t.Fatalf("same generation: %v", err)
	}
	tg.Bucket, tg.SecretAccessKey = "new", ""
	if err := d.RebindStorageTarget(box, tg); err != nil {
		t.Fatal(err)
	}
	it.State, it.VerifiedAt = OffloadVerified, time.Now()
	if err := d.PutOffloadItemIfBinding(it, gen); !errors.Is(err, ErrBindingChanged) {
		t.Fatalf("stale write: %v", err)
	}
	rows, _ := d.OffloadItemsFor("rec.mkv")
	if len(rows) != 1 || rows[0].State != OffloadPending {
		t.Fatalf("the move's reset was overwritten: %+v", rows)
	}
	// Deleted target: nothing written, no orphan row.
	if err := d.DeleteStorageTarget(id); err != nil {
		t.Fatal(err)
	}
	if err := d.PutOffloadItemIfBinding(it, gen+1); !errors.Is(err, ErrBindingChanged) {
		t.Fatalf("write for a deleted target: %v", err)
	}
	if its, _ := d.ListOffloadItems(); len(its) != 0 {
		t.Fatalf("orphan row written: %+v", its)
	}
}
