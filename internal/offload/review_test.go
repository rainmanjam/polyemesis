package offload

import (
	"context"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/objstore"
)

// #13: Retry now pressed after a pass has read the items is not overwritten
// by that pass's stale copy of the attempt count, and its kick is not dropped.
func TestRetryNowDuringAPassIsNotOverwrittenByIt(t *testing.T) {
	f := newFixture(t)
	id, _ := f.store.CreateStorageTarget(f.svc.box, db.StorageTarget{Name: "t", Provider: "s3", Endpoint: "http://h.test",
		Bucket: "b", AccessKeyID: fakeKeyID, SecretAccessKey: fakeSecret, Enabled: true, MaxAttempts: 7})
	m := newMem()
	m.putErr = &objstore.Error{Op: "PutObject", StatusCode: 503}
	name := f.segment(5*time.Hour, "footage", true)
	f.segment(time.Minute, "newest", true)
	_ = f.store.PutOffloadItem(db.OffloadItem{TargetID: id, RelPath: name, Kind: db.OffloadKindRecording,
		Key: "polyemesis/recordings/x/" + name, State: db.OffloadFailed, Attempts: 6})
	var once sync.Once
	// The factory runs after the pass has read the items: the operator's
	// Retry now lands exactly in that window.
	f.svc.newClient = func(db.StorageTarget) (Client, error) {
		once.Do(func() {
			if n, err := f.svc.Retry(id, ""); err != nil || n != 1 {
				t.Errorf("retry %d %v", n, err)
			}
		})
		return m, nil
	}
	f.svc.Pass(context.Background())
	it, _ := f.item(id, name)
	if it.State == db.OffloadStuck || it.Attempts != 1 {
		t.Fatalf("after a retry mid-pass: %+v (want failed, attempts 1)", it)
	}
	if m.puts == 0 {
		t.Fatal("the retried item was not attempted")
	}
}

// #17: an SSE-KMS or SSE-C object's ETag is not its MD5; an upload is not
// failed for that.
type kmsStore struct{ *memStore }

func (k kmsStore) Head(ctx context.Context, key string) (objstore.ObjectInfo, error) {
	info, err := k.memStore.Head(ctx, key)
	info.ETag = strings.Repeat("9f", 16)
	info.OpaqueETag = true
	return info, err
}

func TestAnSSEKMSObjectIsVerifiedWithoutComparingItsETag(t *testing.T) {
	f := newFixture(t)
	id := f.target()
	f.svc.newClient = func(db.StorageTarget) (Client, error) { return kmsStore{f.mems[id]}, nil }
	name := f.segment(5*time.Hour, "footage", true)
	f.segment(time.Minute, "newest", true)
	f.svc.Pass(context.Background())
	if it, _ := f.item(id, name); it.State != db.OffloadVerified {
		t.Fatalf("%+v", it)
	}
	// And the same opaque ETag WITHOUT the encryption header is a mismatch.
	f2 := newFixture(t)
	id2 := f2.target()
	f2.mems[id2].lieETag = true
	n2 := f2.segment(5*time.Hour, "footage", true)
	f2.segment(time.Minute, "newest", true)
	f2.svc.Pass(context.Background())
	if it, _ := f2.item(id2, n2); it.State == db.OffloadVerified {
		t.Fatal("an unexplained wrong ETag verified")
	}
}

// #20: a long session's fetch runs a bounded number of transfers at once.
type countingGets struct {
	*memStore
	cur, max atomic.Int32
}

// rebindingClient moves the target to another bucket the moment its first
// upload lands, the way an operator's edit can arrive mid-pass.
type rebindingClient struct {
	*memStore
	onPut func()
}

func (c rebindingClient) Put(ctx context.Context, key string, body io.ReadSeeker, size int64, sh, mb string, meta map[string]string) (string, error) {
	e, err := c.memStore.Put(ctx, key, body, size, sh, mb, meta)
	if c.onPut != nil {
		c.onPut()
	}
	return e, err
}

// The target is moved while an upload to the OLD bucket is in flight. The
// move resets the row to pending for the new address; the upload's
// "verified" belongs to the old bucket and must not be written over it.
func TestAnUploadInFlightDuringAMoveDoesNotMarkTheNewAddressVerified(t *testing.T) {
	f := newFixture(t)
	id := f.target()
	name := f.segment(5*time.Hour, "footage", true)
	f.segment(time.Minute, "newest", true)
	old := f.mems[id]
	var once sync.Once
	f.svc.newClient = func(db.StorageTarget) (Client, error) {
		return rebindingClient{memStore: old, onPut: func() {
			once.Do(func() {
				cur, _ := f.store.GetStorageTarget(f.svc.box, id)
				cur.Bucket, cur.SecretAccessKey = "new-bucket", ""
				if err := f.store.RebindStorageTarget(f.svc.box, cur); err != nil {
					t.Errorf("rebind: %v", err)
				}
			})
		}}, nil
	}
	f.svc.Pass(context.Background())
	it, ok := f.item(id, name)
	if !ok || it.State == db.OffloadVerified {
		t.Fatalf("after a move mid-upload the row is %+v; it must stay pending for the new bucket", it)
	}
	// The next pass, at the new binding, uploads it to the new address.
	f.svc.newClient = func(db.StorageTarget) (Client, error) { return f.mems[id], nil }
	f.svc.Pass(context.Background())
	if it, _ := f.item(id, name); it.State != db.OffloadVerified {
		t.Fatalf("not uploaded at the new address: %+v", it)
	}
}
