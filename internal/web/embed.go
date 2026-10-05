package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static/*
var staticFS embed.FS

// Handler serves the embedded SPA.
func Handler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Embedded files carry no ModTime, so http.ServeContent emits no
		// Last-Modified or ETag — browsers then heuristic-cache app.js/style.css
		// and keep running a stale UI after an upgrade. Force revalidation.
		w.Header().Set("Cache-Control", "no-cache")
		// SPA fallback: unknown paths → index.html (except assets).
		path := r.URL.Path
		if path != "/" && !exists(sub, path) {
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	})
}

func exists(fsys fs.FS, path string) bool {
	path = path[1:] // trim leading /
	if path == "" {
		return true
	}
	f, err := fsys.Open(path)
	if err != nil {
		return false
	}
	f.Close()
	return true
}
