package server

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// guard rejects requests that only a hostile web page could have
// produced. Two browser attacks matter for a server that executes shell
// commands, even bound to localhost:
//
//   - DNS rebinding: a page at evil.com re-resolves its own name to this
//     machine and the browser sends API requests here with
//     "Host: evil.com". A rebound request always carries the attacker's
//     DNS name, so pinning the Host header defeats it. Acceptable hosts:
//     loopback names, IP literals (a browser only sends one when the
//     user typed the address), the bound host, and allowed_hosts (for
//     serving under a VPN DNS name).
//
//   - Cross-site requests: any open page can POST to this port unless
//     the Origin header is checked. A present Origin must be loopback
//     (the Vite dev server proxies from its own port) or match the host
//     the request was addressed to.
//
// Non-browser clients send neither header oddly and pass untouched. This
// is a browser boundary, not authentication; network access control is
// the VPN's job.
func (s *Server) guard(next http.Handler) http.Handler {
	bound := hostnameOf(s.cfg.Addr)
	if bound == "0.0.0.0" || bound == "::" {
		bound = "" // wildcard bind pins nothing
	}
	extra := make(map[string]bool, len(s.cfg.AllowedHosts))
	for _, h := range s.cfg.AllowedHosts {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			extra[h] = true
		}
	}
	hostOK := func(h string) bool {
		if h == "" {
			return false
		}
		if isLoopbackName(h) || net.ParseIP(h) != nil {
			return true
		}
		return h == bound || extra[h]
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := hostnameOf(r.Host)
		if !hostOK(host) {
			http.Error(w, "unrecognized Host header (DNS rebinding protection); if buntline is legitimately served under this name, add it to allowed_hosts in config.toml", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" {
			u, err := url.Parse(o)
			var oh string
			if err == nil {
				oh = strings.ToLower(u.Hostname())
			}
			if oh == "" || (oh != host && !isLoopbackName(oh) && !extra[oh]) {
				http.Error(w, "cross-origin request rejected", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// hostnameOf extracts the lowercase hostname from a host[:port] value.
func hostnameOf(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return strings.ToLower(strings.Trim(h, "[]"))
	}
	return strings.ToLower(strings.Trim(hostport, "[]"))
}

func isLoopbackName(h string) bool {
	if h == "localhost" {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
