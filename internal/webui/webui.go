package webui

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// The build pipeline replaces dist with the Vue production build before Go
// compilation. A checked-in fallback keeps `go test` and source builds valid.
//
//go:embed dist/*
var assets embed.FS

// HasAsset reports whether requestPath names a real embedded frontend file.
// It lets the outer router distinguish Hostpin's hashed chunks from an active
// third-party theme that also publishes files below /assets/.
func HasAsset(requestPath string) bool {
	root, err := fs.Sub(assets, "dist")
	if err != nil {
		return false
	}
	clean := strings.TrimPrefix(path.Clean("/"+requestPath), "/")
	if clean == "" || clean == "." || clean == "index.html" {
		return false
	}
	info, err := fs.Stat(root, clean)
	return err == nil && info.Mode().IsRegular()
}

func Handler() http.Handler {
	root, _ := fs.Sub(assets, "dist")
	files := http.FileServer(http.FS(root))
	index, _ := fs.ReadFile(root, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if clean == "." || clean == "" {
			clean = "index.html"
		}
		if _, err := fs.Stat(root, clean); err != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))
			return
		}
		files.ServeHTTP(w, r)
	})
}
