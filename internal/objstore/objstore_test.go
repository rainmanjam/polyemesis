package objstore

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Obviously fake, low-entropy credentials: gitleaks scans every commit.
const (
	fakeKeyID  = "fake-access-key"
	fakeSecret = "fake-secret-value-not-real"
)

type seen struct {
	method, host, path string
	header             http.Header
	body               []byte
}

func fakeServer(t *testing.T, status int, respBody string, extra http.Header) (*httptest.Server, *[]seen) {
	t.Helper()
	var got []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, seen{r.Method, r.Host, r.URL.EscapedPath(), r.Header.Clone(), b})
		for k, v := range extra {
			w.Header()[k] = v
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, respBody)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func newClient(t *testing.T, endpoint string, pathStyle bool) *Client {
	t.Helper()
	c, err := New(Config{Endpoint: endpoint, Bucket: "media", Region: "us-east-1", PathStyle: pathStyle,
		AccessKeyID: fakeKeyID, SecretAccessKey: fakeSecret})
	if err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC) }
	return c
}

func digests(b []byte) (string, string) {
	s := sha256.Sum256(b)
	m := md5.Sum(b)
	return hex.EncodeToString(s[:]), base64.StdEncoding.EncodeToString(m[:])
}

func TestPutSignsTheRealPayloadDigestAndSendsContentMD5(t *testing.T) {
	srv, got := fakeServer(t, 200, "", http.Header{"Etag": {`"abc"`}})
	c := newClient(t, srv.URL, true)
	body := []byte("segment bytes")
	sh, mb := digests(body)
	etag, err := c.Put(context.Background(), "rec/a b+c.mkv", bytes.NewReader(body), int64(len(body)), sh, mb, map[string]string{"sha256": sh})
	if err != nil {
		t.Fatal(err)
	}
	if etag != "abc" {
		t.Fatalf("etag %q", etag)
	}
	r := (*got)[0]
	if r.method != http.MethodPut || r.path != "/media/rec/a%20b%2Bc.mkv" {
		t.Fatalf("got %s %s", r.method, r.path)
	}
	if r.header.Get("X-Amz-Content-Sha256") != sh {
		t.Fatalf("payload hash header %q, want the real digest %q", r.header.Get("X-Amz-Content-Sha256"), sh)
	}
	if r.header.Get("Content-Md5") != mb {
		t.Fatalf("Content-MD5 %q", r.header.Get("Content-Md5"))
	}
	if r.header.Get("X-Amz-Meta-Sha256") != sh {
		t.Fatal("sha256 metadata missing")
	}
	auth := r.header.Get("Authorization")
	for _, want := range []string{"AWS4-HMAC-SHA256 Credential=" + fakeKeyID + "/20261101/us-east-1/s3/aws4_request",
		"SignedHeaders=content-md5;content-type;host;x-amz-content-sha256;x-amz-date;x-amz-meta-sha256", "Signature="} {
		if !strings.Contains(auth, want) {
			t.Fatalf("Authorization %q lacks %q", auth, want)
		}
	}
	if strings.Contains(auth, fakeSecret) {
		t.Fatal("the secret itself rode in the Authorization header")
	}
	if !bytes.Equal(r.body, body) {
		t.Fatal("body not sent intact")
	}
}

func TestVirtualHostedStylePutsTheBucketInTheHost(t *testing.T) {
	c := newClient(t, "https://s3.example.test", false)
	u, path := c.objectURL("x/y.ts")
	if u.Host != "media.s3.example.test" || path != "/x/y.ts" {
		t.Fatalf("host %q path %q", u.Host, path)
	}
	c2 := newClient(t, "https://s3.example.test/", true)
	u2, path2 := c2.objectURL("x/y.ts")
	if u2.Host != "s3.example.test" || path2 != "/media/x/y.ts" {
		t.Fatalf("path style host %q path %q", u2.Host, path2)
	}
}

func TestADottedBucketFallsBackToPathStyleOverTLS(t *testing.T) {
	c, err := New(Config{Endpoint: "https://s3.example.test", Bucket: "my.bucket", AccessKeyID: fakeKeyID, SecretAccessKey: fakeSecret})
	if err != nil {
		t.Fatal(err)
	}
	if !c.cfg.PathStyle {
		t.Fatal("dotted bucket over https must use path style")
	}
}

func TestSignatureIsDeterministicAndKeyedOnTheSecret(t *testing.T) {
	sig := func(secret string) string {
		c, _ := New(Config{Endpoint: "http://h.test", Bucket: "b", PathStyle: true, AccessKeyID: fakeKeyID, SecretAccessKey: secret})
		c.now = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }
		req, _ := http.NewRequest(http.MethodGet, "http://h.test/b/k", nil)
		c.sign(req, "/b/k", emptySHA256)
		a := req.Header.Get("Authorization")
		return a[strings.Index(a, "Signature=")+10:]
	}
	a, b := sig(fakeSecret), sig(fakeSecret)
	if a != b || len(a) != 64 {
		t.Fatalf("signature not deterministic: %q %q", a, b)
	}
	if sig("another-fake-secret") == a {
		t.Fatal("signature does not depend on the secret")
	}
}

func TestServiceErrorsCarryCodeButNeverTheSecret(t *testing.T) {
	srv, _ := fakeServer(t, 403, `<?xml version="1.0"?><Error><Code>SignatureDoesNotMatch</Code><Message>nope</Message></Error>`, nil)
	c := newClient(t, srv.URL, true)
	err := c.Delete(context.Background(), "k")
	var se *Error
	if !errors.As(err, &se) || se.Code != "SignatureDoesNotMatch" || se.StatusCode != 403 {
		t.Fatalf("err %v", err)
	}
	if Retryable(err) {
		t.Fatal("a refused signature is not retryable")
	}
	for _, s := range []string{err.Error(), fmt.Sprintf("%v", c.cfg), fmt.Sprintf("%#v", c.cfg), fmt.Sprintf("%+v", c.cfg)} {
		if strings.Contains(s, fakeSecret) {
			t.Fatalf("secret leaked into %q", s)
		}
	}
}

func TestNotFoundIsMatchableOnHeadAndGet(t *testing.T) {
	srv, _ := fakeServer(t, 404, `<Error><Code>NoSuchKey</Code></Error>`, nil)
	c := newClient(t, srv.URL, true)
	if _, err := c.Head(context.Background(), "k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("head: %v", err)
	}
	if _, _, err := c.Get(context.Background(), "k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get: %v", err)
	}
}

func TestHeadReportsSizeEtagAndMetadata(t *testing.T) {
	srv, _ := fakeServer(t, 200, "", http.Header{"Etag": {`"d41d"`}, "X-Amz-Meta-Sha256": {"cafe"}, "Content-Length": {"0"}})
	c := newClient(t, srv.URL, true)
	info, err := c.Head(context.Background(), "k")
	if err != nil {
		t.Fatal(err)
	}
	if info.ETag != "d41d" || info.Meta["sha256"] != "cafe" {
		t.Fatalf("%+v", info)
	}
}

func TestRetryableClassification(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{&Error{StatusCode: 503}, true},
		{&Error{StatusCode: 429}, true},
		{&Error{StatusCode: 400, Code: "BadDigest"}, true},
		{&Error{StatusCode: 403, Code: "AccessDenied"}, false},
		{&Error{StatusCode: 404, Code: "NoSuchBucket"}, false},
		{errors.New("dial tcp: refused"), true},
		{context.Canceled, false},
		{nil, false},
	}
	for _, tc := range cases {
		if got := Retryable(tc.err); got != tc.want {
			t.Errorf("Retryable(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestNewRefusesUnusableConfigs(t *testing.T) {
	bad := []Config{
		{Endpoint: "", Bucket: "b", AccessKeyID: "a", SecretAccessKey: "s"},
		{Endpoint: "ftp://h", Bucket: "b", AccessKeyID: "a", SecretAccessKey: "s"},
		{Endpoint: "http://h?x=1", Bucket: "b", AccessKeyID: "a", SecretAccessKey: "s"},
		{Endpoint: "http://u:p@h", Bucket: "b", AccessKeyID: "a", SecretAccessKey: "s"},
		{Endpoint: "http://h", Bucket: "b"},
	}
	for _, cfg := range bad {
		if _, err := New(cfg); err == nil {
			t.Errorf("New(%v) accepted", cfg)
		} else if strings.Contains(err.Error(), "s\"") && cfg.SecretAccessKey != "" {
			t.Errorf("error echoes config: %v", err)
		}
	}
}

func TestPutRefusesAnObjectOverTheSinglePutLimit(t *testing.T) {
	c := newClient(t, "http://h.test", true)
	if _, err := c.Put(context.Background(), "k", bytes.NewReader(nil), MaxSinglePut+1, emptySHA256, "", nil); err == nil {
		t.Fatal("oversized put accepted")
	}
}

func TestKeysMustBeRelative(t *testing.T) {
	c := newClient(t, "http://h.test", true)
	for _, k := range []string{"", "/abs"} {
		if err := c.Delete(context.Background(), k); err == nil {
			t.Errorf("key %q accepted", k)
		}
	}
}
