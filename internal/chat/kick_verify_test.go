package chat

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// One key for the whole package. RSA generation is the slowest thing in this
// test binary by an order of magnitude and the key is not what any test is
// varying, so it is generated once.
var (
	kickKeyOnce sync.Once
	kickTestKey *rsa.PrivateKey
)

func testKickKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	kickKeyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic("test key: " + err.Error())
		}
		kickTestKey = k
	})
	return kickTestKey
}

// signKick applies Kick's documented scheme to a request: the signature covers
// "{messageID}.{timestamp}.{body}", RSA-PKCS1v15 over SHA-256, base64 in the
// Kick-Event-Signature header.
//
// This helper deliberately reimplements the construction rather than calling
// into the code under test. A signer that shared VerifyKickSignature's idea of
// what gets signed would agree with it even if both were wrong, and the whole
// point of pinning the format is that it has to match a third party we cannot
// interrogate.
func signKick(t *testing.T, key *rsa.PrivateKey, req *http.Request, body []byte) {
	t.Helper()
	const (
		messageID = "01HZTEST0000000000000000"
		timestamp = "2026-03-01T12:00:00Z"
	)
	digest := sha256.Sum256([]byte(messageID + "." + timestamp + "." + string(body)))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	req.Header.Set(KickHeaderMessageID, messageID)
	req.Header.Set(KickHeaderTimestamp, timestamp)
	req.Header.Set(KickHeaderSignature, base64.StdEncoding.EncodeToString(sig))
}

func TestVerifyKickSignatureAcceptsAGenuineDelivery(t *testing.T) {
	key := testKickKey(t)
	body := []byte(`{"content":"hello"}`)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
	signKick(t, key, req, body)

	if err := VerifyKickSignature(&key.PublicKey, req, body); err != nil {
		t.Fatalf("a correctly signed delivery was rejected: %v", err)
	}
}

func TestVerifyKickSignatureRejects(t *testing.T) {
	key := testKickKey(t)
	other := func() *rsa.PrivateKey {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatalf("second key: %v", err)
		}
		return k
	}

	body := []byte(`{"content":"hello"}`)

	tests := []struct {
		name string
		// mutate runs after a valid signature is applied. Every case starts
		// from a delivery that WOULD verify, so a test that passes because the
		// request was never valid in the first place is not possible.
		mutate func(t *testing.T, req *http.Request) []byte
	}{
		{
			name: "a tampered body",
			mutate: func(_ *testing.T, _ *http.Request) []byte {
				// The forgery that matters: a valid signature from a real
				// delivery, replayed over different content.
				return []byte(`{"content":"you have been hacked"}`)
			},
		},
		{
			name: "a signature from the wrong key",
			mutate: func(t *testing.T, req *http.Request) []byte {
				signKick(t, other(), req, body)
				return body
			},
		},
		{
			name: "a missing signature header",
			mutate: func(_ *testing.T, req *http.Request) []byte {
				req.Header.Del(KickHeaderSignature)
				return body
			},
		},
		{
			name: "a missing message id",
			mutate: func(_ *testing.T, req *http.Request) []byte {
				req.Header.Del(KickHeaderMessageID)
				return body
			},
		},
		{
			name: "a missing timestamp",
			mutate: func(_ *testing.T, req *http.Request) []byte {
				req.Header.Del(KickHeaderTimestamp)
				return body
			},
		},
		{
			name: "a swapped message id, which the signature covers",
			mutate: func(_ *testing.T, req *http.Request) []byte {
				req.Header.Set(KickHeaderMessageID, "01HZDIFFERENT00000000000")
				return body
			},
		},
		{
			name: "a swapped timestamp, which the signature also covers",
			mutate: func(_ *testing.T, req *http.Request) []byte {
				req.Header.Set(KickHeaderTimestamp, "2026-03-01T13:00:00Z")
				return body
			},
		},
		{
			name: "a signature that is not base64",
			mutate: func(_ *testing.T, req *http.Request) []byte {
				req.Header.Set(KickHeaderSignature, "!!!not base64!!!")
				return body
			},
		},
		{
			name: "an empty signature",
			mutate: func(_ *testing.T, req *http.Request) []byte {
				req.Header.Set(KickHeaderSignature, "")
				return body
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
			signKick(t, key, req, body)

			// Sanity: valid before the mutation. Without this the table could
			// be silently testing nothing.
			if err := VerifyKickSignature(&key.PublicKey, req, body); err != nil {
				t.Fatalf("the fixture was invalid before the mutation: %v", err)
			}

			checked := tc.mutate(t, req)
			err := VerifyKickSignature(&key.PublicKey, req, checked)
			if err == nil {
				t.Fatal("accepted, want rejected")
			}
			if !errors.Is(err, ErrKickSignature) {
				t.Fatalf("error = %v, want it to wrap ErrKickSignature", err)
			}
		})
	}
}

func TestVerifyKickSignatureRefusesWithoutAKey(t *testing.T) {
	// A nil key is a verifier that was never configured. It must refuse, not
	// wave the delivery through — the whole finding this file exists to fix
	// was a missing verifier being read as "no verification needed".
	body := []byte(`{"content":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
	signKick(t, testKickKey(t), req, body)

	if err := VerifyKickSignature(nil, req, body); !errors.Is(err, ErrKickSignature) {
		t.Fatalf("a nil key returned %v, want ErrKickSignature", err)
	}
}

func TestParseKickPublicKey(t *testing.T) {
	key := testKickKey(t)
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	good := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})

	t.Run("a well formed key round trips", func(t *testing.T) {
		got, err := ParseKickPublicKey(good)
		if err != nil {
			t.Fatalf("ParseKickPublicKey: %v", err)
		}
		if got.N.Cmp(key.PublicKey.N) != 0 {
			t.Fatal("parsed a different modulus than was encoded")
		}
	})

	for _, tc := range []struct{ name, in string }{
		{"not PEM at all", "hello"},
		{"the empty string", ""},
		{"the wrong PEM type", string(pem.EncodeToMemory(&pem.Block{
			Type: "RSA PRIVATE KEY", Bytes: der,
		}))},
		{"a PEM body that is not a key", string(pem.EncodeToMemory(&pem.Block{
			Type: "PUBLIC KEY", Bytes: []byte("nonsense"),
		}))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseKickPublicKey([]byte(tc.in)); err == nil {
				t.Fatal("accepted, want an error")
			}
		})
	}
}

func TestKickKeyFetcherCachesAndFailsOpenToNobody(t *testing.T) {
	key := testKickKey(t)
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":{"public_key":%q}}`, pemKey)
	}))
	defer srv.Close()

	f := &KickKeyFetcher{HTTP: srv.Client(), URL: srv.URL}

	for i := 0; i < 3; i++ {
		got, err := f.Key(t.Context())
		if err != nil {
			t.Fatalf("Key: %v", err)
		}
		if got.N.Cmp(key.PublicKey.N) != 0 {
			t.Fatal("fetched a different key than was served")
		}
	}
	if hits != 1 {
		t.Fatalf("the key was fetched %d times, want 1: it is cached", hits)
	}
}

func TestKickKeyFetcherDoesNotCacheAFailure(t *testing.T) {
	// A momentary outage must not disable verification for the process
	// lifetime. The next delivery has to retry.
	key := testKickKey(t)
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		if hits == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, `{"data":{"public_key":%q}}`, pemKey)
	}))
	defer srv.Close()

	f := &KickKeyFetcher{HTTP: srv.Client(), URL: srv.URL}

	if _, err := f.Key(t.Context()); err == nil {
		t.Fatal("a 500 returned no error")
	}
	if _, err := f.Key(t.Context()); err != nil {
		t.Fatalf("the retry after a failed fetch also failed: %v", err)
	}
}

func TestKickVerifierRefusesWhenTheKeyCannotBeFetched(t *testing.T) {
	// "We could not check this delivery" must not resolve to "this delivery is
	// fine". That equivalence is precisely what left the check switched off.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	verify := KickVerifier(&KickKeyFetcher{HTTP: srv.Client(), URL: srv.URL})

	body := []byte(`{"content":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
	signKick(t, testKickKey(t), req, body)

	if err := verify(req, body); !errors.Is(err, ErrKickSignature) {
		t.Fatalf("an unfetchable key returned %v, want ErrKickSignature", err)
	}
}
