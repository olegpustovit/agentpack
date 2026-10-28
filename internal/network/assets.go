package network

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed web/public/*
var webAssets embed.FS

func WebAssets() http.Handler {
	files, _ := fs.Sub(webAssets, "web/public")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /static/manifest.webmanifest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/manifest+json")
		http.ServeFileFS(w, r, files, "manifest.webmanifest")
	})
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(files))))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		data, err := fs.ReadFile(files, "index.html")
		if err != nil {
			http.Error(w, "UI unavailable", 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	})
	return mux
}
