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

// NewTCPForwarder builds an http.Handler that transparently relays every
// request to http://upstreamHost:upstreamPort, attaching secret via
// registrymanifest.TCPSecretHeader on the outbound leg -- the box-local
// half of issue #3111's TCP-fallback transport. Ecosystem tooling (cargo,
// npm, Go, gradle) points at a bare loopback Forwarder port exactly as it
// does for the unix-socket transport (forwarder.go's socat bridge); this
// handler is what a Forwarder listening on that port relays through when
// the socket can't cross, standing in for socat, which can carry raw bytes
// but can't inject an HTTP header. GET/HEAD enforcement and the real upstream
// credential attach both stay launcher-side (registryproxy.New's Handler,
// served over the same ListenAndServeTCP this forwards to) -- this handler
// only adds the one header a socket transport gets for free from its own
// filesystem permissions.
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
			// SetURL preserves pr.Out's own path/query untouched (unlike
			// registryproxy.New's Rewrite hook, this forwarder's "upstream"
			// URL carries no path or query of its own to merge in) -- only
			// scheme/host/authority are replaced.
			pr.SetURL(target)
			pr.Out.Header.Set(registrymanifest.TCPSecretHeader, secret)
		},
	}

	return rp, nil
}

// SpawnHTTPForwarder starts an HTTP-aware Forwarder detached, listening on
// 127.0.0.1:port and relaying to upstreamHost:upstreamPort with secret
// attached via NewTCPForwarder -- the TCP-fallback transport's SpawnFunc-
// compatible counterpart to SpawnSocat. It works by re-executing this same
// driver-exec binary (os.Executable()) in a new "forward-registry-tcp"
// subcommand mode, mirroring how SpawnSocat execs an external "socat"
// binary -- the same Setsid-detach mechanism, just re-invoking ourselves
// instead of a separate binary, since there's no free-standing HTTP-proxy
// binary already on the image the way socat is.
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
	// The secret must never appear on the child's argv (visible via ps/
	// /proc to any local user); it rides the child's environment instead,
	// read back by the forward-registry-tcp subcommand via
	// REGISTRY_PROXY_TCP_SECRET. os.Environ() may already carry this key
	// (the caller read it from its own environment) -- drop any prior
	// value so the child sees exactly one, unambiguous entry.
	cmd.Env = append(filterEnv(os.Environ(), "REGISTRY_PROXY_TCP_SECRET"), "REGISTRY_PROXY_TCP_SECRET="+secret)
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	// Setsid detaches the Forwarder from the caller's session/process
	// group so it outlives the caller process; cmd.Wait() is deliberately
	// never called -- this must stay a detached, long-running process.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := closeOnExecInheritedFDs(); err != nil {
		return 0, err
	}

	if err := cmd.Start(); err != nil {
		return 0, err
	}
	return cmd.Process.Pid, nil
}

// filterEnv returns env with every entry for key removed, preserving order
// of the rest -- used to drop a pre-existing value before appending a fresh
// one, so a child process never sees the same key twice.
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
