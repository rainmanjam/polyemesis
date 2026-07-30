package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
)

// The HTTP-to-HTTPS redirect takes its destination from the Host header when no
// hostname is configured, which makes that destination attacker-controlled.
//
// A victim's browser cannot be made to send a forged Host to this box, so the
// direct steer is not the risk. The risk is a shared cache in front of the
// server storing a permanent redirect whose Location a stranger chose, and
// replaying it to the next client. These tests pin the properties that stop
// that, and each fails if the corresponding guard is removed.

func doRedirect(t *testing.T, hostname, sendHost, method string) *httptest.ResponseRecorder {
	t.Helper()
	cfg := config.Config{Addr: ":8443", TLS: config.TLS{Hostname: hostname}}
	req := httptest.NewRequest(method, "http://placeholder/some/path?a=1", nil)
	req.Host = sendHost
	w := httptest.NewRecorder()
	redirectToHTTPS(cfg)(w, req)
	return w
}

func TestConfiguredHostnameIgnoresTheHostHeaderEntirely(t *testing.T) {
	w := doRedirect(t, "box.example.com", "evil.attacker.test", http.MethodGet)

	if got, want := w.Header().Get("Location"), "https://box.example.com:8443/some/path?a=1"; got != want {
		t.Fatalf("Location = %q, want %q: a configured hostname must win over the "+
			"Host header, or the certificate would not match the destination anyway", got, want)
	}
	if w.Code != http.StatusMovedPermanently {
		t.Errorf("code = %d, want 301: a destination we chose is safe to cache", w.Code)
	}
	if cc := w.Header().Get("Cache-Control"); cc == "no-store" {
		t.Errorf("Cache-Control = %q; a fixed, configured destination is worth caching", cc)
	}
}

func TestAReflectedHostIsNeverCacheable(t *testing.T) {
	// The regression guard for the finding. With no hostname configured the
	// Location carries whatever the client sent, so no intermediary may be
	// allowed to store it against this URL and serve it to anybody else.
	w := doRedirect(t, "", "evil.attacker.test", http.MethodGet)

	if got, want := w.Header().Get("Location"), "https://evil.attacker.test:8443/some/path?a=1"; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}

	if w.Code != http.StatusFound {
		t.Errorf("code = %d, want 302: a redirect whose destination came from a "+
			"request header must not be a PERMANENT one, or a shared cache will "+
			"hand a stranger's Location to the next client", w.Code)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := w.Header().Get("Vary"); got != "Host" {
		t.Errorf("Vary = %q, want Host: the response differs by Host and a cache "+
			"has to be told so", got)
	}
}

func TestAReflectedHostStaysTemporaryForNonIdempotentMethods(t *testing.T) {
	// 308/307 preserve the method; the same caching argument applies, so the
	// non-GET path must pick 307 and not 308.
	w := doRedirect(t, "", "evil.attacker.test", http.MethodPost)

	if w.Code != http.StatusTemporaryRedirect {
		t.Fatalf("code = %d, want 307", w.Code)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

func TestAConfiguredHostnameKeepsThePermanentMethodPreservingCode(t *testing.T) {
	if w := doRedirect(t, "box.example.com", "x", http.MethodPost); w.Code != http.StatusPermanentRedirect {
		t.Fatalf("code = %d, want 308", w.Code)
	}
}

func TestAMalformedHostHeaderIsRefused(t *testing.T) {
	for _, bad := range []string{
		"evil.test/path",
		"evil.test\\@other",
		"evil test",
		"evil.test?x=1",
		"evil.test#frag",
		"evil.test%2f",
		"ev\til.test",
	} {
		t.Run(bad, func(t *testing.T) {
			w := doRedirect(t, "", bad, http.MethodGet)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("Host %q produced %d with Location %q, want 400",
					bad, w.Code, w.Header().Get("Location"))
			}
		})
	}
}

func TestOrdinaryHostsStillRedirect(t *testing.T) {
	// The positive case. A guard that refuses everything passes a table of bad
	// input just as happily as a correct one.
	for _, good := range []string{
		"192.168.1.50",
		"box.local",
		"polyemesis",
		"my-box.lan",
		"[::1]",
	} {
		t.Run(good, func(t *testing.T) {
			w := doRedirect(t, "", good, http.MethodGet)
			if w.Code != http.StatusFound {
				t.Fatalf("Host %q produced %d, want 302 — an operator who has not "+
					"set tls.hostname must still get a working redirect", good, w.Code)
			}
			if w.Header().Get("Location") == "" {
				t.Fatal("no Location header")
			}
		})
	}
}

func TestPlausibleHost(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"box.local", true},
		{"192.168.0.1", true},
		{"[fe80::1]:8443", true},
		{"a-b.c.d:443", true},
		{"", false},
		{"has space", false},
		{"has/slash", false},
		{"has\\backslash", false},
		{"has\nnewline", false},
		{"has\rcr", false},
		{"has\ttab", false},
		{"unicodé.test", false},
	} {
		if got := plausibleHost(tc.in); got != tc.want {
			t.Errorf("plausibleHost(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
