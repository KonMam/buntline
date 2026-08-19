package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/KonMam/buntline/internal/config"
)

func guardFor(t *testing.T, cfg config.Config) http.Handler {
	t.Helper()
	s := &Server{cfg: cfg}
	return s.guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
}

func TestGuardHost(t *testing.T) {
	cases := []struct {
		name    string
		addr    string
		allowed []string
		host    string
		want    int
	}{
		{"loopback name", "localhost:7433", nil, "localhost:7433", 200},
		{"loopback ip", "localhost:7433", nil, "127.0.0.1:7433", 200},
		{"ipv6 loopback", "localhost:7433", nil, "[::1]:7433", 200},
		{"vite dev proxy keeps its own port", "localhost:7433", nil, "localhost:5173", 200},
		{"rebinding dns name", "localhost:7433", nil, "evil.com:7433", 403},
		{"ip literal is never rebinding", "0.0.0.0:7433", nil, "192.168.1.10:7433", 200},
		{"bound dns name", "buntline.lan:7433", nil, "buntline.lan:7433", 200},
		{"foreign name with wildcard bind", "0.0.0.0:7433", nil, "evil.com:7433", 403},
		{"allowed_hosts name", "0.0.0.0:7433", []string{"box.tail1234.ts.net"}, "box.tail1234.ts.net:7433", 200},
		{"empty host", "localhost:7433", nil, "", 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := guardFor(t, config.Config{Addr: tc.addr, AllowedHosts: tc.allowed})
			req := httptest.NewRequest("GET", "/api/sessions", nil)
			req.Host = tc.host
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("host %q: got %d, want %d", tc.host, rec.Code, tc.want)
			}
		})
	}
}

func TestGuardOrigin(t *testing.T) {
	cases := []struct {
		name   string
		host   string
		origin string
		want   int
	}{
		{"no origin (curl)", "localhost:7433", "", 200},
		{"same origin", "localhost:7433", "http://localhost:7433", 200},
		{"vite dev server origin", "localhost:7433", "http://localhost:5173", 200},
		{"cross-site", "localhost:7433", "https://evil.com", 403},
		{"null origin (sandboxed iframe)", "localhost:7433", "null", 403},
		{"same vpn name", "box.tail1234.ts.net:7433", "http://box.tail1234.ts.net:7433", 200},
		{"garbage origin", "localhost:7433", "::not a url::", 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := guardFor(t, config.Config{Addr: "localhost:7433", AllowedHosts: []string{"box.tail1234.ts.net"}})
			req := httptest.NewRequest("POST", "/api/sessions", nil)
			req.Host = tc.host
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("origin %q: got %d, want %d", tc.origin, rec.Code, tc.want)
			}
		})
	}
}
