package objstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func wireClient(t *testing.T, url string) *Client {
	t.Helper()
	c, err := New(Config{Endpoint: url, Bucket: "media", PathStyle: true, AccessKeyID: fakeKeyID, SecretAccessKey: fakeSecret})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// The multipart verbs as a service sees them: create returns an upload id,
// parts carry partNumber and uploadId in the (signed) query, completion
// assembles them, and the composite ETag the client predicts is the one the
// service reports.
func TestMultipartOverTheWire(t *testing.T) {
	f, srv := newFakeS3(t)
	c := wireClient(t, srv.URL)
	ctx := context.Background()
	key := "rec/big file.mkv"
	id, err := c.CreateMultipart(ctx, key, map[string]string{"sha256": "abc"})
	if err != nil || id == "" {
		t.Fatalf("create: %q %v", id, err)
	}
	var parts []Part
	for i, b := range [][]byte{bytes.Repeat([]byte("a"), 50), []byte("tail")} {
		sh, mb := digests(b)
		etag, err := c.UploadPart(ctx, UploadPartInput{Key: key, UploadID: id, N: i + 1, Body: bytes.NewReader(b), Size: int64(len(b)), SHA256Hex: sh, MD5B64: mb})
		if err != nil {
			t.Fatalf("part %d: %v", i+1, err)
		}
		parts = append(parts, Part{Number: i + 1, ETag: etag, MD5: mb, Size: int64(len(b))})
	}
	got, err := c.CompleteMultipart(ctx, key, id, parts)
	if err != nil {
		t.Fatal(err)
	}
	want, err := CompositeETag(parts)
	if err != nil || got != want || !strings.HasSuffix(got, "-2") {
		t.Fatalf("composite %q, predicted %q (%v)", got, want, err)
	}
	info, err := c.Head(ctx, key)
	if err != nil || info.Size != 54 || info.ETag != want || info.Meta["sha256"] != "abc" {
		t.Fatalf("head %+v %v", info, err)
	}
	rc, _, err := c.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != strings.Repeat("a", 50)+"tail" {
		t.Fatalf("assembled %q", b)
	}
	sawQuery := false
	for _, s := range f.seen {
		sawQuery = sawQuery || strings.Contains(s, "partNumber=1") && strings.Contains(s, "uploadId="+id)
	}
	if !sawQuery {
		t.Fatalf("part request did not carry its query: %v", f.seen)
	}
}

// CompleteMultipartUpload can answer 200 with an <Error> body; that is a
// failure, not a success with an empty ETag.
func TestACompleteThatAnswersAnErrorBodyFails(t *testing.T) {
	f, srv := newFakeS3(t)
	f.completeError = true
	c := wireClient(t, srv.URL)
	ctx := context.Background()
	id, _ := c.CreateMultipart(ctx, "k", nil)
	sh, mb := digests([]byte("x"))
	etag, _ := c.UploadPart(ctx, UploadPartInput{Key: "k", UploadID: id, N: 1, Body: bytes.NewReader([]byte("x")), Size: 1, SHA256Hex: sh, MD5B64: mb})
	_, err := c.CompleteMultipart(ctx, "k", id, []Part{{Number: 1, ETag: etag, MD5: mb}})
	var se *Error
	if !errors.As(err, &se) || se.Code != "InternalError" {
		t.Fatalf("err %v", err)
	}
}

// Aborting an upload the service no longer knows is the goal reached, not an
// error; a part for such an upload is NoSuchUpload.
func TestAbortIsIdempotentAndAPartForAGoneUploadIsRefused(t *testing.T) {
	_, srv := newFakeS3(t)
	c := wireClient(t, srv.URL)
	ctx := context.Background()
	id, _ := c.CreateMultipart(ctx, "k", nil)
	if err := c.AbortMultipart(ctx, "k", id); err != nil {
		t.Fatal(err)
	}
	if err := c.AbortMultipart(ctx, "k", id); err != nil {
		t.Fatalf("second abort: %v", err)
	}
	sh, mb := digests([]byte("x"))
	_, err := c.UploadPart(ctx, UploadPartInput{Key: "k", UploadID: id, N: 1, Body: bytes.NewReader([]byte("x")), Size: 1, SHA256Hex: sh, MD5B64: mb})
	var se *Error
	if !errors.As(err, &se) || se.Code != "NoSuchUpload" {
		t.Fatalf("part for a gone upload: %v", err)
	}
	if _, err := c.CompleteMultipart(ctx, "k", id, nil); err == nil {
		t.Fatal("completed a gone upload")
	}
}

func TestCompositeETagRefusesAPartWithoutAnMD5(t *testing.T) {
	if _, err := CompositeETag([]Part{{Number: 1, MD5: "not base64!"}}); err == nil {
		t.Fatal("accepted a part with no usable MD5")
	}
}

// MakeBucket creates the bucket, and a bucket that already exists is fine.
func TestMakeBucketIsIdempotent(t *testing.T) {
	f, srv := newFakeS3(t)
	c := wireClient(t, srv.URL)
	for i := 0; i < 2; i++ {
		if err := c.MakeBucket(context.Background()); err != nil {
			t.Fatalf("make bucket #%d: %v", i+1, err)
		}
	}
	if !f.buckets["media"] {
		t.Fatal("bucket not created")
	}
}
