package transcribe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A truncated model does not fail to load — it produces fluent nonsense. These
// tests are about making that failure loud.

// TestTheGGMLMagicIsTheBytesOnDiskNotTheBytesInTheName pins the magic to a
// literal, because every other test in this package builds its fixtures with
// `copy(buf, ggmlMagic)` and would therefore agree with any value it held.
//
// That is not hypothetical. ggmlMagic shipped byte-reversed as {0x67, 0x67,
// 0x6d, 0x6c} — the order the constant is pronounced, "ggml" — while a real
// model begins 6c 6d 67 67, the little-endian spelling of the uint32
// GGML_FILE_MAGIC (0x67676d6c) that the converter fwrites. looksLikeGGML
// therefore rejected every genuine whisper.cpp model: Download failed claiming
// "the server most likely returned an error page", and InstalledModels silently
// skipped models copied in by hand. The whole offline suite stayed green
// throughout, which is exactly why this test spells the bytes out.
//
// Proven able to fail against the committed tree by reversing ggmlMagic in
// download.go back to []byte{0x67, 0x67, 0x6d, 0x6c}.
func TestTheGGMLMagicIsTheBytesOnDiskNotTheBytesInTheName(t *testing.T) {
	// Transcribed from the head of ggml-tiny.bin as served by Hugging Face, and
	// deliberately not derived from anything in the package under test.
	onDisk := []byte{0x6c, 0x6d, 0x67, 0x67}
	if string(ggmlMagic) != string(onDisk) {
		t.Fatalf("ggmlMagic = % x (%q), want % x (%q) — a real model starts with the little-endian "+
			"spelling of GGML_FILE_MAGIC, so this value rejects every genuine model",
			ggmlMagic, ggmlMagic, onDisk, onDisk)
	}

	// And the consequence, stated where it bites: the gate that decides whether
	// a file is a model at all.
	dir := t.TempDir()
	path := filepath.Join(dir, "ggml-real.bin")
	if err := os.WriteFile(path, append(onDisk, make([]byte, minModelBytes)...), 0o644); err != nil {
		t.Fatal(err)
	}
	if !looksLikeGGML(path) {
		t.Fatal("looksLikeGGML rejected a file starting with the real on-disk magic")
	}
}

// modelBody builds a plausible model payload: the ggml magic, then filler.
func modelBody(n int) []byte {
	buf := make([]byte, n)
	copy(buf, ggmlMagic)
	for i := len(ggmlMagic); i < n; i++ {
		buf[i] = byte(i)
	}
	return buf
}

// serve stands in for Hugging Face. hook may rewrite the response.
func serve(t *testing.T, body []byte, hook func(w http.ResponseWriter, r *http.Request) bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hook != nil && hook(w, r) {
			return
		}
		w.Header().Set("Content-Length", itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// downloadFrom points a Downloader at a test server by overriding the model
// URL through a transport that rewrites the host.
func downloadFrom(t *testing.T, srv *httptest.Server, dir string, m Model, progress ProgressFunc) (DownloadResult, error) {
	t.Helper()
	d := &Downloader{Dir: dir, Client: srv.Client()}
	d.Client.Transport = rewriteHost{base: srv.URL, next: srv.Client().Transport}
	return d.Download(context.Background(), m, progress)
}

type rewriteHost struct {
	base string
	next http.RoundTripper
}

func (rt rewriteHost) RoundTrip(r *http.Request) (*http.Response, error) {
	u, err := r.URL.Parse(rt.base)
	if err != nil {
		return nil, err
	}
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
	next := rt.next
	if next == nil {
		next = http.DefaultTransport
	}
	return next.RoundTrip(r)
}

func testModel(size int) Model {
	return Model{Name: "test", Size: SizeTiny, Bytes: int64(size), RAMBytes: 1 << 20, RelSpeed: 32, Accuracy: 1}
}

func TestDownloadWritesAVerifiedModelAndNoPartFile(t *testing.T) {
	body := modelBody(minModelBytes + 512)
	srv := serve(t, body, nil)
	dir := t.TempDir()

	var lastFraction float64
	res, err := downloadFrom(t, srv, dir, testModel(len(body)), func(f float64, done, total int64) {
		lastFraction = f
	})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if res.Bytes != int64(len(body)) {
		t.Errorf("wrote %d bytes, want %d", res.Bytes, len(body))
	}
	if lastFraction != 1 {
		t.Errorf("final progress = %v, want 1", lastFraction)
	}
	if res.SHA1 == "" {
		t.Error("no SHA-1 reported; the operator has nothing to compare against upstream")
	}
	if _, err := os.Stat(filepath.Join(dir, "ggml-test.bin"+partSuffix)); err == nil {
		t.Error("a part-file survived a successful download")
	}
	if _, err := os.Stat(filepath.Join(dir, "ggml-test.bin")); err != nil {
		t.Errorf("the model is not at its final name: %v", err)
	}
}

func TestATruncatedDownloadIsRejectedAndLeavesNothingBehind(t *testing.T) {
	body := modelBody(minModelBytes + 512)
	srv := serve(t, body, func(w http.ResponseWriter, r *http.Request) bool {
		// Promise the full size, deliver half. This is exactly what a dropped
		// connection looks like, and exactly the case that would otherwise
		// produce confident garbage transcripts.
		w.Header().Set("Content-Length", itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		w.Write(body[:len(body)/2])
		return true
	})
	dir := t.TempDir()

	_, err := downloadFrom(t, srv, dir, testModel(len(body)), nil)
	if err == nil {
		t.Fatal("a truncated download was accepted")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("a rejected download left %d file(s) behind", len(entries))
	}
}

func TestAnErrorPageServedWithA200IsRejectedByTheMagicBytes(t *testing.T) {
	page := []byte(strings.Repeat("<html>not a model</html>", 100_000))
	srv := serve(t, page, nil)

	_, err := downloadFrom(t, srv, t.TempDir(), testModel(len(page)), nil)
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("err = %v, want a checksum failure naming the magic bytes", err)
	}
	if !strings.Contains(err.Error(), "ggml") {
		t.Errorf("err = %v, want it to explain what was wrong", err)
	}
}

func TestTheServersAdvertisedSHA256IsEnforcedWhenItIsOffered(t *testing.T) {
	body := modelBody(minModelBytes + 64)
	sum := sha256.Sum256(body)

	t.Run("a matching hash is accepted and recorded as the strongest check", func(t *testing.T) {
		srv := serve(t, body, func(w http.ResponseWriter, r *http.Request) bool {
			w.Header().Set("X-Linked-Etag", `"`+hex.EncodeToString(sum[:])+`"`)
			w.Header().Set("Content-Length", itoa(len(body)))
			w.WriteHeader(http.StatusOK)
			w.Write(body)
			return true
		})
		res, err := downloadFrom(t, srv, t.TempDir(), testModel(len(body)), nil)
		if err != nil {
			t.Fatalf("Download: %v", err)
		}
		if res.Verified != "sha256" {
			t.Errorf("Verified = %q, want sha256", res.Verified)
		}
	})

	t.Run("a mismatched hash is rejected", func(t *testing.T) {
		wrong := sha256.Sum256([]byte("something else"))
		srv := serve(t, body, func(w http.ResponseWriter, r *http.Request) bool {
			w.Header().Set("X-Linked-Etag", hex.EncodeToString(wrong[:]))
			w.Header().Set("Content-Length", itoa(len(body)))
			w.WriteHeader(http.StatusOK)
			w.Write(body)
			return true
		})
		dir := t.TempDir()
		if _, err := downloadFrom(t, srv, dir, testModel(len(body)), nil); !errors.Is(err, ErrChecksum) {
			t.Fatalf("err = %v, want a checksum failure", err)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Error("a file that failed its checksum was left on disk")
		}
	})
}

// The restrictive-direction failure this codebase keeps relearning: refusing a
// download because there was no checksum to compare it against would break the
// feature behind any proxy that rewrites ETags, and buy nothing.
func TestADownloadWithNoAdvertisedChecksumIsStillAccepted(t *testing.T) {
	body := modelBody(minModelBytes + 8)
	srv := serve(t, body, func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Etag", `W/"weak-opaque-tag"`) // says nothing about the content
		w.Header().Set("Content-Length", itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		w.Write(body)
		return true
	})
	res, err := downloadFrom(t, srv, t.TempDir(), testModel(len(body)), nil)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if res.Verified != "length" {
		t.Errorf("Verified = %q, want the byte-count check to be what was enforced", res.Verified)
	}
}

func TestCatalogueSHA1IsEnforcedWhenTheInstallHasBeenToldOne(t *testing.T) {
	body := modelBody(minModelBytes + 16)
	srv := serve(t, body, nil)
	m := testModel(len(body))
	m.SHA1 = "0000000000000000000000000000000000000000"

	if _, err := downloadFrom(t, srv, t.TempDir(), m, nil); !errors.Is(err, ErrChecksum) {
		t.Fatalf("err = %v, want the catalogue SHA-1 to be enforced", err)
	}
}

func TestChecksumFromHeadersOnlyTrustsAFullContentHash(t *testing.T) {
	full := strings.Repeat("ab", 32)
	tests := []struct {
		name   string
		hdr    http.Header
		want   string
		wantOK bool
	}{
		{"linked etag, quoted", http.Header{"X-Linked-Etag": {`"` + full + `"`}}, full, true},
		{"linked etag, bare", http.Header{"X-Linked-Etag": {full}}, full, true},
		{"plain etag", http.Header{"Etag": {`"` + full + `"`}}, full, true},
		{"weak etag is not a content hash", http.Header{"Etag": {`W/"` + full + `"`}}, "", false},
		{"a short opaque tag", http.Header{"Etag": {`"abc123"`}}, "", false},
		{"an md5-shaped tag is not sha256", http.Header{"Etag": {strings.Repeat("a", 32)}}, "", false},
		{"no headers", nil, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := checksumFromHeaders(tc.hdr)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("checksumFromHeaders = %q, %v; want %q, %v", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestAnAlreadyDownloadedModelIsNotFetchedAgain(t *testing.T) {
	body := modelBody(minModelBytes + 32)
	hits := 0
	srv := serve(t, body, func(w http.ResponseWriter, r *http.Request) bool {
		hits++
		return false
	})
	dir := t.TempDir()
	m := testModel(len(body))

	if _, err := downloadFrom(t, srv, dir, m, nil); err != nil {
		t.Fatalf("first Download: %v", err)
	}
	res, err := downloadFrom(t, srv, dir, m, nil)
	if err != nil {
		t.Fatalf("second Download: %v", err)
	}
	if hits != 1 {
		t.Errorf("server was hit %d times; a present, valid model must not be re-fetched", hits)
	}
	if res.Verified != "existing" {
		t.Errorf("Verified = %q, want the existing file to be reported as such", res.Verified)
	}
}

func TestACorruptModelOnDiskIsReplacedRatherThanReused(t *testing.T) {
	body := modelBody(minModelBytes + 32)
	srv := serve(t, body, nil)
	dir := t.TempDir()
	// A previous interrupted download, at the final name.
	if err := os.WriteFile(filepath.Join(dir, "ggml-test.bin"), []byte("<html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := downloadFrom(t, srv, dir, testModel(len(body)), nil); err != nil {
		t.Fatalf("Download: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "ggml-test.bin"))
	if err != nil || len(got) != len(body) {
		t.Fatalf("the corrupt file was not replaced: %d bytes, err %v", len(got), err)
	}
}

func TestVerifyModelFile(t *testing.T) {
	dir := t.TempDir()
	writeFile := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	full := writeFile("full.bin", modelBody(minModelBytes+100))
	tiny := writeFile("tiny.bin", modelBody(16))
	html := writeFile("html.bin", []byte(strings.Repeat("<html>", 200_000)))

	tests := []struct {
		name    string
		path    string
		model   Model
		wantErr bool
	}{
		{name: "a good file against its catalogue entry", path: full, model: testModel(minModelBytes + 100)},
		{name: "a good file with no catalogue entry to compare against", path: full},
		{name: "a file below the floor", path: tiny, wantErr: true},
		{name: "an error page", path: html, wantErr: true},
		{name: "half a model", path: full, model: testModel(4 * minModelBytes), wantErr: true},
		{name: "a missing file", path: filepath.Join(dir, "gone.bin"), wantErr: true},
		{
			// A size a tenth off is within tolerance: the published figures drift
			// when upstream re-uploads, and rejecting a working model over that
			// is the restrictive-direction mistake.
			name:  "a size within the tolerance",
			path:  full,
			model: testModel(minModelBytes + 100 + (minModelBytes+100)/20),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := VerifyModelFile(tc.path, tc.model)
			if (err != nil) != tc.wantErr {
				t.Errorf("VerifyModelFile = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestDownloadStopsWhenTheContextIsCancelled(t *testing.T) {
	body := modelBody(minModelBytes + 4096)
	srv := serve(t, body, nil)
	dir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := &Downloader{Dir: dir, Client: srv.Client()}
	d.Client.Transport = rewriteHost{base: srv.URL}
	if _, err := d.Download(ctx, testModel(len(body)), nil); err == nil {
		t.Fatal("a cancelled download reported success")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Error("a cancelled download left a file behind")
	}
}
