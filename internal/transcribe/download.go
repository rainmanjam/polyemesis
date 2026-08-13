package transcribe

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Model downloading, and the reason it is paranoid.
//
// A truncated whisper model does not fail to load. It loads, and it produces
// fluent, confident, completely wrong text — the worst failure mode available,
// because nothing downstream can detect it and a human reading the transcript
// has no reason to doubt it. Every integrity check here exists to turn that
// silent failure into a loud one.
//
// Three checks, in increasing order of strength, and all of them are things we
// can verify at transfer time rather than constants baked into this file:
//
//  1. The file starts with the GGML magic. Catches the single most common
//     failure: a proxy or a login wall serving an HTML error page with a 200.
//  2. The byte count equals the Content-Length the server promised. This is the
//     exact truncation check, and unlike a hardcoded size it cannot go stale.
//  3. The content hash equals the strong checksum the server advertised.
//     Hugging Face serves LFS objects with the object's SHA-256 in X-Linked-Etag,
//     so this is a real end-to-end checksum with nothing hardcoded. When the
//     server offers no such header the download is still accepted — see below.
//
// A catalogue SHA-1, if the install has been told one, is checked too.
//
// What we deliberately do NOT do is refuse a download because we had no
// checksum to compare it against. That would be a check that is wrong in the
// restrictive direction: it makes the feature unusable behind any proxy that
// rewrites ETags, in exchange for no additional safety over checks 1 and 2.

// partSuffix marks an in-flight download. The final rename is atomic, so a
// model file either does not exist or is complete and verified; there is no
// window in which a half-written file looks installed.
const partSuffix = ".part"

// downloadTimeout bounds a whole model transfer. The large models are three
// gigabytes, and an operator on a slow connection is not an error.
const downloadTimeout = 2 * time.Hour

// ggmlMagic is the four bytes every ggml model starts with.
//
// GGML_FILE_MAGIC is the uint32 0x67676d6c, and the converter writes it with a
// plain fwrite of the integer — so what lands on disk is its LITTLE-ENDIAN
// spelling, 6c 6d 67 67, which reads as "lmgg". Writing the bytes in the order
// the constant is pronounced gives "ggml", which is the byte sequence no real
// model has ever started with.
//
// That was this variable's value until scripts/acceptance-transcribe.sh
// compared it against a model fetched from the real host. Every test in this
// package builds its fixtures with `copy(buf, ggmlMagic)`, so the offline suite
// agreed with itself perfectly while looksLikeGGML rejected every genuine
// whisper.cpp model: downloads failed as "the server most likely returned an
// error page", and InstalledModels hid models copied in by hand.
var ggmlMagic = []byte{0x6c, 0x6d, 0x67, 0x67}

// minModelBytes is a floor no real model is under. It exists to reject an error
// page, not to validate a model; the real size check is Content-Length.
const minModelBytes = 1 << 20

// ErrChecksum is returned when a download's content does not match what the
// server said it would be.
var ErrChecksum = errors.New("model checksum mismatch")

// DownloadResult describes a completed download.
type DownloadResult struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
	// SHA1 of the downloaded file, always computed. Reported so an operator can
	// compare it against upstream's published list by eye if they want to,
	// which is the honest answer to "we do not ship checksums".
	SHA1 string `json:"sha1"`
	// Verified names the checksum that was actually enforced: "sha256" from the
	// server's ETag, "sha1" from the catalogue, or "length" when only the byte
	// count could be checked.
	Verified string `json:"verified"`
}

// Downloader fetches models into a directory.
type Downloader struct {
	// Dir is where models are written. Created on demand.
	Dir string
	// Client defaults to a plain http.Client with no timeout, because the
	// per-request deadline is carried on the context instead — an http.Client
	// timeout covers the whole body read and would kill a legitimate 3 GB
	// transfer on a slow link.
	Client *http.Client
}

// ProgressFunc reports transfer progress. total is 0 when the server did not
// say how big the file is.
type ProgressFunc func(fraction float64, downloaded, total int64)

// Download fetches a model and verifies it before it is visible under its final
// name.
//
// An already-present, valid model is returned immediately: re-downloading three
// gigabytes because a job was retried would be its own kind of failure.
func (d *Downloader) Download(ctx context.Context, m Model, progress ProgressFunc) (DownloadResult, error) {
	if d.Dir == "" {
		return DownloadResult{}, errors.New("no model directory configured")
	}
	if err := os.MkdirAll(d.Dir, 0o755); err != nil {
		return DownloadResult{}, fmt.Errorf("create model directory: %w", err)
	}
	final := filepath.Join(d.Dir, m.Filename())
	if st, err := os.Stat(final); err == nil && !st.IsDir() {
		if err := VerifyModelFile(final, m); err == nil {
			if progress != nil {
				progress(1, st.Size(), st.Size())
			}
			return DownloadResult{Path: final, Bytes: st.Size(), Verified: "existing"}, nil
		}
		// A file that is there but does not verify is a previous interrupted or
		// corrupted download. Replacing it is the only useful thing to do.
	}

	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.URL(), nil)
	if err != nil {
		return DownloadResult{}, err
	}
	base := d.Client
	if base == nil {
		base = http.DefaultClient
	}
	// The content hash is on the REDIRECT, not on the response we end up reading.
	//
	// huggingface.co answers with a 302 carrying X-Linked-Etag — the LFS object's
	// SHA-256, which is what check 3 above is describing — and points at a CDN
	// whose own response does not repeat it. Since Hugging Face moved to Xet
	// storage that CDN sets a plain Etag holding the xetHash, a DIFFERENT hash of
	// the same bytes that is also bare 64-hex and therefore also matches
	// etagSHA256RE. Reading only the final response meant hashing the content
	// with SHA-256 and comparing it to something that was never a SHA-256, so
	// every model download failed as a checksum mismatch — the restrictive
	// direction this file's opening comment says it will not fail in.
	//
	// Capturing it here rather than trusting the last hop is the fix: a redirect
	// request carries the response that caused it, which is the only place the
	// figure we can actually verify against appears.
	var linkedETag string
	client := *base // shallow copy: the caller's client must not grow our hook
	prior := base.CheckRedirect
	client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if linkedETag == "" && r.Response != nil {
			linkedETag = strings.TrimSpace(r.Response.Header.Get("X-Linked-Etag"))
		}
		if prior != nil {
			return prior(r, via)
		}
		// net/http's own default, restated because replacing CheckRedirect
		// replaces that default along with it.
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("download %s: %w", m.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return DownloadResult{}, fmt.Errorf("download %s: server said %s", m.Name, resp.Status)
	}

	tmp := final + partSuffix
	f, err := os.Create(tmp)
	if err != nil {
		return DownloadResult{}, err
	}
	// Any failure past this point must not leave a part-file lying around: a
	// retry would find it, and InstalledModels would have to keep guessing.
	cleanup := func() { f.Close(); os.Remove(tmp) }

	total := resp.ContentLength
	sha1sum := sha1.New()
	sha256sum := sha256.New()
	written, err := copyWithProgress(ctx, f, resp.Body, io.MultiWriter(sha1sum, sha256sum), total, progress)
	if err != nil {
		cleanup()
		return DownloadResult{}, fmt.Errorf("download %s: %w", m.Name, err)
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return DownloadResult{}, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return DownloadResult{}, err
	}

	res := DownloadResult{Path: final, Bytes: written, SHA1: hex.EncodeToString(sha1sum.Sum(nil))}
	verified, err := verifyTransfer(tmp, m, written, total, linkedETag, resp.Header, sha1sum, sha256sum)
	if err != nil {
		os.Remove(tmp)
		return DownloadResult{}, err
	}
	res.Verified = verified

	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return DownloadResult{}, err
	}
	return res, nil
}

// verifyTransfer runs the three checks and reports which one was strongest.
func verifyTransfer(path string, m Model, written, promised int64, linkedETag string, hdr http.Header, sha1sum, sha256sum hash.Hash) (string, error) {
	if written < minModelBytes {
		return "", fmt.Errorf("%w: %s is only %d bytes, which is not a model", ErrChecksum, m.Name, written)
	}
	if !looksLikeGGML(path) {
		return "", fmt.Errorf("%w: %s does not start with the ggml magic bytes — the server most likely "+
			"returned an error page rather than the model", ErrChecksum, m.Name)
	}
	if promised > 0 && written != promised {
		return "", fmt.Errorf("%w: %s is truncated — got %d bytes, the server promised %d",
			ErrChecksum, m.Name, written, promised)
	}
	if want, ok := checksumForTransfer(linkedETag, hdr); ok {
		if got := hex.EncodeToString(sha256sum.Sum(nil)); !strings.EqualFold(got, want) {
			return "", fmt.Errorf("%w: %s hashed to %s, the server said %s", ErrChecksum, m.Name, got, want)
		}
		return "sha256", nil
	}
	if m.SHA1 != "" {
		if got := hex.EncodeToString(sha1sum.Sum(nil)); !strings.EqualFold(got, m.SHA1) {
			return "", fmt.Errorf("%w: %s hashed to %s, expected %s", ErrChecksum, m.Name, got, m.SHA1)
		}
		return "sha1", nil
	}
	if promised > 0 {
		return "length", nil
	}
	// No Content-Length and no advertised hash. The magic bytes and the size
	// floor still passed, which is enough to reject the failures that actually
	// happen. Refusing here would break downloads behind chunked-encoding
	// proxies for no gain.
	return "magic", nil
}

// etagSHA256RE matches an ETag that is a bare SHA-256 hex digest, which is how
// Hugging Face's LFS storage identifies an object. A weak ETag (W/"...") or a
// short opaque one says nothing about the content and is ignored rather than
// being misread as a checksum.
var etagSHA256RE = regexp.MustCompile(`^"?([0-9a-fA-F]{64})"?$`)

// checksumForTransfer picks the strongest content hash the transfer offered,
// preferring the X-Linked-Etag carried on a redirect over anything the last hop
// said.
//
// The order matters and is not a stylistic preference. A CDN's own Etag
// describes the object however that CDN chooses to; Hugging Face's Xet storage
// puts a xetHash there, which is bare 64-hex and so indistinguishable by shape
// from the SHA-256 we are looking for. The redirect's X-Linked-Etag is the one
// Hugging Face documents as the LFS object's SHA-256, so when it is present it
// is the only figure worth comparing against.
func checksumForTransfer(linkedETag string, hdr http.Header) (string, bool) {
	if m := etagSHA256RE.FindStringSubmatch(strings.TrimSpace(linkedETag)); m != nil {
		return strings.ToLower(m[1]), true
	}
	return checksumFromHeaders(hdr)
}

// checksumFromHeaders extracts a content SHA-256 the server has advertised.
func checksumFromHeaders(h http.Header) (string, bool) {
	if h == nil {
		return "", false
	}
	for _, key := range []string{"X-Linked-Etag", "Etag"} {
		if m := etagSHA256RE.FindStringSubmatch(strings.TrimSpace(h.Get(key))); m != nil {
			return strings.ToLower(m[1]), true
		}
	}
	return "", false
}

// VerifyModelFile checks a model already on disk.
//
// This is the check applied before a model is handed to whisper, and before a
// re-download is skipped. It is deliberately weaker than the download-time
// check: at rest there is no Content-Length and no ETag to compare against, so
// it verifies the magic bytes, a floor, and the catalogue size within a wide
// tolerance. A model the catalogue does not know is checked for magic only —
// somebody's own fine-tune is not corrupt just because we have never heard of
// it.
func VerifyModelFile(path string, m Model) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if st.IsDir() {
		return fmt.Errorf("%s is a directory, not a model", path)
	}
	if st.Size() < minModelBytes {
		return fmt.Errorf("%w: %s is only %d bytes", ErrChecksum, filepath.Base(path), st.Size())
	}
	if !looksLikeGGML(path) {
		return fmt.Errorf("%w: %s does not start with the ggml magic bytes", ErrChecksum, filepath.Base(path))
	}
	if m.Bytes > 0 {
		// A tenth either way. The published sizes drift when upstream re-quantises
		// a model, and the purpose of this check is to catch a file that is half
		// there, not to pin a byte count.
		lo, hi := m.Bytes-m.Bytes/10, m.Bytes+m.Bytes/10
		if st.Size() < lo || st.Size() > hi {
			return fmt.Errorf("%w: %s is %d bytes, expected roughly %d — most likely an interrupted download",
				ErrChecksum, filepath.Base(path), st.Size(), m.Bytes)
		}
	}
	return nil
}

// looksLikeGGML reads the first four bytes and compares them to the magic.
// Unreadable is reported as false: we cannot vouch for a file we cannot open.
func looksLikeGGML(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var buf [4]byte
	if _, err := io.ReadFull(f, buf[:]); err != nil {
		return false
	}
	return string(buf[:]) == string(ggmlMagic)
}

// copyWithProgress streams src to dst and to the hashers, reporting progress
// and honouring cancellation between chunks.
func copyWithProgress(ctx context.Context, dst io.Writer, src io.Reader, hashes io.Writer, total int64, progress ProgressFunc) (int64, error) {
	const chunk = 512 << 10
	buf := make([]byte, chunk)
	var written int64
	var lastReport time.Time
	for {
		select {
		case <-ctx.Done():
			return written, ctx.Err()
		default:
		}
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return written, werr
			}
			if _, herr := hashes.Write(buf[:n]); herr != nil {
				return written, herr
			}
			written += int64(n)
			// Coalesced: a 3 GB download is six thousand chunks, and a UI does
			// not need six thousand updates.
			if progress != nil && time.Since(lastReport) > 250*time.Millisecond {
				lastReport = time.Now()
				progress(fractionOf(written, total), written, total)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return written, err
		}
	}
	if progress != nil {
		progress(fractionOf(written, total), written, total)
	}
	return written, nil
}

func fractionOf(written, total int64) float64 {
	if total <= 0 {
		return 0
	}
	f := float64(written) / float64(total)
	if f > 1 {
		return 1
	}
	return f
}
