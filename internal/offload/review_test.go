package offload

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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

// partRebinder moves the target after the first part lands.
type partRebinder struct {
	*memStore
	after func()
	once  sync.Once
}

func (p *partRebinder) UploadPart(ctx context.Context, key, id string, n int, body io.ReadSeeker, size int64, sh, mb string) (string, error) {
	etag, err := p.memStore.UploadPart(ctx, key, id, n, body, size, sh, mb)
	p.once.Do(p.after)
	return etag, err
}

// Codex #1: a multipart upload in flight when the target is moved records no
// progress afterwards -- the rebind dropped it, and the old bucket's upload
// ID must never be left for the new address to resume -- and the upload at
// the old address is abandoned there.
func TestAMoveMidMultipartLeavesNoProgressForTheNewAddress(t *testing.T) {
	f := newFixture(t)
	f.svc.mpThreshold, f.svc.partSize, f.svc.partBackoff = 100, 40, time.Millisecond
	id := f.target()
	m := f.mems[id]
	pr := &partRebinder{memStore: m}
	pr.after = func() {
		cur, _ := f.store.GetStorageTarget(f.svc.box, id)
		cur.Bucket, cur.SecretAccessKey = "new-bucket", ""
		if err := f.store.RebindStorageTarget(f.svc.box, cur); err != nil {
			t.Errorf("rebind: %v", err)
		}
	}
	f.svc.newClient = func(db.StorageTarget) (Client, error) { return pr, nil }
	name := f.segment(5*time.Hour, bigBody(130), true)
	f.segment(time.Minute, "newest", true)
	f.svc.Pass(context.Background())
	if rows, _ := f.store.ListOffloadMultipart(id); len(rows) != 0 {
		t.Fatalf("progress from the old bucket recreated after the move: %+v", rows)
	}
	if len(m.aborted) != 1 {
		t.Fatalf("the old address's upload was not abandoned: %v", m.aborted)
	}
	if it, _ := f.item(id, name); it.State == db.OffloadVerified {
		t.Fatalf("verified for the new address: %+v", it)
	}
}

// noEvidence answers HEAD with the right size, an opaque ETag and no SHA-256
// metadata: nothing that says the object is the bytes that were sent.
type noEvidence struct{ *memStore }

func (n noEvidence) Head(ctx context.Context, key string) (objstore.ObjectInfo, error) {
	info, err := n.memStore.Head(ctx, key)
	info.ETag, info.OpaqueETag = "opaque-etag", true
	delete(info.Meta, "sha256")
	return info, err
}

// Codex #2: an object with no checksum to compare is not verified on its
// length alone; verify-by-download is what can still verify it.
func TestAnObjectWithNoChecksumIsNotVerifiedOnLengthAlone(t *testing.T) {
	f := newFixture(t)
	id := f.target()
	f.mems[id].corrupt = true // wrong bytes, right length
	f.svc.newClient = func(db.StorageTarget) (Client, error) { return noEvidence{f.mems[id]}, nil }
	name := f.segment(5*time.Hour, "footage", true)
	f.segment(time.Minute, "newest", true)
	f.svc.Pass(context.Background())
	if it, _ := f.item(id, name); it.State == db.OffloadVerified {
		t.Fatalf("verified with no evidence: %+v", it)
	}
	// With the read-back, good bytes do verify.
	f2 := newFixture(t)
	id2, _ := f2.store.CreateStorageTarget(f2.svc.box, db.StorageTarget{Name: "dl", Provider: "s3", Endpoint: "http://h.test",
		Bucket: "b", AccessKeyID: fakeKeyID, SecretAccessKey: fakeSecret, Enabled: true, VerifyDownload: true})
	f2.mems[id2] = newMem()
	f2.svc.newClient = func(db.StorageTarget) (Client, error) { return noEvidence{f2.mems[id2]}, nil }
	n2 := f2.segment(5*time.Hour, "footage", true)
	f2.segment(time.Minute, "newest", true)
	f2.svc.Pass(context.Background())
	if it, _ := f2.item(id2, n2); it.State != db.OffloadVerified {
		t.Fatalf("read-back of good bytes not verified: %+v", it)
	}
}

// refusingRebinder moves the target and then refuses the upload, the way a
// failure from the OLD address arrives after an operator's move.
type refusingRebinder struct {
	*memStore
	move func()
}

func (r refusingRebinder) Put(context.Context, string, io.ReadSeeker, int64, string, string, map[string]string) (string, error) {
	r.move()
	return "", &objstore.Error{Op: "PutObject", StatusCode: 403, Code: "AccessDenied"}
}

// Codex #5: a permanent failure from an address the target has moved away
// from is not a stuck upload: its state write is refused, and no alert,
// webhook or event is raised for it.
func TestAFailureFromAnOldAddressRaisesNoStuckAlert(t *testing.T) {
	f := newFixture(t)
	id := f.target()
	var raised []StuckEvent
	f.svc.SetStuckHook(func(ev StuckEvent) { raised = append(raised, ev) })
	var once sync.Once
	f.svc.newClient = func(db.StorageTarget) (Client, error) {
		return refusingRebinder{memStore: f.mems[id], move: func() {
			once.Do(func() {
				cur, _ := f.store.GetStorageTarget(f.svc.box, id)
				cur.Bucket, cur.SecretAccessKey = "new-bucket", ""
				_ = f.store.RebindStorageTarget(f.svc.box, cur)
			})
		}}, nil
	}
	name := f.segment(5*time.Hour, "footage", true)
	f.segment(time.Minute, "newest", true)
	f.svc.Pass(context.Background())
	if len(raised) != 0 {
		t.Fatalf("stuck raised for a failure at the old address: %+v", raised)
	}
	if it, _ := f.item(id, name); it.State == db.OffloadStuck {
		t.Fatalf("row marked stuck at the new address: %+v", it)
	}
}

// recordingPut remembers the digests the probe sent with its object.
type recordingPut struct {
	*memStore
	sha, md5b64 string
	body        []byte
}

func (r *recordingPut) Put(ctx context.Context, key string, body io.ReadSeeker, size int64, sh, mb string, meta map[string]string) (string, error) {
	b, _ := io.ReadAll(body)
	r.body, r.sha, r.md5b64 = b, sh, mb
	_, _ = body.Seek(0, io.SeekStart)
	return r.memStore.Put(ctx, key, body, size, sh, mb, meta)
}

// Guard for sonar-project.properties e14: the probe's MD5 is exactly the
// Content-MD5 of the bytes it puts (the S3 integrity header), and the
// SHA-256 beside it is the payload hash the request is signed over.
func TestTheProbesMD5IsOnlyTheContentMD5Header(t *testing.T) {
	rp := &recordingPut{memStore: newMem()}
	res := Probe(context.Background(), db.StorageTarget{Name: "p"}, func(db.StorageTarget) (Client, error) { return rp, nil })
	if !res.OK {
		t.Fatalf("%+v", res)
	}
	m := md5.Sum(rp.body)
	sh := sha256.Sum256(rp.body)
	if rp.md5b64 != base64.StdEncoding.EncodeToString(m[:]) || rp.sha != hex.EncodeToString(sh[:]) {
		t.Fatalf("probe digests are not the Content-MD5 and payload SHA-256 of what it put: md5=%q sha=%q", rp.md5b64, rp.sha)
	}
}
