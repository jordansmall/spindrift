package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"

	"spindrift.dev/launcher/internal/bindregistry"
)

// isForwardRegistryTCPInvocation reports whether args selects the
// forward-registry-tcp subcommand, which is a verb rather than a top-level flag.
func isForwardRegistryTCPInvocation(args []string) bool {
	return len(args) > 0 && args[0] == "forward-registry-tcp"
}

// runForwardRegistryTCP runs the forward-registry-tcp subcommand (ADR 0007,
// issue #3111). It is the detached child bindregistry.SpawnHTTPForwarder execs,
// so on success it never returns: http.ListenAndServe blocks forever and no
// caller waits on its exit code.
func runForwardRegistryTCP(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("forward-registry-tcp", flag.ContinueOnError)
	fs.SetOutput(stdout)
	listenPort := fs.Int("listen-port", 0, "TCP port to listen on at 127.0.0.1 (required)")
	upstreamHost := fs.String("upstream-host", "", "host the launcher-side registry proxy is reachable at (required)")
	upstreamPort := fs.Int("upstream-port", 0, "port the launcher-side registry proxy is reachable at (required)")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	if *listenPort == 0 {
		fmt.Fprintln(stdout, "driver-exec forward-registry-tcp: -listen-port is required")
		return 1
	}
	if *upstreamHost == "" {
		fmt.Fprintln(stdout, "driver-exec forward-registry-tcp: -upstream-host is required")
		return 1
	}
	if *upstreamPort == 0 {
		fmt.Fprintln(stdout, "driver-exec forward-registry-tcp: -upstream-port is required")
		return 1
	}

	// bindregistry.SpawnHTTPForwarder sets this variable rather than passing a
	// flag: the secret must never reach this process's argv, which ps/proc
	// expose to any local user.
	secret := os.Getenv("REGISTRY_PROXY_TCP_SECRET")
	if secret == "" {
		fmt.Fprintln(stdout, "driver-exec forward-registry-tcp: REGISTRY_PROXY_TCP_SECRET is required")
		return 1
	}

	handler, err := bindregistry.NewTCPForwarder(*upstreamHost, *upstreamPort, secret)
	if err != nil {
		fmt.Fprintln(stdout, "driver-exec forward-registry-tcp:", err)
		return 1
	}

	addr := fmt.Sprintf("127.0.0.1:%d", *listenPort)
	if err := http.ListenAndServe(addr, handler); err != nil {
		fmt.Fprintln(stdout, "driver-exec forward-registry-tcp: listen and serve on "+addr+":", err)
		return 1
	}

	return 0
}
