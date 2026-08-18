package web

import (
	"net/http"
	"strings"
)

// spaHandler serves static files, falling back to index.html for paths
// without extensions (client-side routes).
func spaHandler(fsys http.FileSystem) http.Handler {
	files := http.FileServer(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path != "" && !strings.Contains(path, ".") {
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
}
