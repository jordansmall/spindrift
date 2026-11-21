package bindregistry

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"spindrift.dev/launcher/internal/registrymanifest"
)

// NewTCPForwarder relays every request to http://upstreamHost:upstreamPort,
// attaching secret via registrymanifest.TCPSecretHeader (issue #3111). That
// header authenticates the hop: a TCP port has none of the filesystem
// permissions guarding the unix-socket transport, and socat cannot inject it.
// GET/HEAD enforcement and the upstream credential attach stay launcher-side.
func NewTCPForwarder(upstreamHost string, upstreamPort int, secret string) (http.Handler, error) {
	if upstreamHost == "" {
		return nil, fmt.Errorf("bindregistry: upstream host must not be empty")
	}
	if upstreamPort == 0 {
		return nil, fmt.Errorf("bindregistry: upstream port must not be zero")
	}
	if secret == "" {
		return nil, fmt.Errorf("bindregistry: secret must not be empty")
	}

	target := &url.URL{
		Scheme: "http",
		Host:   fmt.Sprintf("%s:%d", upstreamHost, upstreamPort),
	}

	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			// SetURL also rewrites the Host header to target's host; restore
			// the inbound Host, which the launcher-side proxy relies on to
			// derive the Forwarder's own address for the cargo dl rewrite.
			pr.SetURL(target)
			pr.Out.Host = pr.In.Host
			pr.Out.Header.Set(registrymanifest.TCPSecretHeader, secret)
		},
	}

	return rp, nil
}

// SpawnHTTPForwarder starts a detached Forwarder on 127.0.0.1:port relaying
// to upstreamHost:upstreamPort with secret attached via NewTCPForwarder. It
// execs binary's "forward-registry-tcp" subcommand (only driver-exec has one)
// because the image carries no standalone HTTP proxy the way it carries socat.
func SpawnHTTPForwarder(binary, upstreamHost string, upstreamPort int, secret string, port int) (int, error) {
	cmd := exec.Command(binary,
		"forward-registry-tcp",
		"-listen-port", fmt.Sprintf("%d", port),
		"-upstream-host", upstreamHost,
		"-upstream-port", fmt.Sprintf("%d", upstreamPort),
	)
	// The secret rides the environment, never argv, which ps and /proc expose
	// to any local user. Drop any inherited value so the child gets exactly
	// one entry.
	cmd.Env = append(filterEnv(os.Environ(), "REGISTRY_PROXY_TCP_SECRET"), "REGISTRY_PROXY_TCP_SECRET="+secret)
	return startDetached(cmd)
}

// filterEnv returns env with every entry for key removed.
func filterEnv(env []string, key string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return out
}
