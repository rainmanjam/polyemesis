package offload

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// wireTarget points a target at the in-process S3 wire server through the
// production client: the SSRF-guarded transport, opted in to loopback.
func wireTarget(t *testing.T, f *fixture, url string) db.StorageTarget {
	t.Helper()
	id, err := f.store.CreateStorageTarget(f.svc.box, db.StorageTarget{Name: "wire", Provider: "minio", Endpoint: url,
		Bucket: "media", PathStyle: true, AccessKeyID: fakeKeyID, SecretAccessKey: fakeSecret, Enabled: true,
		AllowPrivateTarget: true, OffloadClips: true})
	if err != nil {
		t.Fatal(err)
	}
	tg, _ := f.store.GetStorageTarget(f.svc.box, id)
	return tg
}

// Mirror offload end to end over HTTP: a small file in one PUT, a large one
// in parts, both verified against what the service reports, the local files
// untouched -- through NewClient, the client production builds.
func TestMirrorOffloadOverTheWire(t *testing.T) {
	s3, srv := newFakeS3(t)
	f := newFixture(t)
	f.svc.newClient = NewClient
	f.svc.mpThreshold, f.svc.partSize = 100, 40
	tg := wireTarget(t, f, srv.URL)
	small := f.segment(6*time.Hour, "a small segment", true)
	big := f.segment(5*time.Hour, bigBody(130), true)
	f.segment(time.Minute, "newest", true)
	f.svc.Pass(context.Background())
	for _, name := range []string{small, big} {
		it, ok := f.item(tg.ID, name)
		if !ok || it.State != db.OffloadVerified {
			t.Fatalf("%s: %+v", name, it)
		}
		if !f.local(name) {
			t.Fatalf("%s: local copy gone", name)
		}
		if got := string(s3.objs["media/"+it.Key]); (name == small && got != "a small segment") || (name == big && got != bigBody(130)) {
			t.Fatalf("%s stored %d bytes", name, len(got))
		}
	}
	if it, _ := f.item(tg.ID, big); !strings.HasSuffix(s3.etags["media/"+it.Key], "-4") {
		t.Fatalf("large file did not go up in parts: etag %q", s3.etags["media/"+it.Key])
	}
	if res := Probe(context.Background(), tg, nil); !res.OK {
		t.Fatalf("probe over the wire: %+v", res)
	}
}

// Abandoned multipart uploads are aborted over the wire: by AbortTarget (a
// deleted target) and by AbortUploads (a moved one's old address).
func TestAbandonedUploadsAreAbortedOverTheWire(t *testing.T) {
	s3, srv := newFakeS3(t)
	f := newFixture(t)
	f.svc.newClient = NewClient
	tg := wireTarget(t, f, srv.URL)
	c, err := NewClient(tg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a, _ := c.CreateMultipart(ctx, "k/a", nil)
	b, _ := c.CreateMultipart(ctx, "k/b", nil)
	_ = f.store.PutOffloadMultipart(db.OffloadMultipart{TargetID: tg.ID, RelPath: "a.mkv", Key: "k/a", UploadID: a, PartSize: 1, Bytes: 1, SHA256: "x"})
	f.svc.AbortTarget(ctx, tg)
	if _, open := s3.uploads[a]; open {
		t.Fatal("AbortTarget left the upload open")
	}
	f.svc.AbortUploads(ctx, tg, []db.OffloadMultipart{{Key: "k/b", UploadID: b, RelPath: "b.mkv"}})
	if _, open := s3.uploads[b]; open {
		t.Fatal("AbortUploads left the upload open")
	}
	f.svc.AbortUploads(ctx, tg, nil) // nothing to do is not an error
}

// Run recovers interrupted uploads before its first pass, passes, and stops
// with its context.
func TestRunRecoversThenPassesUntilCancelled(t *testing.T) {
	f := newFixture(t)
	id := f.target()
	name := f.segment(5*time.Hour, "footage", true)
	f.segment(time.Minute, "newest", true)
	_ = f.store.PutOffloadItem(db.OffloadItem{TargetID: id, RelPath: name, Kind: db.OffloadKindRecording,
		Key: "polyemesis/recordings/x/" + name, State: db.OffloadUploading, Attempts: 2})
	var stuck []StuckEvent
	svc := New(f.svc.log, f.store, f.svc.box, f.rec, nil, WithClock(func() time.Time { return f.now }),
		WithClientFactory(func(db.StorageTarget) (Client, error) { return f.mems[id], nil }),
		WithStuckHook(func(ev StuckEvent) { stuck = append(stuck, ev) }))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { svc.Run(ctx); close(done) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if it, _ := f.item(id, name); it.State == db.OffloadVerified {
			break
		}
		if time.Now().After(deadline) {
			it, _ := f.item(id, name)
			t.Fatalf("Run never uploaded the interrupted item: %+v", it)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop with its context")
	}
	if len(stuck) != 0 {
		t.Fatalf("stuck raised: %+v", stuck)
	}
}
