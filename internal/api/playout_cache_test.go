package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
