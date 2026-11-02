//go:build s3integration

// Offload end to end against a real S3-compatible server: mirror upload and
// verify, a multipart upload resumed across a restart, and the Test button's
// three steps. Built only with -tags s3integration and configured by the same
// environment as internal/objstore's live tests (endpoint, keys, bucket,
// region, path style, create-bucket); scripts/acceptance-s3.sh runs it against
// RustFS. Fails, never skips, when the endpoint is missing.
package offload

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/objstore"
)

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// liveTarget saves a target for the configured server. bucket overrides the
// configured bucket (the Test case's missing-bucket check); "" uses it. Each
// test's objects live under a prefix named for the test.
func liveTarget(t *testing.T, f *fixture, bucket string) db.StorageTarget {
	t.Helper()
	ep, ak, sk := os.Getenv("POLYEMESIS_S3_ENDPOINT"), os.Getenv("POLYEMESIS_S3_ACCESS_KEY"), os.Getenv("POLYEMESIS_S3_SECRET_KEY")
	if ep == "" || ak == "" || sk == "" {
		t.Fatal("POLYEMESIS_S3_ENDPOINT, _ACCESS_KEY and _SECRET_KEY must be set; scripts/acceptance-s3.sh starts RustFS and sets them")
	}
	if bucket == "" {
		bucket = envOr("POLYEMESIS_S3_BUCKET", "polyemesis-s3-suite")
	}
	region := envOr("POLYEMESIS_S3_REGION", "us-east-1")
	pathStyle := os.Getenv("POLYEMESIS_S3_PATH_STYLE") != "0"
	tg := db.StorageTarget{Name: "live", Provider: db.StorageProviderS3, Endpoint: ep, Bucket: bucket,
		Region: region, PathStyle: pathStyle, AccessKeyID: ak, SecretAccessKey: sk, Enabled: true,
		AllowPrivateTarget: true, VerifyDownload: true, PrefixTemplate: "offload/" + t.Name() + "/{kind}/{yyyy}"}
	if os.Getenv("POLYEMESIS_S3_CREATE_BUCKET") == "1" {
		c, err := objstore.New(objstore.Config{Endpoint: ep, Bucket: bucket, Region: region, PathStyle: pathStyle,
			AccessKeyID: ak, SecretAccessKey: sk})
		if err != nil {
			t.Fatal(err)
		}
		if err := c.MakeBucket(context.Background()); err != nil {
			t.Fatal(err)
		}
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

func TestLiveMirrorUploadsVerifiesAndKeepsTheLocalFile(t *testing.T) {
	f := newFixture(t)
	f.svc.newClient = NewClient
	tg := liveTarget(t, f, "")
	body := "real footage bytes for the live suite"
	name := f.segment(5*time.Hour, body, true)
	f.segment(time.Minute, "newest", true)

	f.svc.Pass(context.Background())
	it, ok := f.item(tg.ID, name)
	if !ok || it.State != db.OffloadVerified {
		t.Fatalf("item %+v", it)
	}
	if !f.local(name) {
		t.Fatal("mirror mode removed the local file")
	}
	c, err := NewClient(tg)
	if err != nil {
		t.Fatal(err)
	}
	rc, _, err := c.Get(context.Background(), it.Key)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != body {
		t.Fatalf("stored %q", got)
	}
}

func TestLiveProbeRunsPutGetDelete(t *testing.T) {
	f := newFixture(t)
	tg := liveTarget(t, f, "")
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

func TestLiveMultipartResumesAcrossARestartAndVerifies(t *testing.T) {
	f := newFixture(t)
	tg := liveTarget(t, f, "")
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
	if it.State != db.OffloadVerified || !f.local(name) {
		t.Fatalf("after resume %+v local=%v", it, f.local(name))
	}
	rc, _, err := real.Get(context.Background(), it.Key)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != body {
		t.Fatalf("stored %d bytes, want %d", len(got), len(body))
	}
}
