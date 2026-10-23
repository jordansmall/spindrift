package ecosystemkit

import (
	"net/url"
	"strings"
)

// HTTPAbsoluteURL reports raw's host and trimmed base URL when raw is an
// absolute http(s) URL with no userinfo. Rejecting userinfo matches
// registryroutes.ValidateUpstreamOrigin and keeps a credential out of the
// routes file. u.Hostname() rejects a port-only host ("http://:8080/"), which
// registryvocab.HostKey normalizes to the empty match-host that
// registryroutes.Parse rejects.
func HTTPAbsoluteURL(raw string) (host, upstreamBaseURL string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return "", "", false
	}
	return u.Host, strings.TrimSuffix(raw, "/"), true
}
