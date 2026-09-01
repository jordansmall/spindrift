package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"

	"spindrift.dev/launcher/internal/bindregistry"
)

// isForwardRegistryTCPInvocation reports whether args (os.Args[1:]) selects
// the forward-registry-tcp subcommand: a distinct verb, not a top-level
// flag, mirroring isProbeRegistrySocketInvocation.
func isForwardRegistryTCPInvocation(args []string) bool {
	return len(args) > 0 && args[0] == "forward-registry-tcp"
}

// runForwardRegistryTCP is the `forward-registry-tcp` subcommand's thin CLI
// wrapper (ADR 0007's thin-exec-glue tier, issue #3111): it is the detached,
// long-running child bindregistry.SpawnHTTPForwarder execs -- the TCP-mode
// counterpart of the detached `socat` process bindregistry.SpawnSocat
// leaves running. It never returns on success; http.ListenAndServe blocks
// forever, which is correct here since this subcommand only ever runs as
// that detached child, never inline in a caller waiting on its exit code.
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

	// Read from the environment, not a flag: the secret must never appear
	// on this process's own argv, which is visible via ps/proc to any local
	// user (see bindregistry.SpawnHTTPForwarder, which sets this exact
	// variable rather than passing a flag).
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
