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
	files := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}

		if f, err := sub.Open(p); err == nil {
			f.Close()
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
	}), nil
}
