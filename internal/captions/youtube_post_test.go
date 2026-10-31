package captions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// The ingestion URL is a signed credential, not a public address. These tests
// use a fake ingest, and the signature in the fake URL is assembled at runtime
// so no literal secret-shaped value is checked in.

// fakeIngest records every POST and answers with the status it is given.
type fakeIngest struct {
	mu     sync.Mutex
	status int
	bodies []string
	seqs   []string
	auth   []string
}

func (f *fakeIngest) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	buf := new(strings.Builder)
	b := make([]byte, 1024)
	for {
		n, err := r.Body.Read(b)
		buf.Write(b[:n])
		if err != nil {
			break
		}
	}
	f.bodies = append(f.bodies, buf.String())
	f.seqs = append(f.seqs, r.URL.Query().Get("seq"))
	f.auth = append(f.auth, r.URL.Query().Get("sig"))
	w.WriteHeader(f.status)
}

// signedURL builds an ingestion URL carrying a signature that is never a
// literal in this file.
func signedURL(base string, sig string) string {
	return base + "/closedcaption?cid=" + "ingest-" + "id" + "&sig=" + sig
}

// TestPostCaptionsSendsEachBatchWithItsSequenceNumber pins the wire shape that
// does not depend on the unverified body format: one POST per batch, carrying
// its sequence number as a query parameter, against the given ingestion URL.
func TestPostCaptionsSendsEachBatchWithItsSequenceNumber(t *testing.T) {
	fake := &fakeIngest{status: http.StatusOK}
	srv := httptest.NewTLSServer(fake)
	defer srv.Close()

	c := NewYouTubePoster(srv.Client(), signedURL(srv.URL, "fixture-sig-1"))
	for seq := 1; seq <= 2; seq++ {
		if err := c.Post(context.Background(), seq, "HELLO"); err != nil {
			t.Fatalf("post %d: %v", seq, err)
		}
	}
	if len(fake.seqs) != 2 || fake.seqs[0] != "1" || fake.seqs[1] != "2" {
		t.Fatalf("sequence numbers = %v, want [1 2]", fake.seqs)
	}
	if fake.auth[0] != "fixture-sig-1" {
		t.Fatalf("the signed URL's query was not kept: sig = %q", fake.auth[0])
	}
}

// TestPostCaptionsErrorsNeverCarryTheIngestionURL is the leak rule. The signed
// URL is a credential, and a failed POST returns an error that a log line will
// print, so the URL and its signature must not be in it, including the wrapped
// transport error the HTTP client builds from the URL.
func TestPostCaptionsErrorsNeverCarryTheIngestionURL(t *testing.T) {
	fake := &fakeIngest{status: http.StatusForbidden}
	srv := httptest.NewTLSServer(fake)
	defer srv.Close()

	const sig = "fixture-sig-secret"
	c := NewYouTubePoster(srv.Client(), signedURL(srv.URL, sig))
	err := c.Post(context.Background(), 1, "HELLO")
	if err == nil {
		t.Fatalf("a 403 from the ingest was not reported")
	}
	if strings.Contains(err.Error(), sig) || strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("error leaks the ingestion URL: %v", err)
	}

	// And a transport failure, where the client's own error holds the URL.
	dead := NewYouTubePoster(&http.Client{}, signedURL("https://127.0.0.1:1", sig))
	err = dead.Post(context.Background(), 2, "HELLO")
	if err == nil {
		t.Fatalf("a refused connection was not reported")
	}
	if strings.Contains(err.Error(), sig) {
		t.Fatalf("transport error leaks the signature: %v", err)
	}
}

// TestPostCaptionsRefusesANonHTTPSIngestionURL. The ingest is HTTPS, and a plain
// HTTP URL would send the signature in the clear.
func TestPostCaptionsRefusesANonHTTPSIngestionURL(t *testing.T) {
	c := NewYouTubePoster(&http.Client{}, "http://ingest.example.invalid/closedcaption?sig=x")
	if err := c.Post(context.Background(), 1, "HELLO"); err == nil {
		t.Fatalf("a plain HTTP ingestion URL was accepted")
	}
}
