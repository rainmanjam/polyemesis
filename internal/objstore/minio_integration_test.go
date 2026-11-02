//go:build s3integration

// The live half of this package's tests: real SigV4 against a real MinIO.
// Built only with -tags s3integration (scripts/acceptance-s3.sh starts the
// container and sets the environment); without the endpoint it FAILS rather
// than skips, because a suite someone asked to run that quietly ran nothing
// is the defect this repository keeps naming.
package objstore

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func liveConfig(t *testing.T) Config {
	t.Helper()
	ep := os.Getenv("POLYEMESIS_S3_ENDPOINT")
	if ep == "" {
		t.Fatal("POLYEMESIS_S3_ENDPOINT is unset; run scripts/acceptance-s3.sh, which starts MinIO and sets it")
	}
	return Config{Endpoint: ep, Bucket: "objstore-live", Region: "us-east-1", PathStyle: true,
		AccessKeyID: os.Getenv("POLYEMESIS_S3_ACCESS_KEY"), SecretAccessKey: os.Getenv("POLYEMESIS_S3_SECRET_KEY")}
}

func liveClient(t *testing.T) *Client {
	t.Helper()
	c, err := New(liveConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.MakeBucket(context.Background()); err != nil {
		t.Fatalf("make bucket: %v", err)
	}
	return c
}

func TestLiveRoundTripWithAKeyThatNeedsEscaping(t *testing.T) {
	c := liveClient(t)
	ctx := context.Background()
	body := []byte("live segment bytes\n")
	sh, mb := digests(body)
	key := "rec/2026/11/01/rec s3+a=b~c.mkv"
	etag, err := c.Put(ctx, key, bytes.NewReader(body), int64(len(body)), sh, mb, map[string]string{"sha256": sh})
	if err != nil {
		t.Fatal(err)
	}
	m := md5.Sum(body)
	if etag != hex.EncodeToString(m[:]) {
		t.Fatalf("etag %q is not the body's MD5", etag)
	}
	info, err := c.Head(ctx, key)
	if err != nil || info.Size != int64(len(body)) || info.Meta["sha256"] != sh {
		t.Fatalf("head %+v %v", info, err)
	}
	rc, _, err := c.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, body) {
		t.Fatalf("got %q", got)
	}
	if err := c.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Head(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

// The integrity claim in the package comment, proven against the server: a
// body that does not match the digests signed for it is refused, not stored.
func TestLiveServerRefusesABodyThatIsNotTheSignedDigest(t *testing.T) {
	c := liveClient(t)
	ctx := context.Background()
	body := []byte("the bytes actually sent")
	other := []byte("the bytes the digest is for")
	shOther := sha256.Sum256(other)
	mOther := md5.Sum(other)
	_, mbBody := digests(body)
	shBody, _ := digests(body)

	_, err := c.Put(ctx, "bad-sha", bytes.NewReader(body), int64(len(body)), hex.EncodeToString(shOther[:]), mbBody, nil)
	if err == nil {
		t.Fatal("server stored a body whose SHA-256 differs from the signed payload hash")
	}
	_, err = c.Put(ctx, "bad-md5", bytes.NewReader(body), int64(len(body)), shBody, base64.StdEncoding.EncodeToString(mOther[:]), nil)
	var se *Error
	if !errors.As(err, &se) || se.Code != "BadDigest" {
		t.Fatalf("Content-MD5 mismatch: %v", err)
	}
	for _, k := range []string{"bad-sha", "bad-md5"} {
		if _, err := c.Head(ctx, k); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s exists after a refused put: %v", k, err)
		}
	}
}

func TestLiveWrongSecretIsRefusedAndNeverEchoed(t *testing.T) {
	cfg := liveConfig(t)
	cfg.SecretAccessKey = "wrong-secret-for-the-test"
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	err = c.Delete(context.Background(), "anything")
	var se *Error
	if !errors.As(err, &se) || se.Code != "SignatureDoesNotMatch" {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), cfg.SecretAccessKey) || strings.Contains(err.Error(), liveConfig(t).SecretAccessKey) {
		t.Fatal("error echoes a secret")
	}
}

func TestLiveMultipartAndRangedGet(t *testing.T) {
	c := liveClient(t)
	ctx := context.Background()
	part := bytes.Repeat([]byte("0123456789abcdef"), (5<<20)/16) // 5 MiB, MinIO's minimum part
	tail := []byte("the short last part")
	key := "mp/large object.mkv"
	id, err := c.CreateMultipart(ctx, key, map[string]string{"sha256": "recorded-by-the-caller"})
	if err != nil {
		t.Fatal(err)
	}
	var parts []Part
	for i, b := range [][]byte{part, tail} {
		sh, mb := digests(b)
		etag, err := c.UploadPart(ctx, key, id, i+1, bytes.NewReader(b), int64(len(b)), sh, mb)
		if err != nil {
			t.Fatalf("part %d: %v", i+1, err)
		}
		parts = append(parts, Part{Number: i + 1, ETag: etag, MD5: mb, Size: int64(len(b))})
	}
	etag, err := c.CompleteMultipart(ctx, key, id, parts)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := CompositeETag(parts)
	if etag != want {
		t.Fatalf("composite etag %q, predicted %q", etag, want)
	}
	info, err := c.Head(ctx, key)
	if err != nil || info.ETag != want || info.Size != int64(len(part)+len(tail)) || info.Meta["sha256"] != "recorded-by-the-caller" {
		t.Fatalf("head %+v %v", info, err)
	}
	off := int64(len(part)) + 4
	rc, rinfo, partial, err := c.GetFrom(ctx, key, off)
	if err != nil || !partial || rinfo.Size != info.Size {
		t.Fatalf("ranged get partial=%v %+v %v", partial, rinfo, err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != string(tail[4:]) {
		t.Fatalf("ranged body %q", got)
	}
	// Abort of an upload, and of one that no longer exists, both succeed.
	id2, _ := c.CreateMultipart(ctx, "mp/abandoned", nil)
	if err := c.AbortMultipart(ctx, "mp/abandoned", id2); err != nil {
		t.Fatal(err)
	}
	if err := c.AbortMultipart(ctx, "mp/abandoned", id2); err != nil {
		t.Fatalf("second abort: %v", err)
	}
}
