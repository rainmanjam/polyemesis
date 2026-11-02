// Package objstore is a deliberately small S3-compatible object client: put,
// head, get and delete of single objects, signed with AWS Signature V4.
//
// It exists instead of aws-sdk-go-v2 or minio-go because offload needs four
// verbs and nothing else -- no multipart, no listing, no presigning, no STS --
// and either SDK would add a large transitive tree (and its advisories) to a
// binary that otherwise carries almost no third-party code. SigV4 over plain
// net/http is a few hundred lines and is the same wire protocol every target
// this feature names speaks: AWS S3, Cloudflare R2, Backblaze B2 (S3 API),
// MinIO and Wasabi.
//
// Integrity is carried on the wire, not trusted after the fact: every PUT
// signs the real SHA-256 of the payload into x-amz-content-sha256 (the server
// recomputes it and refuses a mismatch with XAmzContentSHA256Mismatch) and
// sends Content-MD5 (refused with BadDigest). The caller then verifies the
// stored object with Head (and optionally a full Get) before it treats the
// upload as durable.
//
// Credentials never appear in an error, a log line or a String(): Config
// formats itself with the secret redacted, and server error bodies are
// reduced to their S3 Code and Message, which do not echo the signature.
package objstore

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// MaxSinglePut is the S3 single-request PUT ceiling. Offload refuses a larger
// file up front with a clear reason rather than letting the server answer
// EntityTooLarge after the whole body was sent.
const MaxSinglePut int64 = 5 << 30

// Config names one bucket on one S3-compatible endpoint.
type Config struct {
	// Endpoint is the service base URL, scheme included, e.g.
	// https://s3.us-east-1.amazonaws.com, https://<acct>.r2.cloudflarestorage.com,
	// https://s3.us-west-004.backblazeb2.com, http://minio.lan:9000.
	Endpoint string
	Bucket   string
	// Region signs the request scope. R2 uses "auto"; MinIO accepts any
	// value and defaults to us-east-1.
	Region string
	// PathStyle addresses the bucket as <endpoint>/<bucket>/<key> instead of
	// <bucket>.<endpoint-host>/<key>. MinIO and most self-hosted targets need
	// it; AWS, R2, B2 and Wasabi accept virtual-hosted style.
	PathStyle       bool
	AccessKeyID     string
	SecretAccessKey string
	// HTTPClient is the transport. The caller installs the SSRF dial guard
	// on it; nil means a plain client with a conservative timeout.
	HTTPClient *http.Client
}

// String renders the config without its secret, so a %v in a log line or an
// error wrap can never leak it.
func (c Config) String() string {
	return fmt.Sprintf("objstore.Config{Endpoint:%q Bucket:%q Region:%q PathStyle:%t AccessKeyID:%q SecretAccessKey:[redacted]}",
		c.Endpoint, c.Bucket, c.Region, c.PathStyle, c.AccessKeyID)
}

// GoString keeps %#v as safe as %v.
func (c Config) GoString() string { return c.String() }

// Client talks to one bucket.
type Client struct {
	cfg  Config
	base *url.URL
	hc   *http.Client
	now  func() time.Time
}

// ObjectInfo is what Head and Get report about a stored object.
type ObjectInfo struct {
	Size int64
	// ETag is the server's entity tag with its quotes removed. For a single
	// PUT without SSE-KMS it is the hex MD5 of the body on every target this
	// package names.
	ETag string
	// Meta is the x-amz-meta-* user metadata, keys lower-cased and without
	// the prefix.
	Meta map[string]string
}

// Error is a non-2xx answer from the service.
type Error struct {
	Op         string
	StatusCode int
	Code       string
	Message    string
}

func (e *Error) Error() string {
	msg := e.Code
	if e.Message != "" {
		msg += ": " + e.Message
	}
	if msg == "" {
		msg = http.StatusText(e.StatusCode)
	}
	return fmt.Sprintf("%s: HTTP %d %s", e.Op, e.StatusCode, msg)
}

// ErrNotFound is matched by errors.Is for a 404 on any verb.
var ErrNotFound = errors.New("object not found")

// Is lets errors.Is(err, ErrNotFound) see a 404.
func (e *Error) Is(target error) bool {
	return target == ErrNotFound && e.StatusCode == http.StatusNotFound
}

// Retryable reports whether the failure is worth another attempt: transport
// errors, throttling and server-side faults are; a refused signature, a
// missing bucket or a digest mismatch the server caught is not going to fix
// itself by waiting -- except a digest mismatch, which a re-read of a file
// that was still settling can cure, so the caller decides that one.
func Retryable(err error) bool {
	var se *Error
	if errors.As(err, &se) {
		return se.StatusCode == http.StatusTooManyRequests || se.StatusCode >= 500 ||
			se.StatusCode == http.StatusRequestTimeout || se.Code == "SlowDown" ||
			se.Code == "RequestTimeout" || se.Code == "BadDigest" ||
			se.Code == "XAmzContentSHA256Mismatch"
	}
	return err != nil && !errors.Is(err, context.Canceled)
}

// New validates cfg and returns a client.
func New(cfg Config) (*Client, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" {
		return nil, errors.New("objstore: endpoint and bucket are required")
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("objstore: endpoint must be an http(s) URL with a host, got %q", cfg.Endpoint)
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, errors.New("objstore: endpoint must not carry a query, fragment or userinfo")
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	if cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return nil, errors.New("objstore: access key id and secret are required")
	}
	if !cfg.PathStyle && strings.Contains(cfg.Bucket, ".") && u.Scheme == "https" {
		// A dotted bucket in a virtual-hosted name breaks the endpoint's
		// wildcard certificate; path style is the only working form.
		cfg.PathStyle = true
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Minute}
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	return &Client{cfg: cfg, base: u, hc: hc, now: time.Now}, nil
}

// Put stores body under key. size, sha256Hex and md5B64 describe the exact
// bytes body will yield; the service recomputes both digests and refuses the
// object if either disagrees, so a body that changed underneath the caller
// fails here rather than landing corrupt. body is re-read from offset 0 on a
// transport-level retry, hence io.ReadSeeker.
func (c *Client) Put(ctx context.Context, key string, body io.ReadSeeker, size int64, sha256Hex, md5B64 string, meta map[string]string) (string, error) {
	if size > MaxSinglePut {
		return "", fmt.Errorf("objstore: %d bytes exceeds the %d-byte single PUT limit", size, MaxSinglePut)
	}
	h := http.Header{}
	h.Set("Content-MD5", md5B64)
	h.Set("Content-Type", "application/octet-stream")
	for k, v := range meta {
		h.Set("X-Amz-Meta-"+k, v)
	}
	resp, err := c.do(ctx, "PutObject", http.MethodPut, key, h, body, size, sha256Hex)
	if err != nil {
		return "", err
	}
	defer drain(resp)
	return strings.Trim(resp.Header.Get("ETag"), `"`), nil
}

// Head reports size, ETag and user metadata without fetching the body.
func (c *Client) Head(ctx context.Context, key string) (ObjectInfo, error) {
	resp, err := c.do(ctx, "HeadObject", http.MethodHead, key, nil, nil, 0, emptySHA256)
	if err != nil {
		return ObjectInfo{}, err
	}
	defer drain(resp)
	return infoFrom(resp), nil
}

// Get opens the object for reading. The caller closes the body.
func (c *Client) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	resp, err := c.do(ctx, "GetObject", http.MethodGet, key, nil, nil, 0, emptySHA256)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	return resp.Body, infoFrom(resp), nil
}

// Delete removes the object. S3 answers 204 whether or not it existed.
func (c *Client) Delete(ctx context.Context, key string) error {
	resp, err := c.do(ctx, "DeleteObject", http.MethodDelete, key, nil, nil, 0, emptySHA256)
	if err != nil {
		return err
	}
	drain(resp)
	return nil
}

const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func infoFrom(resp *http.Response) ObjectInfo {
	info := ObjectInfo{ETag: strings.Trim(resp.Header.Get("ETag"), `"`), Meta: map[string]string{}}
	if n, err := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64); err == nil {
		info.Size = n
	}
	for k, v := range resp.Header {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "x-amz-meta-") && len(v) > 0 {
			info.Meta[strings.TrimPrefix(lk, "x-amz-meta-")] = v[0]
		}
	}
	return info
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
}

// objectURL returns the request URL and the canonical (already-escaped) path.
func (c *Client) objectURL(key string) (*url.URL, string) {
	u := *c.base
	var path string
	if c.cfg.PathStyle {
		path = u.Path + "/" + escapePath(c.cfg.Bucket) + "/" + escapePath(key)
	} else {
		u.Host = c.cfg.Bucket + "." + u.Host
		path = u.Path + "/" + escapePath(key)
	}
	u.Path = ""
	u.RawPath = ""
	u.Opaque = ""
	out, _ := url.Parse(u.String() + path)
	return out, path
}

func (c *Client) do(ctx context.Context, op, method, key string, hdr http.Header, body io.ReadSeeker, size int64, payloadSHA string) (*http.Response, error) {
	if key == "" || strings.HasPrefix(key, "/") {
		return nil, fmt.Errorf("objstore: %s: key must be non-empty and relative", op)
	}
	u, canonPath := c.objectURL(key)
	var rdr io.Reader
	if body != nil {
		if _, err := body.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		rdr = io.NopCloser(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rdr)
	if err != nil {
		return nil, err
	}
	// Keep the path exactly as it was signed; net/http would otherwise
	// re-escape characters S3 and SigV4 treat differently.
	req.URL.Opaque = "//" + u.Host + canonPath
	if body != nil {
		req.ContentLength = size
		req.GetBody = func() (io.ReadCloser, error) {
			if _, err := body.Seek(0, io.SeekStart); err != nil {
				return nil, err
			}
			return io.NopCloser(body), nil
		}
	}
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	c.sign(req, canonPath, payloadSHA)
	resp, err := c.hc.Do(req)
	if err != nil {
		// url.Error carries the URL, which holds no credential: the
		// signature is in a header, never the query.
		return nil, fmt.Errorf("objstore: %s: %w", op, err)
	}
	if resp.StatusCode/100 != 2 {
		defer drain(resp)
		return nil, parseError(op, resp)
	}
	return resp, nil
}

func parseError(op string, resp *http.Response) error {
	e := &Error{Op: op, StatusCode: resp.StatusCode}
	if resp.Request != nil && resp.Request.Method == http.MethodHead {
		return e
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	var x struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	if xml.Unmarshal(raw, &x) == nil {
		e.Code = clip(x.Code, 64)
		e.Message = clip(x.Message, 200)
	}
	return e
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}

// sign adds the SigV4 Authorization header.
func (c *Client) sign(req *http.Request, canonPath, payloadSHA string) {
	t := c.now().UTC()
	amzDate := t.Format("20060102T150405Z")
	day := t.Format("20060102")
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadSHA)
	host := req.URL.Host
	if host == "" {
		host = req.Host
	}

	names := []string{"host"}
	vals := map[string]string{"host": host}
	for k, vs := range req.Header {
		lk := strings.ToLower(k)
		if lk == "authorization" || lk == "user-agent" || lk == "accept-encoding" {
			continue
		}
		names = append(names, lk)
		vals[lk] = strings.Join(trimAll(vs), ",")
	}
	sort.Strings(names)
	var ch strings.Builder
	for _, n := range names {
		ch.WriteString(n)
		ch.WriteByte(':')
		ch.WriteString(vals[n])
		ch.WriteByte('\n')
	}
	signed := strings.Join(names, ";")
	canon := strings.Join([]string{req.Method, canonPath, canonicalQuery(req.URL.Query()), ch.String(), signed, payloadSHA}, "\n")
	scope := day + "/" + c.cfg.Region + "/s3/aws4_request"
	sts := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hexSHA([]byte(canon))
	k := hmacSHA([]byte("AWS4"+c.cfg.SecretAccessKey), day)
	k = hmacSHA(k, c.cfg.Region)
	k = hmacSHA(k, "s3")
	k = hmacSHA(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA(k, sts))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+c.cfg.AccessKeyID+"/"+scope+
		", SignedHeaders="+signed+", Signature="+sig)
}

func trimAll(vs []string) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = strings.Join(strings.Fields(v), " ")
	}
	return out
}

func canonicalQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b bytes.Buffer
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			if b.Len() > 0 {
				b.WriteByte('&')
			}
			b.WriteString(escape(k, true))
			b.WriteByte('=')
			b.WriteString(escape(v, true))
		}
	}
	return b.String()
}

// escapePath URI-encodes every byte except unreserved characters and '/',
// which is exactly the SigV4 rule for S3 object paths (S3 does not
// double-encode).
func escapePath(p string) string { return escape(p, false) }

func escape(s string, encodeSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'A' && ch <= 'Z', ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9',
			ch == '-', ch == '_', ch == '.', ch == '~':
			b.WriteByte(ch)
		case ch == '/' && !encodeSlash:
			b.WriteByte(ch)
		default:
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}

func hexSHA(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func hmacSHA(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}
