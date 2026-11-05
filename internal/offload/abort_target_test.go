package offload

import (
	"context"
	"errors"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// #14 of the 2026-11-04 lingering-state audit. AbortTarget returned nothing,
// and the target delete then dropped every multipart row -- the only record of
// the upload ids -- whether or not the service had aborted them. What it could
// not abort is now returned, so the delete can name it.

// refusingAbort is a bucket that will not abort a multipart upload.
type refusingAbort struct{ *memStore }

func (refusingAbort) AbortMultipart(context.Context, string, string) error {
	return errors.New("AccessDenied")
}

func seedUpload(t *testing.T, f *fixture, id int64, rel, key string) string {
	t.Helper()
	upID, err := f.mems[id].CreateMultipart(context.Background(), key, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.PutOffloadMultipart(db.OffloadMultipart{TargetID: id, RelPath: rel, Key: key, UploadID: upID,
		PartSize: 40, Bytes: 100, SHA256: "x"}); err != nil {
		t.Fatal(err)
	}
	return upID
}

func TestAbortTargetReturnsWhatTheServiceRefused(t *testing.T) {
	f := newFixture(t)
	id := f.target()
	tg, err := f.store.GetStorageTarget(f.svc.box, id)
	if err != nil {
		t.Fatal(err)
	}
	upID := seedUpload(t, f, id, "a.mkv", "k/a.mkv")
	f.svc.newClient = func(db.StorageTarget) (Client, error) { return refusingAbort{f.mems[id]}, nil }

	left := f.svc.AbortTarget(context.Background(), tg)
	if len(left) != 1 || left[0].UploadID != upID || left[0].Key != "k/a.mkv" {
		t.Fatalf("AbortTarget returned %+v, want the refused upload %s on k/a.mkv", left, upID)
	}
}

func TestAbortTargetReturnsEverythingWhenTheTargetCannotBeReached(t *testing.T) {
	f := newFixture(t)
	id := f.target()
	tg, err := f.store.GetStorageTarget(f.svc.box, id)
	if err != nil {
		t.Fatal(err)
	}
	seedUpload(t, f, id, "a.mkv", "k/a.mkv")
	seedUpload(t, f, id, "b.mkv", "k/b.mkv")
	f.svc.newClient = func(db.StorageTarget) (Client, error) { return nil, errors.New("the keys were revoked") }

	if left := f.svc.AbortTarget(context.Background(), tg); len(left) != 2 {
		t.Fatalf("AbortTarget returned %d upload(s) with no client to abort them with, want both", len(left))
	}
}

func TestAbortTargetReturnsNothingWhenEveryAbortSucceeds(t *testing.T) {
	f := newFixture(t)
	id := f.target()
	tg, err := f.store.GetStorageTarget(f.svc.box, id)
	if err != nil {
		t.Fatal(err)
	}
	seedUpload(t, f, id, "a.mkv", "k/a.mkv")
	if left := f.svc.AbortTarget(context.Background(), tg); len(left) != 0 {
		t.Fatalf("a clean abort returned %+v", left)
	}
}
