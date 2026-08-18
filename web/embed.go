//go:build embedded

// Package web serves the frontend. With the `embedded` build tag (release
// builds), the Vite output in web/dist is compiled into the binary; without
// it (dev builds, tests, CI's plain `go build`), web/dev.go serves from
// disk instead, so the Go toolchain never depends on a frontend build.
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed all:dist
var dist embed.FS

func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // embed shape is fixed at compile time
	}
	return spaHandler(http.FS(sub))
}
