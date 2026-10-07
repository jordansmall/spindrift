package main

import (
	"net"
	"net/http"
	"strings"
)

// hostGuard rejects requests whose Host header names anything but localhost,
// an IP literal, or an allowed name. DNS rebinding points an attacker's name
// at the Dashboard's address, loopback or LAN, so the browser treats the page
// as same-origin while Host still names the attacker; an IP literal can never
// be rebound.
func hostGuard(next http.Handler, allow []string) http.Handler {
	allowed := make(map[string]bool, len(allow))
	for _, name := range allow {
		allowed[hostOnly(name)] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := hostOnly(r.Host)
		if host == "localhost" || net.ParseIP(host) != nil || allowed[host] {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "forbidden: Host not allowed (see --allow-host)", http.StatusForbidden)
	})
}

// hostOnly strips the port, any IPv6 brackets, and one trailing root dot from
// a host[:port] value and lower-cases it. A fully qualified `localhost.` names
// the same host, so it must not fall outside the allow rule.
func hostOnly(hostport string) string {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	host = strings.TrimSuffix(host, ".")
	return strings.ToLower(host)
}
