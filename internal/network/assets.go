package network

import (
	"bytes"
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed all:webdist
var webDist embed.FS

// WebAssets serves the embedded React build. webdist holds only a placeholder until `make ui-stage` copies ui/dist in.
func WebAssets() http.Handler {
	files, _ := fs.Sub(webDist, "webdist")
	return webAssets(files)
}

// spaRoutes are first path segments the client router owns; a missing file under them gets index.html.
var spaRoutes = []string{"/sessions", "/library", "/machines", "/limits", "/settings", "/login"}

func webAssets(files fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		if info, err := fs.Stat(files, name); err == nil && !info.IsDir() {
			serveAsset(w, r, files, name)
			return
		}
		switch {
		case strings.HasPrefix("/"+name, "/assets/"):
			http.NotFound(w, r)
		case isSPARoute("/" + name):
			serveAsset(w, r, files, "index.html")
		case path.Ext(name) != "":
			http.NotFound(w, r)
		default:
			serveAsset(w, r, files, "index.html")
		}
	})
}

func isSPARoute(urlPath string) bool {
	for _, route := range spaRoutes {
		if urlPath == route || strings.HasPrefix(urlPath, route+"/") {
			return true
		}
	}
	return false
}

func serveAsset(w http.ResponseWriter, r *http.Request, files fs.FS, name string) {
	data, err := fs.ReadFile(files, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType(name))
	w.Header().Set("Vary", "Accept-Encoding")
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		if zipped, err := fs.ReadFile(files, name+".gz"); err == nil {
			data = zipped
			w.Header().Set("Content-Encoding", "gzip")
		}
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}

// contentType pins the types the browser depends on, so host mime databases cannot change them.
func contentType(name string) string {
	switch ext := path.Ext(name); ext {
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".html":
		return "text/html; charset=utf-8"
	case ".webmanifest":
		return "application/manifest+json"
	default:
		if t := mime.TypeByExtension(ext); t != "" {
			return t
		}
		return "application/octet-stream"
	}
}
