package playout

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// servedHarness is a manager with a populated playout directory and a handler
// mounted, which is the shape a request actually meets.
func servedHarness(t *testing.T, mutate func(*db.PlayoutSettings)) (*harness, http.Handler) {
	t.Helper()
	h := newHarness(t)
	s := baseSettings(db.PlayoutVariant{Name: "hd", Enabled: true})
	if mutate != nil {
		mutate(&s)
	}
	if err := h.Reconcile(s, h.resolve); err != nil {
		t.Fatal(err)
	}
	writeSegment(t, h.dir, "hd/"+MediaPlaylist, 32, 0)
	writeSegment(t, h.dir, "hd/"+DASHManifest, 32, 0)
	writeSegment(t, h.dir, "hd/seg_00000.ts", 64, 0)
	writeSegment(t, h.dir, "hd/init-0.m4s", 64, 0)
	writeSegment(t, h.dir, "hd/secrets.txt", 64, 0)
	// The live caption sidecar sits at the playout root, beside the ladder.
	writeSegment(t, h.dir, "captions.vtt", 64, 0)
	return h, h.Handler("/playout/")
}

func get(t *testing.T, h http.Handler, path, from string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.RemoteAddr = from + ":51234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestHandlerServesOnlyMediaAndManifestFiles(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		code  int
		ctype string
	}{
		{"the cross-variant ladder", "/playout/" + MasterPlaylist, http.StatusOK, "application/vnd.apple.mpegurl"},
		{"a media playlist", "/playout/hd/" + MediaPlaylist, http.StatusOK, "application/vnd.apple.mpegurl"},
		{"a dash manifest", "/playout/hd/" + DASHManifest, http.StatusOK, "application/dash+xml"},
		{"an mpeg-ts segment", "/playout/hd/seg_00000.ts", http.StatusOK, "video/mp2t"},
		{"a cmaf init segment", "/playout/hd/init-0.m4s", http.StatusOK, "video/iso.segment"},
		{"the live caption sidecar, served as WebVTT for players", "/playout/captions.vtt", http.StatusOK, "text/vtt"},
		{"anything else in the directory", "/playout/hd/secrets.txt", http.StatusNotFound, ""},
		{"a bare variant path is not a directory listing", "/playout/hd/", http.StatusNotFound, ""},
		{"the root is not a directory listing", "/playout/", http.StatusNotFound, ""},
	}
	_, handler := servedHarness(t, nil)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := get(t, handler, tc.path, "10.0.0.1")
			if w.Code != tc.code {
				t.Fatalf("status = %d, want %d", w.Code, tc.code)
			}
			if tc.ctype != "" && w.Header().Get("Content-Type") != tc.ctype {
				t.Fatalf("content type = %q, want %q", w.Header().Get("Content-Type"), tc.ctype)
			}
		})
	}
}

// TestTheOriginCachePolicy pins Cache-Control for every kind of file, for an
// open stream (shared caches allowed) and for everything else (a token, a
// session, or no decision at all -- which must fail closed to private).
func TestTheOriginCachePolicy(t *testing.T) {
	tests := []struct {
		name         string
		path         string
		open, closed string
	}{
		// The playlist moves every segment: one second in a CDN for an open
		// stream, never stored for a gated one.
		{"media playlist", "/playout/hd/" + MediaPlaylist, "public, max-age=1", "no-store"},
		{"dash manifest", "/playout/hd/" + DASHManifest, "public, max-age=1", "no-store"},
		// HLS segment names are numbered from the wall clock (args.go), so a
		// name is never reused: immutable.
		// never reused: a browser keeps it a year, a shared cache only for the
		// seekable window (here 4s x 6 segments) and never as immutable, so
		// withdrawing publication reaches a CDN edge within that window.
		{"hls segment", "/playout/hd/seg_00000.ts", "public, max-age=31536000, s-maxage=24", "private, max-age=31536000, immutable"},
		// DASH init and chunk names still restart with the muxer.
		{"dash init segment", "/playout/hd/init-0.m4s", "public, max-age=4", "private, max-age=4"},
		{"captions", "/playout/captions.vtt", "public, max-age=4", "private, max-age=4"},
	}
	_, handler := servedHarness(t, func(s *db.PlayoutSettings) { s.SegmentSeconds = 4; s.PlaylistSegments = 6; s.DVRWindowSeconds = 0 })
	for _, tc := range tests {
		for _, scope := range []struct {
			name string
			ctx  func(*http.Request) *http.Request
			want string
		}{
			{"open stream", func(r *http.Request) *http.Request { return r.WithContext(WithSharedCache(r.Context(), true)) }, tc.open},
			{"gated stream", func(r *http.Request) *http.Request { return r.WithContext(WithSharedCache(r.Context(), false)) }, tc.closed},
			{"no decision", func(r *http.Request) *http.Request { return r }, tc.closed},
		} {
			t.Run(tc.name+"/"+scope.name, func(t *testing.T) {
				r := scope.ctx(httptest.NewRequest(http.MethodGet, tc.path, nil))
				r.RemoteAddr = "10.0.0.1:1"
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code != http.StatusOK {
					t.Fatalf("status %d", w.Code)
				}
				if got := w.Header().Get("Cache-Control"); got != scope.want {
					t.Fatalf("cache-control = %q, want %q", got, scope.want)
				}
			})
		}
	}
}

// Every served file carries a strong validator, and a revalidation with it
// is a 304 with no body -- what lets a CDN or player refresh a playlist
// cheaply.
func TestServedFilesCarryAnETagAndRevalidateTo304(t *testing.T) {
	_, handler := servedHarness(t, nil)
	for _, p := range []string{"/playout/hd/seg_00000.ts", "/playout/hd/" + MediaPlaylist} {
		w := get(t, handler, p, "10.0.0.1")
		etag := w.Header().Get("ETag")
		if !strings.HasPrefix(etag, `"`) || len(etag) < 5 {
			t.Fatalf("%s: etag %q", p, etag)
		}
		r := httptest.NewRequest(http.MethodGet, p, nil)
		r.RemoteAddr = "10.0.0.1:1"
		r.Header.Set("If-None-Match", etag)
		w2 := httptest.NewRecorder()
		handler.ServeHTTP(w2, r)
		if w2.Code != http.StatusNotModified || w2.Body.Len() != 0 {
			t.Fatalf("%s: revalidation %d with %d bytes", p, w2.Code, w2.Body.Len())
		}
	}
}

func TestHandlerServesNothingWhilePlayoutIsDisabled(t *testing.T) {
	h, handler := servedHarness(t, nil)

	off := h.Settings()
	off.Enabled = false
	if err := h.Reconcile(off, h.resolve); err != nil {
		t.Fatal(err)
	}
	// The variant's files are still on disk until the next sweep; the handler
	// must not be what hands them out.
	writeSegment(t, h.dir, "hd/seg_00000.ts", 64, 0)

	if w := get(t, handler, "/playout/hd/seg_00000.ts", "10.0.0.1"); w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 while playout is off", w.Code)
	}
}

func TestHandlerRefusesToWriteAndToBeUsedAsAnEscapeHatch(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		code   int
	}{
		{"a post is not a viewer", http.MethodPost, "/playout/hd/" + MediaPlaylist, http.StatusMethodNotAllowed},
		{"a delete is not a viewer", http.MethodDelete, "/playout/hd/seg_00000.ts", http.StatusMethodNotAllowed},
		{"head is how a player sizes a segment", http.MethodHead, "/playout/hd/seg_00000.ts", http.StatusOK},
		{"traversal out of the playout root", http.MethodGet, "/playout/../secret.m3u8", http.StatusNotFound},
		{"encoded traversal out of the playout root", http.MethodGet, "/playout/hd/..%2f..%2fsecret.m3u8", http.StatusNotFound},
	}
	_, handler := servedHarness(t, nil)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			r.RemoteAddr = "10.0.0.1:1"
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.code {
				t.Fatalf("status = %d, want %d", w.Code, tc.code)
			}
		})
	}
}

func TestCrossOriginHeadersAreOnlySentWhenTheOperatorAsksForThem(t *testing.T) {
	tests := []struct {
		name  string
		allow bool
		want  string
	}{
		{"same-origin embedding needs no CORS", false, ""},
		{"a public origin exists to be embedded", true, "*"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, handler := servedHarness(t, func(s *db.PlayoutSettings) { s.AllowCrossOrigin = tc.allow })
			w := get(t, handler, "/playout/hd/"+MediaPlaylist, "10.0.0.1")
			if got := w.Header().Get("Access-Control-Allow-Origin"); got != tc.want {
				t.Fatalf("allow-origin = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPreflightIsAnsweredEvenWhilePlayoutIsOff(t *testing.T) {
	// 404ing a preflight surfaces in a browser as a CORS error, which reads as
	// a bug rather than as a disabled feature.
	h, handler := servedHarness(t, func(s *db.PlayoutSettings) { s.AllowCrossOrigin = true })
	off := h.Settings()
	off.Enabled = false
	if err := h.Reconcile(off, h.resolve); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodOptions, "/playout/hd/"+MediaPlaylist, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Origin") == "" {
		t.Fatal("a preflight with no allow-origin fails the check it exists to pass")
	}
}

func TestEveryServedRequestIsCountedAgainstTheViewerTable(t *testing.T) {
	tests := []struct {
		name     string
		requests []struct{ path, from string }
		viewers  int
		variant  string
	}{
		{"one player polling one rung is one viewer",
			[]struct{ path, from string }{
				{"/playout/hd/" + MediaPlaylist, "10.0.0.1"},
				{"/playout/hd/seg_00000.ts", "10.0.0.1"},
			}, 1, "hd"},
		{"two players are two viewers",
			[]struct{ path, from string }{
				{"/playout/hd/" + MediaPlaylist, "10.0.0.1"},
				{"/playout/hd/" + MediaPlaylist, "10.0.0.2"},
			}, 2, "hd"},
		{"a segment request alone still counts, which is what a dvr seek looks like",
			[]struct{ path, from string }{
				{"/playout/hd/seg_00000.ts", "10.0.0.9"},
			}, 1, "hd"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, handler := servedHarness(t, nil)
			for _, req := range tc.requests {
				get(t, handler, req.path, req.from)
			}
			a := h.Analytics()
			if a.Viewers != tc.viewers {
				t.Fatalf("viewers = %d, want %d", a.Viewers, tc.viewers)
			}
			if a.ByVariant[tc.variant] != tc.viewers {
				t.Fatalf("byVariant = %v, want %d on %q", a.ByVariant, tc.viewers, tc.variant)
			}
		})
	}
}

func TestTheLadderIsCountedUnderNoVariantSoAViewerChoosingIsStillVisible(t *testing.T) {
	h, handler := servedHarness(t, nil)
	get(t, handler, "/playout/"+MasterPlaylist, "10.0.0.1")

	a := h.Analytics()
	if a.Viewers != 1 {
		t.Fatalf("viewers = %d, want 1", a.Viewers)
	}
	if a.ByVariant[""] != 1 {
		t.Fatalf("byVariant = %v, want the ladder counted under the empty name", a.ByVariant)
	}
}

func TestARefusedRequestIsNotAViewer(t *testing.T) {
	h, handler := servedHarness(t, nil)
	get(t, handler, "/playout/hd/secrets.txt", "10.0.0.1")
	get(t, handler, "/playout/hd/", "10.0.0.2")

	if got := h.Analytics().Viewers; got != 0 {
		t.Fatalf("viewers = %d, want 0: nothing was served", got)
	}
}

func TestAViewerIsIdentifiedByAddressNotByConnection(t *testing.T) {
	h, handler := servedHarness(t, nil)
	for _, port := range []string{"1000", "1001", "1002"} {
		r := httptest.NewRequest(http.MethodGet, "/playout/hd/"+MediaPlaylist, nil)
		r.RemoteAddr = "10.0.0.1:" + port
		handler.ServeHTTP(httptest.NewRecorder(), r)
	}
	if got := h.Analytics().Viewers; got != 1 {
		t.Fatalf("viewers = %d, want 1: a player opens several connections", got)
	}
}

func TestAProxyAwareIdentityIsHonouredWhenTheCallerSuppliesOne(t *testing.T) {
	// The API decides what identifies a client, because only it knows whether a
	// proxy in front is trusted.
	h := newHarness(t)
	h.Manager.clientIP = func(r *http.Request) string {
		if v := r.Header.Get("X-Forwarded-For"); v != "" {
			return strings.TrimSpace(strings.Split(v, ",")[0])
		}
		return remoteIP(r)
	}
	if err := h.Reconcile(baseSettings(db.PlayoutVariant{Name: "hd", Enabled: true}), h.resolve); err != nil {
		t.Fatal(err)
	}
	writeSegment(t, h.dir, "hd/"+MediaPlaylist, 32, 0)
	handler := h.Handler("/playout/")

	for _, fwd := range []string{"203.0.113.5", "203.0.113.6", "203.0.113.5"} {
		r := httptest.NewRequest(http.MethodGet, "/playout/hd/"+MediaPlaylist, nil)
		r.RemoteAddr = "10.0.0.1:1234"
		r.Header.Set("X-Forwarded-For", fwd)
		handler.ServeHTTP(httptest.NewRecorder(), r)
	}

	if got := h.Analytics().Viewers; got != 2 {
		t.Fatalf("viewers = %d, want 2: every request shared one proxy address", got)
	}
}

func TestHandlerToleratesBeingMountedWithoutATrailingSlash(t *testing.T) {
	h, _ := servedHarness(t, nil)
	handler := h.Handler("/playout")
	if w := get(t, handler, "/playout/hd/"+MediaPlaylist, "10.0.0.1"); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

func TestASharedCacheIsBoundedByTheSeekableWindow(t *testing.T) {
	cases := []struct {
		s    db.PlayoutSettings
		want int
	}{
		{db.PlayoutSettings{SegmentSeconds: 4, PlaylistSegments: 6}, 24},
		{db.PlayoutSettings{SegmentSeconds: 4, PlaylistSegments: 6, DVRWindowSeconds: 3600}, 3600},
		{db.PlayoutSettings{SegmentSeconds: 2}, 2},
	}
	for _, tc := range cases {
		if got := sharedWindow(tc.s); got != tc.want {
			t.Errorf("sharedWindow(%+v) = %d, want %d", tc.s, got, tc.want)
		}
		cc := cacheControl(".ts", maxSegmentAge(tc.s), sharedWindow(tc.s), true)
		if strings.Contains(cc, "immutable") || !strings.Contains(cc, "s-maxage="+strconv.Itoa(tc.want)) {
			t.Errorf("shared segment %q", cc)
		}
	}
}

// Review #18: every refusal from the media handler is no-store -- a missing
// file, an unserved type, playout switched off.
func TestEveryRefusalFromTheMediaHandlerIsNoStore(t *testing.T) {
	h, handler := servedHarness(t, nil)
	for _, p := range []string{"/playout/hd/seg_99999.ts", "/playout/hd/secrets.txt", "/playout/nothing/index.m3u8"} {
		w := get(t, handler, p, "10.0.0.1")
		if w.Code != http.StatusNotFound || w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: %d %q", p, w.Code, w.Header().Get("Cache-Control"))
		}
	}
	off := h.Settings()
	off.Enabled = false
	if err := h.Reconcile(off, h.resolve); err != nil {
		t.Fatal(err)
	}
	if w := get(t, handler, "/playout/hd/seg_00000.ts", "10.0.0.1"); w.Code != http.StatusNotFound || w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("disabled: %d %q", w.Code, w.Header().Get("Cache-Control"))
	}
}

// Review #19: a playlist rewritten to the same length within one mtime tick
// gets a new ETag, so a revalidation is never answered 304 for changed text.
func TestAPlaylistETagFollowsItsContentNotItsMtime(t *testing.T) {
	h, handler := servedHarness(t, nil)
	path := filepath.Join(h.dir, "hd", MediaPlaylist)
	at := time.Unix(1_800_000_000, 0)
	write := func(body string) string {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(path, at, at)
		return get(t, handler, "/playout/hd/"+MediaPlaylist, "10.0.0.1").Header().Get("ETag")
	}
	a := write("#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:1\n")
	b := write("#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:2\n")
	if a == "" || a == b {
		t.Fatalf("same-length, same-mtime rewrite kept ETag %q", a)
	}
}

// Sonar S2083/S6549: nothing outside the playout directory is ever read,
// hashed or served -- not by "..", not by an encoded traversal, not by a
// leading slash, not through a symlink that points out -- and every such
// request is the same 404 with no-store as a file that does not exist.
func TestNothingOutsideThePlayoutDirectoryIsReadOrDisclosed(t *testing.T) {
	h, handler := servedHarness(t, nil)
	outside := filepath.Join(filepath.Dir(h.dir), "outside-secret.m3u8")
	if err := os.WriteFile(outside, []byte("#EXTM3U\n# outside the root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(h.dir, "hd", "link.m3u8")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		"/playout/../outside-secret.m3u8",
		"/playout/hd/../../outside-secret.m3u8",
		"/playout/hd/..%2f..%2foutside-secret.m3u8",
		"/playout/%2e%2e/outside-secret.m3u8",
		"/playout//" + strings.TrimPrefix(outside, "/"),
		"/playout/hd/link.m3u8",
	} {
		w := get(t, handler, p, "10.0.0.1")
		if w.Code != http.StatusNotFound || w.Header().Get("Cache-Control") != "no-store" ||
			w.Header().Get("ETag") != "" || strings.Contains(w.Body.String(), "outside the root") {
			t.Errorf("%s: %d cc=%q etag=%q body=%q", p, w.Code, w.Header().Get("Cache-Control"), w.Header().Get("ETag"), w.Body.String())
		}
	}
	// And a real file inside still serves, with Range.
	r := httptest.NewRequest(http.MethodGet, "/playout/hd/seg_00000.ts", nil)
	r.RemoteAddr = "10.0.0.1:1"
	r.Header.Set("Range", "bytes=0-9")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusPartialContent || w.Body.Len() != 10 {
		t.Fatalf("range inside the root: %d %d bytes", w.Code, w.Body.Len())
	}
}
