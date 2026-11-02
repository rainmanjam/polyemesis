//go:build s3integration

// Offload end to end against a real MinIO: upload, verify, evict, fetch back,
// and the Test button's three steps. Built only with -tags s3integration; see
// scripts/acceptance-s3.sh. Fails, never skips, when the endpoint is missing.
package offload

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/objstore"
)

func liveTarget(t *testing.T, f *fixture, bucket string, mode string) db.StorageTarget {
	t.Helper()
	ep := os.Getenv("POLYEMESIS_S3_ENDPOINT")
	if ep == "" {
		t.Fatal("POLYEMESIS_S3_ENDPOINT is unset; run scripts/acceptance-s3.sh, which starts MinIO and sets it")
	}
	tg := db.StorageTarget{Name: "minio", Provider: db.StorageProviderMinIO, Endpoint: ep, Bucket: bucket,
		Region: "us-east-1", PathStyle: true, AccessKeyID: os.Getenv("POLYEMESIS_S3_ACCESS_KEY"),
		SecretAccessKey: os.Getenv("POLYEMESIS_S3_SECRET_KEY"), Mode: mode, Enabled: true,
		AllowPrivateTarget: true, VerifyDownload: true, PrefixTemplate: "live/{kind}/{yyyy}"}
	c, err := objstore.New(objstore.Config{Endpoint: ep, Bucket: bucket, Region: "us-east-1", PathStyle: true,
		AccessKeyID: tg.AccessKeyID, SecretAccessKey: tg.SecretAccessKey})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.MakeBucket(context.Background()); err != nil {
		t.Fatal(err)
	}
	id, err := f.store.CreateStorageTarget(f.svc.box, tg)
	if err != nil {
		t.Fatal(err)
	}
	tg, err = f.store.GetStorageTarget(f.svc.box, id)
	if err != nil {
		t.Fatal(err)
	}
	return tg
}

func TestLiveMoveModeUploadsVerifiesEvictsAndFetchesBack(t *testing.T) {
	f := newFixture(t)
	f.svc.newClient = NewClient
	tg := liveTarget(t, f, "offload-live-move", db.OffloadMove)
	name := f.segment(5*time.Hour, "real footage bytes for the live suite", true)
	f.segment(time.Minute, "newest", true)

	f.svc.Pass(context.Background())
	it, ok := f.item(tg.ID, name)
	if !ok || it.State != db.OffloadVerified || !it.Evicted {
		t.Fatalf("item %+v", it)
	}
	if f.local(name) {
		t.Fatal("not evicted after a verified upload")
	}
	if err := f.svc.FetchNow(context.Background(), name); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(f.dir, name))
	if err != nil || string(b) != "real footage bytes for the live suite" {
		t.Fatalf("fetched %q %v", b, err)
	}
}

func TestLiveProbeRunsPutGetDelete(t *testing.T) {
	f := newFixture(t)
	tg := liveTarget(t, f, "offload-live-probe", db.OffloadMirror)
	res := Probe(context.Background(), tg, nil)
	if !res.OK || len(res.Steps) != 3 {
		t.Fatalf("%+v", res)
	}
	tg.Bucket = "no-such-bucket-here"
	res = Probe(context.Background(), tg, nil)
	if res.OK || res.FailedStep != "put" {
		t.Fatalf("missing bucket: %+v", res)
	}
}

// partFailer fails one part once, the way a dropped connection does.
type partFailer struct {
	Client
	fail int
	done bool
}

func (p *partFailer) UploadPart(ctx context.Context, key, id string, n int, body io.ReadSeeker, size int64, sh, mb string) (string, error) {
	if n == p.fail && !p.done {
		p.done = true
		return "", &objstore.Error{Op: "UploadPart", StatusCode: 503}
	}
	return p.Client.UploadPart(ctx, key, id, n, body, size, sh, mb)
}

func TestLiveMultipartResumesAcrossARestartAndFetchesBack(t *testing.T) {
	f := newFixture(t)
	tg := liveTarget(t, f, "offload-live-multipart", db.OffloadMove)
	real, err := NewClient(tg)
	if err != nil {
		t.Fatal(err)
	}
	failing := &partFailer{Client: real, fail: 2}
	f.svc.newClient = func(db.StorageTarget) (Client, error) { return failing, nil }
	f.svc.mpThreshold, f.svc.partSize, f.svc.partTries = 5<<20, 5<<20, 1
	body := strings.Repeat("polyemesis multipart live body ", (11<<20)/31)
	name := f.segment(5*time.Hour, body, true)
	f.segment(time.Minute, "newest", true)
	f.svc.Pass(context.Background())
	if rows, _ := f.store.ListOffloadMultipart(tg.ID); len(rows) != 1 || len(rows[0].Parts) != 1 {
		t.Fatalf("progress after part 2 failed: %+v", rows)
	}
	next := New(slog.New(slog.NewTextHandler(io.Discard, nil)), f.store, f.svc.box, f.rec, nil,
		WithClock(func() time.Time { return f.now.Add(2 * time.Hour) }), WithMultipart(5<<20, 5<<20))
	next.Recover(context.Background())
	next.Pass(context.Background())
	it, _ := f.item(tg.ID, name)
	if it.State != db.OffloadVerified || !it.Evicted || f.local(name) {
		t.Fatalf("after resume %+v", it)
	}
	if err := next.FetchNow(context.Background(), name); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(f.dir, name))
	if err != nil || string(b) != body {
		t.Fatalf("fetched %d bytes, %v", len(b), err)
	}
}
