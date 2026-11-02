package playout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
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
			notFound(w, r)
			return
		}

		rel := strings.TrimPrefix(path.Clean("/"+strings.TrimPrefix(r.URL.Path, prefix)), "/")
		ext := strings.ToLower(path.Ext(rel))
		ctype, ok := servedTypes[ext]
		if !ok {
			notFound(w, r)
			return
		}
		// ONE confined open, used for everything after it. The file is opened
		// through an os.Root on the playout directory, which refuses a path
		// that leaves it -- "..", an absolute path, a symlink pointing out --
		// and the same open handle is what is stat'ed, hashed for the ETag and
		// served. Any failure, missing or refused alike, is the same 404 with
		// no-store, so the answer says nothing about what exists outside.
		f, fi, ok := m.openServed(rel)
		if !ok {
			notFound(w, r)
			return
		}
		defer f.Close()

		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Cache-Control", cacheControl(ext, maxSegmentAge(s), sharedWindow(s), SharedCacheable(r.Context())))
		if !m.setETag(w, f, ext, fi) {
			notFound(w, r)
			return
		}

		m.observe(r, rel, ext)
		// ServeContent, not FileServer: it serves the handle already opened
		// and confined above, and still answers Range and If-None-Match.
		http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
	})
}

// openServed opens rel under the playout directory through an os.Root, so it
// cannot resolve outside it, and returns it only if it is a regular file.
func (m *Manager) openServed(rel string) (*os.File, os.FileInfo, bool) {
	root, err := os.OpenRoot(m.dir)
	if err != nil {
		return nil, nil, false
	}
	defer root.Close()
	f, err := root.Open(filepath.FromSlash(rel))
	if err != nil {
		return nil, nil, false
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, nil, false
	}
	return f, fi, true
}

// sharedCacheKey carries the origin's access decision down from the API's
// gate, which is the only layer that knows it.
type sharedCacheKey struct{}

// WithSharedCache marks a request whose response may be stored by a shared
// cache (a CDN): the stream is published with no token, so every viewer gets
// the same bytes. Absent means no -- the direction that fails closed: a
// caller that forgets to set it gets private responses, never a gated
// segment in a CDN.
func WithSharedCache(ctx context.Context, shared bool) context.Context {
	return context.WithValue(ctx, sharedCacheKey{}, shared)
}

// SharedCacheable reports what WithSharedCache put on ctx.
func SharedCacheable(ctx context.Context) bool {
	v, _ := ctx.Value(sharedCacheKey{}).(bool)
	return v
}

// immutableAge is a year, the conventional ceiling for an immutable asset.
const immutableAge = 365 * 24 * 3600

// sharedWindow bounds how long a shared cache may keep an open stream's
// segment: the seekable window (the DVR window, or the live playlist), and
// never less than one segment. A segment older than that is listed by no
// playlist, so nothing legitimate asks for it -- and after the operator
// unpublishes or turns protection on, this is the longest a CDN edge can go on
// serving what it already holds.
func sharedWindow(s db.PlayoutSettings) int {
	seg := maxSegmentAge(s)
	w := seg * s.PlaylistSegments
	if s.DVRWindowSeconds > w {
		w = s.DVRWindowSeconds
	}
	if w < seg {
		w = seg
	}
	return w
}

// cacheControl is the origin's whole caching policy, in one place.
//
//   - Playlists change every segment. An open stream's playlist may be held
//     for one second, which collapses a CDN's viewers onto one origin fetch per
//     second per POP; a gated stream's is never stored.
//   - HLS .ts segments are numbered from the wall clock (args.go), so a name is
//     never reused and the bytes behind it never change. A browser may keep one
//     for a year. A SHARED cache may keep it only for the seekable window
//     (s-maxage) and it is not marked immutable there: publication can be
//     withdrawn, and an edge must stop serving within that window when it is.
//   - DASH chunks, init segments and captions still restart their names when
//     a muxer does, so they keep the one-segment cache they always had.
//
// shared=false (a token, a session, or no decision at all) makes every
// cacheable answer private: a browser may keep it, a CDN must not.
func cacheControl(ext string, segmentAge, window int, shared bool) string {
	switch {
	case manifestExts[ext]:
		if shared {
			return "public, max-age=1"
		}
		return "no-store"
	case ext == ".ts":
		if shared {
			return "public, max-age=" + strconv.Itoa(immutableAge) + ", s-maxage=" + strconv.Itoa(window)
		}
		return "private, max-age=" + strconv.Itoa(immutableAge) + ", immutable"
	case shared:
		return "public, max-age=" + strconv.Itoa(segmentAge)
	default:
		return "private, max-age=" + strconv.Itoa(segmentAge)
	}
}

// setETag gives every served file a strong validator from its size and
// modification time, set before http.FileServer runs so that its
// conditional-request handling answers If-None-Match with a 304.
//
// A playlist's validator is a hash of its CONTENT: it is rewritten in place
// every segment, often to the same length, and a filesystem whose mtime is
// coarser than the rewrite would otherwise hand a 304 for a changed playlist.
// Segments are written once under a name never reused, so size and mtime are
// exact for them.
func (m *Manager) setETag(w http.ResponseWriter, f *os.File, ext string, fi os.FileInfo) bool {
	if manifestExts[ext] {
		// The already-confined handle, read from its start, then rewound for
		// the serve that follows.
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return false
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return false
		}
		w.Header().Set("ETag", `"`+hex.EncodeToString(h.Sum(nil)[:16])+`"`)
		return true
	}
	w.Header().Set("ETag", `"`+strconv.FormatInt(fi.Size(), 16)+"-"+strconv.FormatInt(fi.ModTime().UnixNano(), 16)+`"`)
	return true
}

// notFound is every refusal this handler gives: never stored by anything.
func notFound(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	http.NotFound(w, r)
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
