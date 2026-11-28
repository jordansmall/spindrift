package forge

import (
	"net"
	"strconv"
	"strings"
)

// SameHTTPSHost reports whether two https host[:port] values name one origin,
// case-insensitively, with the default port 443 (or an empty one) implied. Any
// other port must match exactly.
func SameHTTPSHost(a, b string) bool {
	return strings.EqualFold(withoutDefaultPort(a), withoutDefaultPort(b))
}

func withoutDefaultPort(host string) string {
	_, port, err := net.SplitHostPort(host)
	if err != nil {
		return host
	}
	bare := host[:strings.LastIndexByte(host, ':')]
	if port == "" {
		return bare
	}
	if n, err := strconv.ParseUint(port, 10, 16); err == nil && n == 443 {
		return bare
	}
	return host
}
