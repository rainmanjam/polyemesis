package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestOnlyAnOpenStreamIsSharedCacheable drives the real gate: the open
// stream's segment is public and immutable, and a token holder's or an
// operator's answer for the same file is private, so a pull CDN in front of
// the origin can never store gated media. Cloudflare and most CDNs ignore
// Vary, so this header is the line that holds.
func TestOnlyAnOpenStreamIsSharedCacheable(t *testing.T) {
	cases := []struct {
		cfg    string
		who    string
		prefix string
	}{
		{"public open", "anonymous", "public, "},
		{"public open", "session", "private, "},
		{"public token", "query token", "private, "},
		{"public token", "cookie token", "private, "},
		{"public token", "session", "private, "},
		{"private", "session", "private, "},
	}
	principals := map[string]playoutPrincipal{}
	for _, p := range playoutPrincipals() {
		principals[p.name] = p
	}
	configs := map[string]playoutConfig{}
	for _, c := range playoutConfigs() {
		configs[c.name] = c
	}
	for _, tc := range cases {
		t.Run(tc.cfg+"/"+tc.who, func(t *testing.T) {
			c := configs[tc.cfg]
			s, h, sign := playoutOriginServer(t, c.set, c.pub)
			dir := filepath.Join(s.playoutManager().Dir(), "source")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "seg_1793548800000000.ts"), []byte("segment"), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"/playout/source/seg_1793548800000000.ts", "/playout/master.m3u8"} {
				r := httptest.NewRequest(http.MethodGet, path, nil)
				r.RemoteAddr = "203.0.113.9:5555"
				principals[tc.who].apply(t, h, sign, r)
				w := do(t, h, r)
				if w.Code != http.StatusOK {
					t.Fatalf("%s: %d", path, w.Code)
				}
				cc := w.Header().Get("Cache-Control")
				if strings.HasSuffix(path, ".ts") && !strings.HasPrefix(cc, tc.prefix) {
					t.Fatalf("%s as %s on %s: Cache-Control %q, want %q...", path, tc.who, tc.cfg, cc, tc.prefix)
				}
				if strings.HasSuffix(path, ".m3u8") && tc.prefix == "private, " && cc != "no-store" {
					t.Fatalf("gated playlist Cache-Control %q", cc)
				}
				if strings.HasPrefix(tc.prefix, "public") && strings.Contains(cc, "private") {
					t.Fatalf("open stream marked private: %q", cc)
				}
				if w.Header().Get("ETag") == "" {
					t.Fatalf("%s: no ETag", path)
				}
			}
		})
	}
}

// The poster follows the same rule as the media, and every refusal is
// no-store so a cache-eligible CDN rule cannot hold a 401 or a pre-publish 404.
func TestThePosterAndTheRefusalsFollowTheSharedCacheRule(t *testing.T) {
	cases := []struct {
		cfg, who, want string
		code           int
	}{
		{"public open", "anonymous", "public, max-age=10", http.StatusOK},
		{"public open", "session", "private, max-age=10", http.StatusOK},
		{"public token", "query token", "private, max-age=10", http.StatusOK},
		{"public token", "anonymous", "no-store", http.StatusNotFound},
		{"private", "anonymous", "no-store", http.StatusNotFound},
	}
	principals := map[string]playoutPrincipal{}
	for _, p := range playoutPrincipals() {
		principals[p.name] = p
	}
	configs := map[string]playoutConfig{}
	for _, c := range playoutConfigs() {
		configs[c.name] = c
	}
	for _, tc := range cases {
		t.Run(tc.cfg+"/"+tc.who, func(t *testing.T) {
			c := configs[tc.cfg]
			s, h, sign := playoutOriginServer(t, c.set, c.pub)
			st := s.playoutStore()
			st.mu.Lock()
			st.poster, st.posterErr, st.posterAt = []byte("a poster"), nil, time.Now()
			st.mu.Unlock()
			r := httptest.NewRequest(http.MethodGet, "/api/v1/playout/poster.jpg", nil)
			r.RemoteAddr = "203.0.113.9:5555"
			principals[tc.who].apply(t, h, sign, r)
			w := do(t, h, r)
			if w.Code != tc.code || w.Header().Get("Cache-Control") != tc.want {
				t.Fatalf("poster %d %q, want %d %q", w.Code, w.Header().Get("Cache-Control"), tc.code, tc.want)
			}
		})
	}
	// The media origin's refusals.
	for _, tc := range []struct{ cfg, who string }{{"public token", "anonymous"}, {"private", "anonymous"}} {
		c := configs[tc.cfg]
		_, h, sign := playoutOriginServer(t, c.set, c.pub)
		r := httptest.NewRequest(http.MethodGet, "/playout/master.m3u8", nil)
		r.RemoteAddr = "203.0.113.9:5555"
		principals[tc.who].apply(t, h, sign, r)
		w := do(t, h, r)
		if w.Code < 400 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s refusal %d Cache-Control %q", tc.cfg, w.Code, w.Header().Get("Cache-Control"))
		}
	}
}

// Codex #3: the public view follows the shared-cache rule -- briefly public
// for an open stream, private and not stored for a token holder or an
// operator, never stored when refused.
func TestThePublicViewFollowsTheSharedCacheRule(t *testing.T) {
	cases := []struct{ cfg, who, want string }{
		{"public open", "anonymous", "public, max-age=5"},
		{"public open", "session", "private, no-store"},
		{"public token", "query token", "private, no-store"},
		{"public token", "anonymous", "no-store"},
		{"private", "anonymous", "no-store"},
	}
	principals := map[string]playoutPrincipal{}
	for _, p := range playoutPrincipals() {
		principals[p.name] = p
	}
	configs := map[string]playoutConfig{}
	for _, c := range playoutConfigs() {
		configs[c.name] = c
	}
	for _, tc := range cases {
		t.Run(tc.cfg+"/"+tc.who, func(t *testing.T) {
			c := configs[tc.cfg]
			_, h, sign := playoutOriginServer(t, c.set, c.pub)
			r := httptest.NewRequest(http.MethodGet, "/api/v1/playout/public", nil)
			r.RemoteAddr = "203.0.113.9:5555"
			principals[tc.who].apply(t, h, sign, r)
			w := do(t, h, r)
			if got := w.Header().Get("Cache-Control"); got != tc.want {
				t.Fatalf("%d Cache-Control %q, want %q", w.Code, got, tc.want)
			}
		})
	}
}

// Codex #6: a poster that cannot be served after the gate let the request in
// (no frame on disk yet) is a 404 nothing may store.
func TestAPosterNotYetAvailableIsNoStore(t *testing.T) {
	c := playoutConfigs()[1] // public open
	_, h, _ := playoutOriginServer(t, c.set, c.pub)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/playout/poster.jpg", nil)
	r.RemoteAddr = "203.0.113.9:5555"
	w := do(t, h, r)
	if w.Code != http.StatusNotFound || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %q", w.Code, w.Header().Get("Cache-Control"))
	}
}
