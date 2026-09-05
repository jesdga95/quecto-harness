// Package web serves the built UI from the binary. It is separate from the
// server package so that a checkout without Node still compiles: a missing
// build produces a page saying so rather than a build error.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

const notBuiltPage = `quecto harness is running.

The web UI is not built. Run:

    make web

then restart, or use the JSON API at /api/state.
`

// Built reports whether the UI was compiled in.
func Built() bool {
	_, err := dist.Open("dist/index.html")
	return err == nil
}

// Handler serves the built UI, or a page explaining that it is not built.
// Any path without an extension falls back to index.html, so a client-side
// route survives a reload.
func Handler() http.Handler {
	if !Built() {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.Write([]byte(notBuiltPage))
		})
	}
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic("web: the embedded dist directory is unreadable: " + err.Error())
	}
	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := path.Clean("/" + r.URL.Path)
		if path.Ext(clean) == "" {
			serveIndex(w, r, sub)
			return
		}
		if _, err := fs.Stat(sub, strings.TrimPrefix(clean, "/")); err != nil {
			serveIndex(w, r, sub)
			return
		}
		if strings.HasPrefix(clean, "/assets/") {
			// Vite fingerprints these names, so they can never go stale.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, sub fs.FS) {
	body, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		http.Error(w, "The web UI is not built: run make web", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(body)
}
