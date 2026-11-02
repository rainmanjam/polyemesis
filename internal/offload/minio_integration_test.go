//go:build s3integration

// Offload end to end against a real MinIO: upload, verify, evict, fetch back,
// and the Test button's three steps. Built only with -tags s3integration; see
// scripts/acceptance-s3.sh. Fails, never skips, when the endpoint is missing.
package offload

import (
	"context"
	"os"
	"path/filepath"
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
