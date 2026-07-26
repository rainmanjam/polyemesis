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

// Built reports whether a real UI was embedded, as opposed to the placeholder
// that keeps `go build ./...` working before `npm run build` has ever run.
func Built() bool {
	b, err := embedded.ReadFile("dist/index.html")
	if err != nil {
		return false
	}
	return !strings.Contains(string(b), "polyemesis-ui-placeholder")
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
