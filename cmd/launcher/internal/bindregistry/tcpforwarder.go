package bindregistry

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"spindrift.dev/launcher/internal/registrymanifest"
)

// NewTCPForwarder relays every request to http://upstreamHost:upstreamPort,
// attaching secret via registrymanifest.TCPSecretHeader (issue #3111). That
// header authenticates the hop: a loopback port has none of the filesystem
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
// re-execs this binary in its "forward-registry-tcp" subcommand mode because
// the image carries no free-standing HTTP proxy binary the way it carries
// socat.
func SpawnHTTPForwarder(upstreamHost string, upstreamPort int, secret string, port int) (int, error) {
	self, err := os.Executable()
	if err != nil {
		return 0, err
	}

	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return 0, err
	}
	defer devNull.Close()

	cmd := exec.Command(self,
		"forward-registry-tcp",
		"-listen-port", fmt.Sprintf("%d", port),
		"-upstream-host", upstreamHost,
		"-upstream-port", fmt.Sprintf("%d", upstreamPort),
	)
	// The secret must never appear on the child's argv, which ps and /proc
	// expose to any local user, so it rides the environment instead.
	// os.Environ() may already carry this key, so drop any prior value and
	// leave the child exactly one entry.
	cmd.Env = append(filterEnv(os.Environ(), "REGISTRY_PROXY_TCP_SECRET"), "REGISTRY_PROXY_TCP_SECRET="+secret)
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	// Setsid detaches the Forwarder from the caller's session so it outlives
	// the caller. cmd.Wait() is never called; this stays a detached,
	// long-running process.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := closeOnExecInheritedFDs(); err != nil {
		return 0, err
	}

	if err := cmd.Start(); err != nil {
		return 0, err
	}
	return cmd.Process.Pid, nil
}

// filterEnv returns env with every entry for key removed, so a caller can
// append a fresh value and leave a child exactly one entry for that key.
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
