// Package web embeds the built React application and serves it.
//
// The UI is compiled by Vite into internal/web/dist and baked into the binary
// with go:embed, which is what makes polyemesis a single self-contained file
// with no static-asset directory to deploy alongside it.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var embedded embed.FS

// FS returns the built UI as a filesystem rooted at the asset directory.
func FS() (fs.FS, error) {
	return fs.Sub(embedded, "dist")
}

// Built reports whether a real UI was embedded.
//
// In a clean checkout the embedded directory holds only a tracked .gitkeep --
// present so that go:embed has something to match and `go build ./...` works
// before `npm run build` has ever run. No index.html means no UI, and main
// warns rather than serving a blank page.
func Built() bool {
	b, err := embedded.ReadFile("dist/index.html")
	if err != nil {
		return false
	}
	return len(b) > 0
}

// Handler serves the SPA: hashed assets with a long cache, everything else
// falling back to index.html so client-side routes survive a page reload.
func Handler() (http.Handler, error) {
	sub, err := FS()
	if err != nil {
		return nil, err
	}
	return HandlerFor(sub), nil
}

// HandlerFor is Handler over an ARBITRARY asset filesystem, and it exists so
// that the branches below can be driven in both of the configurations this
// binary ships in.
//
// #167: CI's Go job does not run `npm run build`, so `dist` holds only
// .gitkeep and every request that reaches this handler takes the
// "UI not built" branch. The route ledger's NotFound probes were reported as
// covering this surface while, in every configuration they are actually run in,
// eight of nine took one dead branch and the asset branch was entered by
// nothing. Splitting the filesystem out of the closure costs nothing at run
// time -- Handler passes the embedded one, exactly as before -- and lets a test
// hand in a populated one.
//
// The split itself changed no behaviour -- the body is the one that was inside
// Handler. What the seam then made visible, and what IS a behaviour change, is
// the directory case below: it could not be driven at all while the only
// filesystem this handler would accept was an empty one.
// isRegularFile reports whether p names something in sub that is a file rather
// than a directory. A path that does not exist is neither.
func isRegularFile(sub fs.FS, p string) bool {
	f, err := sub.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	return err == nil && !st.IsDir()
}

// staticAssetExts are the extensions a request for a FILE ends in. A request
// for one of these is a browser, a crawler or a tool fetching a resource by
// name -- never a person navigating to a page -- so a miss is a 404 and not the
// SPA.
//
// Deliberately absent: .html and .htm, which name documents and are what the
// SPA fallback is for.
var staticAssetExts = map[string]bool{
	".ico": true, ".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".webp": true, ".avif": true, ".svg": true, ".bmp": true,
	".css": true, ".js": true, ".mjs": true, ".cjs": true, ".map": true,
	".json": true, ".txt": true, ".xml": true, ".webmanifest": true,
	".woff": true, ".woff2": true, ".ttf": true, ".otf": true, ".eot": true,
	".wasm": true, ".pdf": true,
	".mp4": true, ".webm": true, ".mp3": true, ".wav": true, ".m3u8": true, ".ts": true,
}

// isStaticAssetPath reports whether p names a file rather than a UI route.
func isStaticAssetPath(p string) bool {
	return staticAssetExts[strings.ToLower(path.Ext(p))]
}

func HandlerFor(sub fs.FS) http.Handler {
	files := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}

		// A DIRECTORY IS NOT AN ASSET, and this is the reason isRegularFile
		// exists rather than a bare Open. Opening a directory SUCCEEDS, and
		// handing it to http.FileServer answers 200 with an index of its
		// contents: GET /assets/ returned the whole bundle inventory as a page
		// of links to any anonymous caller. Nobody is entitled to that listing
		// and no client asks for it -- the SPA references bundles by their
		// fingerprinted names -- so it is a gratuitous disclosure of build
		// layout. A directory now falls through to the SPA branch, which is how
		// every other unknown path is answered.
		if isRegularFile(sub, p) {
			// Vite fingerprints everything under /assets, so those are safe to
			// cache forever. index.html must never be, or a deploy leaves users
			// on the old bundle.
			if strings.HasPrefix(p, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			files.ServeHTTP(w, r)
			return
		}

		// A request under /api that got this far matched no API route, and the
		// SPA fallback is the wrong answer for it. Serving index.html there
		// returns 200 and a page of HTML to a caller that asked for JSON: a
		// client checking `res.ok` sees success, `JSON.parse` fails on the
		// leading '<' with an error naming neither the endpoint nor the status,
		// and a mistyped, removed or wrongly-versioned endpoint is
		// indistinguishable from a working one. /api/v2/anything answered 200
		// with the UI. Fail as JSON, with the status the caller expects.
		if p == "api" || strings.HasPrefix(p, "api/") {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no such endpoint"}` + "\n"))
			return
		}

		// A MISSING FILE IS NOT A DEEP LINK, and answering one with the SPA is
		// the same mistake the /api branch above exists to correct, made
		// against a different kind of caller.
		//
		// GET /favicon.ico returned 200 with index.html. The browser asked for
		// an image, got a page of HTML, could not decode it, and fell back to
		// its cached or default icon -- so the app appeared to have no favicon
		// while the server reported success for every request. Nothing in a log
		// or a status code said otherwise, which is what made it survive: a
		// missing asset and a served asset are indistinguishable from outside.
		//
		// The rule is a WHITELIST of extensions rather than "the last segment
		// contains a dot", because the dot rule decides the fate of paths this
		// package cannot see. Today every SPA route is a fixed path whose only
		// parameters are numeric ids (App.tsx), so the two rules agree -- but
		// the day someone adds /sources/:name, the dot rule silently starts
		// 404ing a real route for anyone whose source has a dot in its name,
		// and it does so in the router rather than in the page, where it would
		// be hard to trace. A whitelist can only ever be wrong about a file
		// type nobody serves, which is the cheaper failure.
		//
		// .html is deliberately absent: a request for a document is exactly
		// what the SPA fallback is for.
		if isStaticAssetPath(p) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("no such file\n"))
			return
		}

		// Unknown path: hand it to the SPA router rather than 404ing, so a
		// deep link like /routing/3 works on refresh.
		index, err := sub.Open("index.html")
		if err != nil {
			http.Error(w, "UI not built. Run `make ui` or `npm --prefix ui run build`.", http.StatusNotFound)
			return
		}
		defer index.Close()
		stat, _ := index.Stat()
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.ServeContent(w, r, "index.html", stat.ModTime(), index.(interface {
			Read([]byte) (int, error)
			Seek(int64, int) (int64, error)
		}))
	})
}
