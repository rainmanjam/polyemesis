package playout

import (
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// servedTypes is the closed set of files playout will hand out, mapped to the
// content type each needs.
//
// Closed on purpose. The playout directory is written by FFmpeg and served,
// potentially anonymously, to the internet; anything not on this list is a file
// that has no business being fetched, and answering 404 for it is cheaper to
// reason about than deciding case by case. It also removes the directory
// listing http.FileServer would otherwise produce for a bare variant path,
// which would enumerate the DVR window to anyone who asked.
var servedTypes = map[string]string{
	".m3u8": "application/vnd.apple.mpegurl",
	".mpd":  "application/dash+xml",
	".ts":   "video/mp2t",
	".m4s":  "video/iso.segment",
	".mp4":  "video/mp4",
	".m4v":  "video/mp4",
	".m4a":  "audio/mp4",
	// The live caption sidecar the engine writes to <playoutDir>/captions.vtt.
	// Deliberately NOT advertised as an HLS SUBTITLES rendition: a single
	// growing VTT has no X-TIMESTAMP-MAP and no segmentation, so it is a file a
	// player can fetch, not a conformant rendition. Safe to serve from here —
	// the sweeper only deletes segment extensions and clearVariantDir only
	// touches variant subdirectories.
	".vtt": "text/vtt",
}

// manifestExts are the files that are rewritten in place every segment.
var manifestExts = map[string]bool{".m3u8": true, ".mpd": true}

// Handler serves the playout directory, counting every request against the
// viewer table.
//
// prefix is the URL path the handler is mounted at, e.g. "/playout/". Auth is
// the caller's decision: the API consults AllowAnonymous to decide whether a
// session is required, because "public origin" is a setting and route tables
// are built once at startup.
func (m *Manager) Handler(prefix string) http.Handler {
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	fs := http.FileServer(http.Dir(m.dir))
	stripped := http.StripPrefix(prefix, fs)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := m.Settings()
		if s.AllowCrossOrigin {
			setCORS(w)
		}
		if r.Method == http.MethodOptions {
			// Answered unconditionally HERE, and that is no longer the whole
			// story: this handler is not reached at all unless the caller has
			// already passed the API's configuration gate.
			//
			// This comment used to say "answered even when playout is off",
			// justified by a CORS error reading as a bug rather than as a
			// disabled feature. That justification was for a browser embedding a
			// stream the operator DID publish -- and it was being applied to a
			// server with playout switched off, where there is nothing to embed
			// and the 204 (plus, with AllowCrossOrigin, the CORS headers set
			// above this branch, above the Enabled check) disclosed the server's
			// configuration to anyone who asked. See
			// api.(*Server).playoutPreflightAllowed, which now answers 404 for
			// that case, matching what GET already did.
			//
			// The unconditional answer is kept here because by this point the
			// only remaining reasons to refuse would be credential ones, and a
			// preflight carries no credentials. A caller reaching this line has
			// already been established as looking at an ENABLED and PUBLISHED
			// stream, or as the operator previewing a private one.
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD, OPTIONS")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !s.Enabled {
			http.NotFound(w, r)
			return
		}

		rel := strings.TrimPrefix(path.Clean("/"+strings.TrimPrefix(r.URL.Path, prefix)), "/")
		ext := strings.ToLower(path.Ext(rel))
		ctype, ok := servedTypes[ext]
		if !ok {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", ctype)
		if manifestExts[ext] {
			// The playlist is rewritten every segment; a cached one freezes the
			// stream at whatever window the viewer first fetched.
			w.Header().Set("Cache-Control", "no-store")
		} else {
			// Segments are immutable while they exist, but their names restart
			// at zero when a muxer does, so a long cache would serve one run's
			// bytes under the next run's URL. One segment of caching absorbs a
			// player's retry burst and expires well before a restart matters.
			w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(maxSegmentAge(s)))
		}

		m.observe(r, rel, ext)
		stripped.ServeHTTP(w, r)
	})
}

// observe records the request against the viewer table.
//
// Both playlists and segments count, unlike the dashboard preview where only
// playlist polls do. A viewer seeking inside a DVR window pulls segments off a
// manifest they already hold, and treating that as idle would report an empty
// stream while people were watching it.
func (m *Manager) observe(r *http.Request, rel, ext string) {
	kind := RequestSegment
	if manifestExts[ext] {
		kind = RequestPlaylist
	}
	// The master playlist has no variant of its own; it is counted under the
	// empty name so a viewer who has loaded the ladder but not yet chosen a
	// rung is still visible.
	name := ""
	if i := strings.IndexByte(rel, '/'); i > 0 {
		name = rel[:i]
	}
	m.sessions.Observe(m.clientIP(r), name, kind, m.now())
}

// maxSegmentAge is one segment, floored at a second so a sub-second
// configuration cannot produce a max-age of zero, which some caches read as
// "cache forever" rather than "do not cache".
func maxSegmentAge(s db.PlayoutSettings) int {
	if s.SegmentSeconds < 1 {
		return 1
	}
	return s.SegmentSeconds
}

func setCORS(w http.ResponseWriter) {
	h := w.Header()
	// A public origin exists to be embedded, and a player on another site
	// cannot read a response it is not allowed to. Only ever set when the
	// operator turned AllowCrossOrigin on, and only on this handler: the API
	// and the UI are untouched by it.
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	// Range is what a player uses to seek inside a segment, and Content-Length
	// is what it needs to size the buffer.
	h.Set("Access-Control-Allow-Headers", "Range")
	h.Set("Access-Control-Expose-Headers", "Content-Length, Content-Range")
}

// remoteIP is the default viewer identity: the peer address with its port
// stripped, so a client opening a second connection is still one viewer.
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
