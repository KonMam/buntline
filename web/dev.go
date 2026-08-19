//go:build !embedded

package web

import (
	"net/http"
	"os"
)

func Handler() http.Handler {
	if _, err := os.Stat("web/dist/index.html"); err == nil {
		return spaHandler(http.Dir("web/dist"))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><body style="font-family: system-ui; padding: 4rem">
<h2>buntline</h2>
<p>Frontend not built. Run <code>make web</code> (or <code>cd web && npm run build</code>),
or use the Vite dev server: <code>cd web && npm run dev</code>.</p>
<p>The API is live at <code>/api</code>.</p></body>`))
	})
}
