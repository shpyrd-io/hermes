package main

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// The setup page: web/, a Next application on the shpyrd design system,
// compiled to static files under /setup (web/out, built before the Go
// build; see the Dockerfile). One document: every address under /setup
// that is not a file of the build is the page.
//
//go:embed all:web/out
var webFiles embed.FS

const webPrefix = "/setup"

func webHandler() http.Handler {
	sub, err := fs.Sub(webFiles, "web/out")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, webPrefix), "/")
		if rel != "" {
			if st, err := fs.Stat(sub, rel); err == nil && !st.IsDir() {
				if strings.HasPrefix(rel, "_next/static/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				r2 := r.Clone(r.Context())
				r2.URL.Path = "/" + rel
				files.ServeHTTP(w, r2)
				return
			}
		}
		page, err := fs.ReadFile(sub, "index.html")
		if err != nil {
			http.Error(w, "the setup page was not built into this image", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(page)
	})
}
