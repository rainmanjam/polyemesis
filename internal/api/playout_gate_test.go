package api

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/db/dbtest"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/secrets"
)

// playoutOriginServer is the fixture the gate tests drive: a REAL router over a
// REAL engine.Manager with a REAL playout manager, and a master playlist
// actually on disk so /playout/master.m3u8 can answer 200.
//
// Every one of those "real"s was paid for by a vacuous test. A fixture with no
// engine answers 503 for every principal, a fixture with no file on disk
// answers 404 for every principal, and in both cases an assertion that a read
// token is refused passes against a build that refuses nobody. The point of
// this file is to make the difference between allowed and denied VISIBLE, which
// means the allowed case has to be a 200 with bytes in it.
func playoutOriginServer(t *testing.T, set db.PlayoutSettings, pub playoutPublish) (*Server, http.Handler, func(*http.Request)) {
	t.Helper()

	dir := t.TempDir()
	store := dbtest.OpenAt(t, filepath.Join(dir, "polyemesis.db"))
	if _, err := store.CreateUser("admin", testPassword); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	box, err := secrets.New([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatalf("secrets.New: %v", err)
	}
	cfg := config.Config{DataDir: dir}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}

	stored, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	stored.Playout = set
	// An SRT port this test owns; see renditionServer for why the default is
	// not safe to assume free.
	stored.Listeners.SRTPort = freeUDPPort(t)
	if err := store.PutSettings(stored); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}

	bus := events.NewBroker()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// A bogus FFmpeg path, as in renditionServer: the playout manager is built
	// and reconciled, and the muxer it would spawn fails to exec instead of
	// binding a real port from a unit test. The manifest is planted below, so
	// nothing depends on a child having produced one.
	eng := engine.NewManager(log, cfg, store, defaultTools(), bus)
	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("engine manager Start: %v", err)
	}
	t.Cleanup(eng.Stop)

	s := New(Options{
		Log: log, Config: cfg, DB: store, Secrets: box,
		Engine: eng, Events: bus, Version: "test",
	})
	h := s.Handler()
	lastTestServer = s

	if _, err := s.playoutStore().save(pub); err != nil {
		t.Fatalf("save publish config: %v", err)
	}

	m := s.playoutManager()
	if m == nil {
		t.Fatal("fixture has no playout manager; every media assertion below would be vacuous")
	}
	// The manager's own view of the settings is what the media handler reads,
	// and it is set by a reconcile rather than by the row. Doing it here rather
	// than trusting startup order means the fixture states the configuration it
	// is testing.
	if err := m.Reconcile(set, nil); err != nil {
		t.Fatalf("reconcile playout: %v", err)
	}
	if got := m.Settings().Enabled; got != set.Enabled {
		t.Fatalf("playout manager Enabled = %v after reconcile, want %v", got, set.Enabled)
	}

	if err := os.MkdirAll(m.Dir(), 0o755); err != nil {
		t.Fatalf("mkdir playout dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(m.Dir(), "master.m3u8"),
		[]byte(plantedMaster), 0o644); err != nil {
		t.Fatalf("plant master playlist: %v", err)
	}

	return s, h, login(t, h)
}

// plantedMaster is a real enough master playlist that a 200 carrying it is
// distinguishable from a 200 carrying nothing.
const plantedMaster = "#EXTM3U\n#EXT-X-VERSION:6\n" +
	"#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=1280x720\nsource/index.m3u8\n"

func TestPlayoutFixtureActuallyServesMedia(t *testing.T) {
	_, h, sign := playoutOriginServer(t, enabledPlayout(false),
		playoutPublish{Protection: PlayoutProtectToken, Token: testToken})

	r := httptest.NewRequest(http.MethodGet, "/playout/master.m3u8", nil)
	r.RemoteAddr = "203.0.113.9:5555"
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("signed-in operator got %d for master.m3u8; the fixture serves no media, "+
			"so every denial assertion in this file would pass against a build that denies nobody",
			w.Code)
	}
	if !strings.Contains(w.Body.String(), "EXT-X-STREAM-INF") {
		t.Fatalf("master.m3u8 body is not the planted playlist: %q", w.Body.String())
	}
}

// TestOnlyTwoFunctionsAuthenticate is the durability half of the playout gate,
// and it guards against the failure that produced the bug rather than against
// the bug.
//
// s.authenticate resolves a principal and says nothing about what that
// principal may do. Everywhere inside the authenticated groups, the answer to
// "may they" comes from requireScope, which runs as middleware immediately
// after requireAuth. /playout/* is outside those groups by necessity -- a
// viewer has no session -- so the scope check has to happen inside the handler,
// and the way the bug happened was that somebody called s.authenticate there,
// looked at the error, and threw the principal away. There is no test of
// behaviour that catches the NEXT route to do that, because the next route does
// not exist yet.
//
// So the invariant is structural: exactly two functions in this package resolve
// a principal, requireAuth (which hands it to requireScope) and playoutOperator
// (which consults the scope itself). A third one is not necessarily wrong, but
// it is a place where the scope can be dropped, and it should not appear
// without somebody reading this and deciding.
//
// Checked against the syntax tree, not against the source text (#107): this is
// a claim about which functions contain a call, and a substring search over
// playout.go would match the sentence above.
func TestOnlyTwoFunctionsAuthenticate(t *testing.T) {
	want := map[string]bool{"requireAuth": true, "playoutOperator": true}

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package api: %v", err)
	}
	pkg, ok := pkgs["api"]
	if !ok {
		t.Fatal("package api did not parse")
	}

	got := map[string]bool{}
	for _, file := range pkg.Files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "authenticate" {
					return true
				}
				if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "s" {
					return true
				}
				got[fn.Name.Name] = true
				return true
			})
		}
	}

	for name := range want {
		if !got[name] {
			t.Errorf("%s no longer calls s.authenticate; if the principal is now resolved "+
				"somewhere else, this guard has to move with it", name)
		}
	}
	for name := range got {
		if !want[name] {
			t.Errorf("%s calls s.authenticate. That is where the scope gets dropped: "+
				"authenticate answers WHO, and outside the authenticated route groups "+
				"nothing else answers WHETHER. Consult the principal's scope there (see "+
				"playoutOperator) and then add the function to this test's want set.", name)
		}
	}
}

// ---------------------------------------------------------------- the matrix

// playoutPrincipal is one way of arriving at the origin. The apply function
// takes the handler and the session signer because a bearer and a cookie both
// have to be minted against the instance under test.
type playoutPrincipal struct {
	name  string
	apply func(t *testing.T, h http.Handler, sign func(*http.Request), r *http.Request)
}

func playoutPrincipals() []playoutPrincipal {
	return []playoutPrincipal{
		{"anonymous", func(*testing.T, http.Handler, func(*http.Request), *http.Request) {}},
		{"query token", func(_ *testing.T, _ http.Handler, _ func(*http.Request), r *http.Request) {
			q := r.URL.Query()
			q.Set(playoutTokenParam, testToken)
			r.URL.RawQuery = q.Encode()
		}},
		{"cookie token", func(_ *testing.T, _ http.Handler, _ func(*http.Request), r *http.Request) {
			r.AddCookie(&http.Cookie{Name: playoutTokenCookie, Value: testToken})
		}},
		{"basic auth token", func(_ *testing.T, _ http.Handler, _ func(*http.Request), r *http.Request) {
			r.SetBasicAuth("viewer", testToken)
		}},
		{"read bearer", func(t *testing.T, h http.Handler, sign func(*http.Request), r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+
				createScopedToken(t, h, sign, "monitoring", db.ScopeRead))
		}},
		{"read bearer with query token", func(t *testing.T, h http.Handler, sign func(*http.Request), r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+
				createScopedToken(t, h, sign, "monitoring", db.ScopeRead))
			q := r.URL.Query()
			q.Set(playoutTokenParam, testToken)
			r.URL.RawQuery = q.Encode()
		}},
		{"admin bearer", func(t *testing.T, h http.Handler, sign func(*http.Request), r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+
				createScopedToken(t, h, sign, "deploy", db.ScopeAdmin))
		}},
		{"session", func(_ *testing.T, _ http.Handler, sign func(*http.Request), r *http.Request) {
			sign(r)
		}},
		{"garbage bearer", func(_ *testing.T, _ http.Handler, _ func(*http.Request), r *http.Request) {
			r.Header.Set("Authorization", "Bearer pmt_not-a-real-token-at-all")
		}},
	}
}

// playoutConfig is one stored configuration of the origin.
type playoutConfig struct {
	name string
	set  db.PlayoutSettings
	pub  playoutPublish
}

func playoutConfigs() []playoutConfig {
	return []playoutConfig{
		{"private", enabledPlayout(false),
			playoutPublish{Protection: PlayoutProtectToken, Token: testToken}},
		{"public open", enabledPlayout(true),
			playoutPublish{Protection: PlayoutProtectOpen, Token: testToken}},
		{"public token", enabledPlayout(true),
			playoutPublish{Protection: PlayoutProtectToken, Token: testToken}},
	}
}

// TestPlayoutGateMatrix is the whole access rule for the public origin, stated
// once, over the REAL router.
//
// Every cell is an exact status rather than a range, and the 200s matter as
// much as the 404s: they are the positive controls. A change that broke playout
// for everybody would satisfy any number of "a read token is refused"
// assertions on its own, and this table refuses to let it.
//
// The poster column is WEAK BY CONSTRUCTION and is labelled so rather than
// quietly relied on: the fixture has no segment to render a frame from, so an
// allowed poster 404s exactly like a denied one. It is here for the one thing
// it still says (a protected stream 404s the poster rather than firing a
// browser password prompt from an <img> tag on somebody's blog) and so that a
// future fixture which can render makes the column start doing real work. The
// poster's actual guard is TestPlayoutPosterVerdict.
func TestPlayoutGateMatrix(t *testing.T) {
	const (
		media  = "/playout/master.m3u8"
		public = "/api/v1/playout/public"
		poster = "/api/v1/playout/poster.jpg"
	)
	// want[config][principal][mount].
	want := map[string]map[string]map[string]int{
		// Enabled but NOT public: only the operator sees anything. The playback
		// token is not a key to an unpublished stream, so presenting it changes
		// nothing -- and neither does a read-scoped bearer, which is the row
		// this whole change exists for.
		"private": {
			"anonymous":                    {media: 404, public: 404, poster: 404},
			"query token":                  {media: 404, public: 404, poster: 404},
			"cookie token":                 {media: 404, public: 404, poster: 404},
			"basic auth token":             {media: 404, public: 404, poster: 404},
			"read bearer":                  {media: 404, public: 404, poster: 404},
			"read bearer with query token": {media: 404, public: 404, poster: 404},
			"admin bearer":                 {media: 200, public: 200, poster: 404},
			"session":                      {media: 200, public: 200, poster: 404},
			"garbage bearer":               {media: 404, public: 404, poster: 404},
		},
		// Published to everyone. Nobody is refused, including the read bearer,
		// which is the other half of the claim: the gate narrows a read token to
		// what an anonymous viewer gets; it does not demote it below that.
		"public open": {
			"anonymous":                    {media: 200, public: 200, poster: 404},
			"query token":                  {media: 200, public: 200, poster: 404},
			"cookie token":                 {media: 200, public: 200, poster: 404},
			"basic auth token":             {media: 200, public: 200, poster: 404},
			"read bearer":                  {media: 200, public: 200, poster: 404},
			"read bearer with query token": {media: 200, public: 200, poster: 404},
			"admin bearer":                 {media: 200, public: 200, poster: 404},
			"session":                      {media: 200, public: 200, poster: 404},
			"garbage bearer":               {media: 200, public: 200, poster: 404},
		},
		// Published behind the playback token. The read bearer WITHOUT the
		// playback token now gets the same 401 challenge an anonymous viewer
		// gets, where before this change it was waved through as the operator.
		// That is a deliberate behaviour change and the only one in the table
		// that costs anybody anything: a read token is a monitoring credential,
		// not a watch credential. It has zero deployed holders until this ships,
		// which is why the tightening is free today and would be a breaking
		// change after the tag. WITH the playback token it is served, because
		// the playback token is what the rule is about.
		"public token": {
			"anonymous":                    {media: 401, public: 401, poster: 404},
			"query token":                  {media: 200, public: 200, poster: 404},
			"cookie token":                 {media: 200, public: 200, poster: 404},
			"basic auth token":             {media: 200, public: 200, poster: 404},
			"read bearer":                  {media: 401, public: 401, poster: 404},
			"read bearer with query token": {media: 200, public: 200, poster: 404},
			"admin bearer":                 {media: 200, public: 200, poster: 404},
			"session":                      {media: 200, public: 200, poster: 404},
			"garbage bearer":               {media: 401, public: 401, poster: 404},
		},
	}

	for _, cfg := range playoutConfigs() {
		t.Run(cfg.name, func(t *testing.T) {
			_, h, sign := playoutOriginServer(t, cfg.set, cfg.pub)
			for _, p := range playoutPrincipals() {
				for _, mount := range []string{media, public, poster} {
					t.Run(p.name+" "+mount, func(t *testing.T) {
						r := httptest.NewRequest(http.MethodGet, mount, nil)
						r.RemoteAddr = "203.0.113.9:5555"
						p.apply(t, h, sign, r)
						w := do(t, h, r)
						if got := w.Code; got != want[cfg.name][p.name][mount] {
							t.Fatalf("GET %s as %s on a %s stream = %d, want %d (body %.120q)",
								mount, p.name, cfg.name, got,
								want[cfg.name][p.name][mount], w.Body.String())
						}
					})
				}
			}
		})
	}
}

// TestPlayoutMethodsOnTheOriginAreUnchanged pins the shape of the mount itself.
// The gate is a change to WHO, and must not turn into a change to WHAT.
func TestPlayoutMethodsOnTheOriginAreUnchanged(t *testing.T) {
	_, h, _ := playoutOriginServer(t, enabledPlayout(true),
		playoutPublish{Protection: PlayoutProtectOpen, Token: testToken})

	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		r := httptest.NewRequest(m, "/playout/master.m3u8", nil)
		r.RemoteAddr = "203.0.113.9:5555"
		if got := do(t, h, r).Code; got != http.StatusMethodNotAllowed {
			t.Errorf("%s /playout/master.m3u8 = %d, want 405", m, got)
		}
	}
	r := httptest.NewRequest(http.MethodOptions, "/playout/master.m3u8", nil)
	r.RemoteAddr = "203.0.113.9:5555"
	if got := do(t, h, r).Code; got != http.StatusNoContent {
		t.Errorf("OPTIONS /playout/master.m3u8 = %d, want 204", got)
	}
}

// TestPlayoutPosterVerdict is the poster's real guard, and it exists because
// the HTTP-level one cannot be.
//
// The poster handler is `if !s.authorizePlayout(r).ok() { 404 }` followed by a
// render that also 404s when there is no segment on disk -- which is the state
// of every fixture in this package, and of any fixture that does not run a real
// FFmpeg. So an HTTP assertion about the poster passes identically against a
// build that authorises nobody and a build that authorises everybody: the two
// paths are indistinguishable at the wire. That is the same pathology as a
// guard asserting over a zero-value fixture, and the answer is the same --
// assert where the difference is observable, which here is the verdict.
func TestPlayoutPosterVerdict(t *testing.T) {
	tests := []struct {
		cfg    string
		public bool
		prot   playoutProtection
	}{
		{"private token", false, PlayoutProtectToken},
		{"private open", false, PlayoutProtectOpen},
		{"public token", true, PlayoutProtectToken},
		{"public open", true, PlayoutProtectOpen},
	}
	for _, tt := range tests {
		t.Run(tt.cfg, func(t *testing.T) {
			s, h, sign := playoutOriginServer(t, enabledPlayout(tt.public),
				playoutPublish{Protection: tt.prot, Token: testToken})
			read := createScopedToken(t, h, sign, "monitoring", db.ScopeRead)
			admin := createScopedToken(t, h, sign, "deploy", db.ScopeAdmin)

			verdict := func(apply func(*http.Request)) playoutAccess {
				r := httptest.NewRequest(http.MethodGet, "/api/v1/playout/poster.jpg", nil)
				r.RemoteAddr = "203.0.113.9:5555"
				apply(r)
				return s.authorizePlayout(r)
			}

			anon := verdict(func(*http.Request) {})
			gotRead := verdict(func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer "+read)
			})
			// The claim, stated as an equality rather than as a list of
			// statuses: on the poster a read token is exactly a viewer.
			// Whatever the operator's configuration grants an anonymous caller
			// it grants the read token, and nothing more.
			if gotRead != anon {
				t.Fatalf("poster verdict for a read bearer = %v, anonymous = %v; "+
					"a read token must be neither promoted nor demoted relative to a viewer",
					gotRead, anon)
			}
			// Positive controls, so a build that denied everybody could not
			// satisfy the equality above.
			if got := verdict(func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer "+admin)
			}); got != playoutAllowAdmin {
				t.Fatalf("poster verdict for an admin bearer = %v, want playoutAllowAdmin", got)
			}
			if got := verdict(sign); got != playoutAllowAdmin {
				t.Fatalf("poster verdict for a session = %v, want playoutAllowAdmin", got)
			}
			// And the unpublished configurations must really be hiding
			// something, or the equality above is being satisfied by a stream
			// nobody is refused from.
			if !tt.public && anon != playoutDenyHidden {
				t.Fatalf("anonymous verdict on an unpublished stream = %v, want playoutDenyHidden", anon)
			}
		})
	}
}

// TestReadBearerIsByteIdenticalToAnonymousOnPlayout is the stronger form of the
// matrix: not merely the same status, but the same response.
//
// Byte-identity is the property, and it was chosen over a scope-shaped 403 on
// purpose. A 403 reading "this token is read-only" would tell the holder that
// there IS a stream at that path -- an existence oracle on a stream the operator
// has not published -- and would make the origin's response vary by principal,
// dragging a media path that currently escapes the Vary/no-store regime into it.
// Indistinguishable from a stranger is the stronger answer.
func TestReadBearerIsByteIdenticalToAnonymousOnPlayout(t *testing.T) {
	mounts := []string{"/playout/master.m3u8", "/api/v1/playout/public", "/api/v1/playout/poster.jpg"}

	for _, cfg := range playoutConfigs() {
		t.Run(cfg.name, func(t *testing.T) {
			_, h, sign := playoutOriginServer(t, cfg.set, cfg.pub)
			read := createScopedToken(t, h, sign, "monitoring", db.ScopeRead)

			for _, mount := range mounts {
				t.Run(mount, func(t *testing.T) {
					anon := httptest.NewRequest(http.MethodGet, mount, nil)
					anon.RemoteAddr = "203.0.113.9:5555"
					a := do(t, h, anon)

					withRead := httptest.NewRequest(http.MethodGet, mount, nil)
					withRead.RemoteAddr = "203.0.113.9:5555"
					withRead.Header.Set("Authorization", "Bearer "+read)
					b := do(t, h, withRead)

					if a.Code != b.Code {
						t.Fatalf("status: anonymous %d, read bearer %d", a.Code, b.Code)
					}
					if a.Body.String() != b.Body.String() {
						t.Fatalf("body differs:\n anonymous   = %q\n read bearer = %q",
							a.Body.String(), b.Body.String())
					}
					if diff := headerDiff(a.Header(), b.Header()); diff != "" {
						t.Fatalf("headers differ between anonymous and a read bearer: %s", diff)
					}
					// Named separately from the header diff above because it is
					// the property rather than an instance of it: presenting a
					// read token must not add a Vary to a response that had
					// none. The origin's cache story is that it does not vary by
					// principal, and this is what keeps that true.
					if a.Header().Get("Vary") != b.Header().Get("Vary") {
						t.Fatalf("Vary differs: anonymous %q, read bearer %q",
							a.Header().Get("Vary"), b.Header().Get("Vary"))
					}
				})
			}
		})
	}
}

// headerDiff reports every difference between two header sets as a printable
// string, or "" when they are equal.
func headerDiff(a, b http.Header) string {
	seen := map[string]bool{}
	var out []string
	for k := range a {
		seen[k] = true
		if strings.Join(a.Values(k), "|") != strings.Join(b.Values(k), "|") {
			out = append(out, k+": "+strings.Join(a.Values(k), "|")+" vs "+strings.Join(b.Values(k), "|"))
		}
	}
	for k := range b {
		if !seen[k] {
			out = append(out, k+": absent vs "+strings.Join(b.Values(k), "|"))
		}
	}
	sort.Strings(out)
	return strings.Join(out, "; ")
}

// TestPlayoutCookieHandoffSurvives pins the mechanism a real player depends on:
// one request carrying ?t=, and every later request carrying only the cookie
// that first request was handed.
//
// The second subtest is a REPAIR, not a regression guard. Pre-fix, a read bearer
// presenting a valid ?t= on a protected stream resolved to playoutAllowAdmin --
// the admin branch fired before the playback token was ever compared -- and
// playoutAllowAdmin issues no cookie. So that player authorised its first
// request and then had nothing to carry, and the breakage only showed up on
// request two. The assertion also rules out the alternative fixes that were
// considered: a middleware, or a hard deny for read bearers, would have made
// this cell a 401 or a 403 rather than a 200 with a Set-Cookie.
func TestPlayoutCookieHandoffSurvives(t *testing.T) {
	handoff := func(t *testing.T, h http.Handler, extra func(*http.Request)) *http.Cookie {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet,
			"/playout/master.m3u8?"+playoutTokenParam+"="+testToken, nil)
		r.RemoteAddr = "203.0.113.9:5555"
		if extra != nil {
			extra(r)
		}
		w := do(t, h, r)
		if w.Code != http.StatusOK {
			t.Fatalf("the request carrying ?%s= got %d, want 200", playoutTokenParam, w.Code)
		}
		for _, c := range w.Result().Cookies() {
			if c.Name == playoutTokenCookie {
				if c.Path != PlayoutPrefix {
					t.Fatalf("handoff cookie Path = %q, want %q", c.Path, PlayoutPrefix)
				}
				return c
			}
		}
		t.Fatalf("no %s cookie was issued; a player would have to keep re-appending the token",
			playoutTokenCookie)
		return nil
	}

	t.Run("anonymous viewer", func(t *testing.T) {
		_, h, _ := playoutOriginServer(t, enabledPlayout(true),
			playoutPublish{Protection: PlayoutProtectToken, Token: testToken})
		c := handoff(t, h, nil)

		next := httptest.NewRequest(http.MethodGet, "/playout/master.m3u8", nil)
		next.RemoteAddr = "203.0.113.9:5555"
		next.AddCookie(c)
		if got := do(t, h, next).Code; got != http.StatusOK {
			t.Fatalf("the follow-up request carrying only the cookie got %d, want 200", got)
		}
	})

	t.Run("read bearer presenting the playback token", func(t *testing.T) {
		_, h, sign := playoutOriginServer(t, enabledPlayout(true),
			playoutPublish{Protection: PlayoutProtectToken, Token: testToken})
		read := createScopedToken(t, h, sign, "monitoring", db.ScopeRead)
		c := handoff(t, h, func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+read)
		})

		next := httptest.NewRequest(http.MethodGet, "/playout/master.m3u8", nil)
		next.RemoteAddr = "203.0.113.9:5555"
		next.Header.Set("Authorization", "Bearer "+read)
		next.AddCookie(c)
		if got := do(t, h, next).Code; got != http.StatusOK {
			t.Fatalf("the follow-up request carrying the cookie and the read bearer got %d, want 200", got)
		}
	})
}
