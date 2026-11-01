package captions

import (
	"context"
	"errors"
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

	c := fixtureHostPoster(srv.Client(), signedURL(srv.URL, "fixture-sig-1"))
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
	c := fixtureHostPoster(srv.Client(), signedURL(srv.URL, sig))
	err := c.Post(context.Background(), 1, "HELLO")
	if err == nil {
		t.Fatalf("a 403 from the ingest was not reported")
	}
	if strings.Contains(err.Error(), sig) || strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("error leaks the ingestion URL: %v", err)
	}

	// And a transport failure, where the client's own error holds the URL.
	dead := fixtureHostPoster(&http.Client{}, signedURL("https://127.0.0.1:1", sig))
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

// fixtureHostPoster returns a poster whose host check is relaxed to the loopback
// test server. Production posters use the Google host allowlist, and the
// allowlist tests below build the poster without the relaxation.
func fixtureHostPoster(client *http.Client, ingestionURL string) *YouTubePoster {
	p := NewYouTubePoster(client, ingestionURL)
	p.allowHost = func(string) bool { return true }
	return p
}

// TestPostCaptionsRefusesAHostOutsideTheIngestAllowlist pins the rule that a
// signed caption URL is only ever sent to a YouTube host. The URL comes from an
// API response, so a poisoned or mistyped value must not turn the poster into a
// request to an arbitrary server carrying the signature.
func TestPostCaptionsRefusesAHostOutsideTheIngestAllowlist(t *testing.T) {
	for _, u := range []string{
		"https://attacker.example.invalid/closedcaption?cid=x&sig=y",
		"https://upload.youtube.com.attacker.example.invalid/closedcaption?cid=x&sig=y",
		"https://127.0.0.1:1/closedcaption?cid=x&sig=y",
	} {
		// The transport counts requests, so a refusal is proven by the request never
		// leaving, not by a network error that would happen anyway.
		var sent int
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			sent++
			return nil, errors.New("request must not be sent")
		})}
		c := NewYouTubePoster(client, u)
		err := c.Post(context.Background(), 1, "HELLO")
		if err == nil {
			t.Fatalf("a host outside the allowlist was accepted: %s", u)
		}
		if sent != 0 {
			t.Fatalf("a request left for a host outside the allowlist: %s", u)
		}
		if strings.Contains(err.Error(), "sig=y") {
			t.Fatalf("refusal leaks the signature: %v", err)
		}
	}
}

// roundTripFunc adapts a function to http.RoundTripper for transport-level tests.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestPostCaptionsAcceptsTheYouTubeIngestHost is the other half of the allowlist:
// the YouTube ingest host passes the host check, and a look-alike does not. It
// asserts on the check itself, so no request leaves the test.
func TestPostCaptionsAcceptsTheYouTubeIngestHost(t *testing.T) {
	for _, host := range []string{"upload.youtube.com", "a.upload.youtube.com"} {
		if !ingestHostAllowed(host) {
			t.Fatalf("documented ingest host %q is refused", host)
		}
	}
	if ingestHostAllowed("youtube.com.evil.example.invalid") {
		t.Fatalf("a look-alike host ending in youtube.com was allowed")
	}
}

// TestPostCaptionsRefusesEveryRedirect. A redirect from the ingest would carry the
// signed query to wherever the Location points, so the poster refuses the
// redirect rather than following it.
func TestPostCaptionsRefusesEveryRedirect(t *testing.T) {
	var hits int
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	front := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/elsewhere", http.StatusTemporaryRedirect)
	}))
	defer front.Close()

	c := fixtureHostPoster(front.Client(), signedURL(front.URL, "fixture-sig-redirect"))
	if err := c.Post(context.Background(), 1, "HELLO"); err == nil {
		t.Fatalf("a redirect was followed instead of refused")
	}
	if hits != 0 {
		t.Fatalf("the redirect target was contacted %d times", hits)
	}
}
