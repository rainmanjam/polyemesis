package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// playoutTestServer returns a server whose stored settings carry the given
// playout configuration, plus the publish config already on disk.
func playoutTestServer(t *testing.T, set db.PlayoutSettings, pub playoutPublish) (*Server, http.Handler) {
	t.Helper()

	dir := t.TempDir()
	s, h, store := testServer(t, config.Config{DataDir: dir})

	stored, err := store.GetSettings()
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	stored.Playout = set
	if err := store.PutSettings(stored); err != nil {
		t.Fatalf("put settings: %v", err)
	}

	if _, err := s.playoutStore().save(pub); err != nil {
		t.Fatalf("save publish config: %v", err)
	}
	return s, h
}

// enabledPlayout is a minimally valid enabled configuration.
func enabledPlayout(public bool) db.PlayoutSettings {
	return db.PlayoutSettings{
		Enabled:            true,
		Public:             public,
		Format:             db.PlayoutHLS,
		SegmentSeconds:     4,
		PlaylistSegments:   6,
		MaxDiskMB:          1024,
		AudioKbps:          128,
		SessionIdleSeconds: 30,
		MaxSessions:        1000,
		Variants:           []db.PlayoutVariant{{Name: "source", Enabled: true}},
	}
}

const testToken = "s3cret-playback-token"

// TestPlayoutAccessDecision pins who may fetch playout media, and how each
// refusal is phrased. This is the only unauthenticated media path in the
// product, so every row here is a security claim.
func TestPlayoutAccessDecision(t *testing.T) {
	tests := []struct {
		name string
		set  db.PlayoutSettings
		pub  playoutPublish
		// mutate presents a credential on the request.
		mutate func(*http.Request)
		want   playoutAccess
	}{
		{
			name:   "playout disabled hides the stream from anonymous callers",
			set:    db.PlayoutSettings{Enabled: false, Public: true},
			pub:    playoutPublish{Protection: PlayoutProtectOpen, Token: testToken},
			want:   playoutDenyHidden,
			mutate: func(*http.Request) {},
		},
		{
			name:   "enabled but not public hides the stream rather than challenging",
			set:    enabledPlayout(false),
			pub:    playoutPublish{Protection: PlayoutProtectOpen, Token: testToken},
			want:   playoutDenyHidden,
			mutate: func(*http.Request) {},
		},
		{
			name:   "public and protected challenges an anonymous caller",
			set:    enabledPlayout(true),
			pub:    playoutPublish{Protection: PlayoutProtectToken, Token: testToken},
			want:   playoutDenyChallenge,
			mutate: func(*http.Request) {},
		},
		{
			name:   "public and protected refuses the wrong token",
			set:    enabledPlayout(true),
			pub:    playoutPublish{Protection: PlayoutProtectToken, Token: testToken},
			want:   playoutDenyChallenge,
			mutate: func(r *http.Request) { r.URL.RawQuery = "t=not-the-token" },
		},
		{
			name:   "query token is accepted",
			set:    enabledPlayout(true),
			pub:    playoutPublish{Protection: PlayoutProtectToken, Token: testToken},
			want:   playoutAllowToken,
			mutate: func(r *http.Request) { r.URL.RawQuery = "t=" + testToken },
		},
		{
			name: "header token is accepted",
			set:  enabledPlayout(true),
			pub:  playoutPublish{Protection: PlayoutProtectToken, Token: testToken},
			want: playoutAllowToken,
			mutate: func(r *http.Request) {
				r.Header.Set(playoutTokenHeader, testToken)
			},
		},
		{
			name: "cookie token is accepted, which is what carries a player past its first request",
			set:  enabledPlayout(true),
			pub:  playoutPublish{Protection: PlayoutProtectToken, Token: testToken},
			want: playoutAllowToken,
			mutate: func(r *http.Request) {
				r.AddCookie(&http.Cookie{Name: playoutTokenCookie, Value: testToken})
			},
		},
		{
			name: "basic auth password is accepted whatever the username",
			set:  enabledPlayout(true),
			pub:  playoutPublish{Protection: PlayoutProtectToken, Token: testToken},
			want: playoutAllowToken,
			mutate: func(r *http.Request) {
				r.SetBasicAuth("anything", testToken)
			},
		},
		{
			name:   "an open stream serves an anonymous caller",
			set:    enabledPlayout(true),
			pub:    playoutPublish{Protection: PlayoutProtectOpen, Token: testToken},
			want:   playoutAllowOpen,
			mutate: func(*http.Request) {},
		},
		{
			name: "an empty configured token never matches, even against an empty presented one",
			set:  enabledPlayout(true),
			pub:  playoutPublish{Protection: PlayoutProtectToken, Token: ""},
			want: playoutDenyChallenge,
			mutate: func(r *http.Request) {
				r.Header.Set(playoutTokenHeader, "")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// normalize would mint a token for the empty-token row, which is
			// the one case that has to reach the check with nothing set.
			pub := tt.pub
			s, _ := playoutTestServer(t, tt.set, pub)
			if pub.Token == "" {
				st := s.playoutStore()
				st.mu.Lock()
				st.cfg.Token = ""
				st.mu.Unlock()
			}

			r := httptest.NewRequest(http.MethodGet, "/playout/master.m3u8", nil)
			r.RemoteAddr = "203.0.113.9:5555"
			tt.mutate(r)

			if got := s.authorizePlayout(r); got != tt.want {
				t.Fatalf("authorizePlayout = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPlayoutAdminAlwaysWatches pins that a signed-in operator can preview the
// stream before it is ever made public — otherwise the Playout page could not
// show what it is about to publish.
func TestPlayoutAdminAlwaysWatches(t *testing.T) {
	s, h := playoutTestServer(t, enabledPlayout(false),
		playoutPublish{Protection: PlayoutProtectToken, Token: testToken})

	sign := login(t, h)
	r := httptest.NewRequest(http.MethodGet, "/playout/master.m3u8", nil)
	r.RemoteAddr = "203.0.113.9:5555"
	sign(r)

	if got := s.authorizePlayout(r); got != playoutAllowAdmin {
		t.Fatalf("authorizePlayout for admin = %v, want playoutAllowAdmin", got)
	}
}

// TestPlayoutRoutesAreUnauthenticated pins that the viewer-facing routes are
// reachable without a session — the property the whole feature rests on, and
// the one a stray requireAuth would silently break.
func TestPlayoutRoutesAreUnauthenticated(t *testing.T) {
	_, h := playoutTestServer(t, enabledPlayout(true),
		playoutPublish{Protection: PlayoutProtectOpen, Token: testToken})

	tests := []struct {
		name    string
		path    string
		notWant int
	}{
		{"public metadata", "/api/v1/playout/public", http.StatusUnauthorized},
		{"poster", "/api/v1/playout/poster.jpg", http.StatusUnauthorized},
		{"media", "/playout/master.m3u8", http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tt.path, nil)
			r.RemoteAddr = "203.0.113.9:5555"
			if got := do(t, h, r).Code; got == tt.notWant {
				t.Fatalf("%s answered %d for an anonymous caller; the public routes must sit outside the authenticated group", tt.path, got)
			}
		})
	}
}

// TestPlayoutPublicRefusesWithoutToken pins that a protected stream tells an
// anonymous caller nothing at all: not the title, not whether it is live.
func TestPlayoutPublicRefusesWithoutToken(t *testing.T) {
	_, h := playoutTestServer(t, enabledPlayout(true),
		playoutPublish{
			Protection:  PlayoutProtectToken,
			Token:       testToken,
			Title:       "Secret broadcast",
			Description: "Nobody should read this",
		})

	r := httptest.NewRequest(http.MethodGet, "/api/v1/playout/public", nil)
	r.RemoteAddr = "203.0.113.9:5555"
	w := do(t, h, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if strings.Contains(w.Body.String(), "Secret broadcast") {
		t.Fatalf("refusal leaked the stream title: %s", w.Body.String())
	}
}

// TestPlayoutMediaChallengeNamesBasic pins that a browser pointed straight at a
// protected playlist gets a password prompt rather than a dead end.
func TestPlayoutMediaChallengeNamesBasic(t *testing.T) {
	_, h := playoutTestServer(t, enabledPlayout(true),
		playoutPublish{Protection: PlayoutProtectToken, Token: testToken})

	r := httptest.NewRequest(http.MethodGet, "/playout/master.m3u8", nil)
	r.RemoteAddr = "203.0.113.9:5555"
	w := do(t, h, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if got := w.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, "Basic ") {
		t.Fatalf("WWW-Authenticate = %q, want a Basic challenge", got)
	}
}

// TestPlayoutTokenCookieIsIssued pins the exchange that makes protection usable:
// one request proves the token, and every relative segment URL after it is
// authorised by the cookie that request set.
func TestPlayoutTokenCookieIsIssued(t *testing.T) {
	_, h := playoutTestServer(t, enabledPlayout(true),
		playoutPublish{Protection: PlayoutProtectToken, Token: testToken})

	r := httptest.NewRequest(http.MethodGet, "/api/v1/playout/public?t="+testToken, nil)
	r.RemoteAddr = "203.0.113.9:5555"
	w := do(t, h, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var found *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == playoutTokenCookie {
			found = c
		}
	}
	if found == nil {
		t.Fatal("no playout cookie was set; every segment after the first would be refused")
	}
	if found.Value != testToken {
		t.Fatalf("cookie value = %q, want the playback token", found.Value)
	}
	if found.Path != PlayoutPrefix {
		t.Fatalf("cookie path = %q, want it scoped to %q so the admin API never sees it", found.Path, PlayoutPrefix)
	}
	if !found.HttpOnly {
		t.Fatal("playout cookie must be HttpOnly")
	}
}

// TestPlayoutPublicViewOmitsToken pins that the viewer-facing payload never
// carries the secret, so a page shared by screenshot does not share access.
func TestPlayoutPublicViewOmitsToken(t *testing.T) {
	_, h := playoutTestServer(t, enabledPlayout(true),
		playoutPublish{Protection: PlayoutProtectToken, Token: testToken, Title: "Live"})

	r := httptest.NewRequest(http.MethodGet, "/api/v1/playout/public?t="+testToken, nil)
	r.RemoteAddr = "203.0.113.9:5555"
	w := do(t, h, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if strings.Contains(w.Body.String(), testToken) {
		t.Fatalf("public payload leaked the playback token: %s", w.Body.String())
	}
}

// TestPlayoutPublishDefaultsProtected pins the default an operator arrives at by
// leaving everything alone.
func TestPlayoutPublishDefaultsProtected(t *testing.T) {
	dir := t.TempDir()
	s, _, _ := testServer(t, config.Config{DataDir: dir})

	cfg := s.playoutStore().load()
	if cfg.Protection != PlayoutProtectToken {
		t.Fatalf("default protection = %q, want %q", cfg.Protection, PlayoutProtectToken)
	}
	if cfg.Token == "" {
		t.Fatal("default config has no playback token, which would leave protection unsatisfiable")
	}

	// Seeded to disk, so a restart does not invalidate every link handed out.
	b, err := os.ReadFile(filepath.Join(dir, "playout.json"))
	if err != nil {
		t.Fatalf("read seeded config: %v", err)
	}
	var onDisk playoutPublish
	if err := json.Unmarshal(b, &onDisk); err != nil {
		t.Fatalf("unmarshal seeded config: %v", err)
	}
	if onDisk.Token != cfg.Token {
		t.Fatalf("token on disk = %q, want the one in memory", onDisk.Token)
	}
}

// TestPlayoutPublishNormalizeFailsClosed pins that an unrecognised protection
// value — a hand-edited file, a downgrade — becomes the protected one rather
// than being treated as open.
func TestPlayoutPublishNormalizeFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		in   playoutProtection
		want playoutProtection
	}{
		{"empty", "", PlayoutProtectToken},
		{"unknown", "public-please", PlayoutProtectToken},
		{"token stays token", PlayoutProtectToken, PlayoutProtectToken},
		{"open is honoured", PlayoutProtectOpen, PlayoutProtectOpen},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := playoutPublish{Protection: tt.in, Token: testToken}
			p.normalize()
			if p.Protection != tt.want {
				t.Fatalf("protection = %q, want %q", p.Protection, tt.want)
			}
		})
	}
}

// TestPlayoutPublishRejectsUnknownProtection pins that the admin endpoint will
// not store a value it does not understand.
func TestPlayoutPublishRejectsUnknownProtection(t *testing.T) {
	_, h := playoutTestServer(t, enabledPlayout(false),
		playoutPublish{Protection: PlayoutProtectToken, Token: testToken})
	sign := login(t, h)

	r := jsonRequest(t, http.MethodPut, "/api/v1/playout/publish",
		map[string]string{"protection": "everyone"})
	sign(r)
	if got := do(t, h, r).Code; got != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", got)
	}
}

// TestPlayoutTokenRotationInvalidatesOldLinks pins the revocation mechanism.
func TestPlayoutTokenRotationInvalidatesOldLinks(t *testing.T) {
	s, h := playoutTestServer(t, enabledPlayout(true),
		playoutPublish{Protection: PlayoutProtectToken, Token: testToken})
	sign := login(t, h)

	r := jsonRequest(t, http.MethodPost, "/api/v1/playout/token", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	var got struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Token == "" || got.Token == testToken {
		t.Fatalf("rotated token = %q, want a new one", got.Token)
	}

	old := httptest.NewRequest(http.MethodGet, "/playout/master.m3u8?t="+testToken, nil)
	old.RemoteAddr = "203.0.113.9:5555"
	if access := s.authorizePlayout(old); access.ok() {
		t.Fatal("the previous token still works after rotation")
	}
}

// TestPlayoutAdminViewReportsExposure pins the flag the UI leads with. Getting
// this wrong in the safe direction is a scary banner; getting it wrong in the
// other direction is a stream silently on the internet.
func TestPlayoutAdminViewReportsExposure(t *testing.T) {
	tests := []struct {
		name string
		set  db.PlayoutSettings
		pub  playoutPublish
		want bool
	}{
		{"disabled is not exposed", db.PlayoutSettings{Enabled: false, Public: true},
			playoutPublish{Protection: PlayoutProtectOpen, Token: testToken}, false},
		{"private is not exposed", enabledPlayout(false),
			playoutPublish{Protection: PlayoutProtectOpen, Token: testToken}, false},
		{"public but token-protected is not exposed", enabledPlayout(true),
			playoutPublish{Protection: PlayoutProtectToken, Token: testToken}, false},
		{"public and open is exposed", enabledPlayout(true),
			playoutPublish{Protection: PlayoutProtectOpen, Token: testToken}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, h := playoutTestServer(t, tt.set, tt.pub)
			sign := login(t, h)

			r := httptest.NewRequest(http.MethodGet, "/api/v1/playout", nil)
			r.RemoteAddr = "203.0.113.9:5555"
			sign(r)
			w := do(t, h, r)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
			}

			var view playoutAdminView
			if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if view.Exposed != tt.want {
				t.Fatalf("exposed = %v, want %v", view.Exposed, tt.want)
			}
		})
	}
}

// TestPlayoutURLsCarryTokenOnlyWhenProtected pins that the copyable link works
// as handed out, and that an open stream's link does not train the operator to
// paste a secret around for no reason.
func TestPlayoutURLsCarryTokenOnlyWhenProtected(t *testing.T) {
	tests := []struct {
		name      string
		set       db.PlayoutSettings
		pub       playoutPublish
		wantToken bool
	}{
		{"public and protected", enabledPlayout(true),
			playoutPublish{Protection: PlayoutProtectToken, Token: testToken}, true},
		{"public and open", enabledPlayout(true),
			playoutPublish{Protection: PlayoutProtectOpen, Token: testToken}, false},
		{"private", enabledPlayout(false),
			playoutPublish{Protection: PlayoutProtectToken, Token: testToken}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/v1/playout", nil)
			r.Host = "stream.example.com"
			urls := playoutURLsFor(r, tt.set, tt.pub)

			if got := strings.Contains(urls.Master, testToken); got != tt.wantToken {
				t.Fatalf("master URL %q carries the token = %v, want %v", urls.Master, got, tt.wantToken)
			}
			if got := strings.Contains(urls.Watch, testToken); got != tt.wantToken {
				t.Fatalf("watch URL %q carries the token = %v, want %v", urls.Watch, got, tt.wantToken)
			}
			if !strings.Contains(urls.Embed, urls.Watch) {
				t.Fatalf("embed snippet %q does not point at the watch URL %q", urls.Embed, urls.Watch)
			}
			if !strings.HasPrefix(urls.Master, "http://stream.example.com/") {
				t.Fatalf("master URL %q is not absolute against the request host", urls.Master)
			}
		})
	}
}

// TestWatchCSPAllowsFraming pins that the derived policy differs from the admin
// one in exactly one directive. An embed is the point of the page, and
// frame-ancestors 'none' would silently blank it on every site it is put on.
func TestWatchCSPAllowsFraming(t *testing.T) {
	if !strings.Contains(watchCSP, "frame-ancestors *") {
		t.Fatalf("watch CSP does not permit framing: %s", watchCSP)
	}
	if strings.Contains(watchCSP, "frame-ancestors 'none'") {
		t.Fatalf("watch CSP still blocks framing: %s", watchCSP)
	}
	// Everything else must survive; a directive added to cspDirectives that
	// went missing here would be a hole nobody notices.
	for _, d := range cspDirectives {
		if strings.HasPrefix(d, "frame-ancestors") {
			continue
		}
		if !strings.Contains(watchCSP, d) {
			t.Fatalf("watch CSP dropped %q: %s", d, watchCSP)
		}
	}
}

// TestNewestPlayoutSegmentSkipsTheOpenOne pins that the poster is taken from a
// segment FFmpeg has finished writing, not the one it is mid-append on.
func TestNewestPlayoutSegmentSkipsTheOpenOne(t *testing.T) {
	dir := t.TempDir()
	variant := filepath.Join(dir, "source")
	if err := os.MkdirAll(variant, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	write := func(name string) string {
		p := filepath.Join(variant, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}

	if got := newestPlayoutSegment(dir); got != "" {
		t.Fatalf("empty directory returned %q, want no segment", got)
	}

	only := write("seg_00001.ts")
	if got := newestPlayoutSegment(dir); got != only {
		t.Fatalf("single segment = %q, want %q", got, only)
	}

	// Non-TS files are never candidates: a lone DASH .m4s cannot be decoded
	// without its init segment.
	write("manifest.mpd")
	write("chunk-0-00002.m4s")
	second := write("seg_00002.ts")

	got := newestPlayoutSegment(dir)
	if got == second {
		t.Fatalf("picked the newest segment %q, which FFmpeg is probably still writing", got)
	}
	if got != only {
		t.Fatalf("segment = %q, want the completed %q", got, only)
	}
}
