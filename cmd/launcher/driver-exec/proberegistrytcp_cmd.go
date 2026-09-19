package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"spindrift.dev/launcher/internal/registryprobe"
)

// probeRegistryTCPDialTimeout keeps an unreachable route failing fast so it
// does not eat into the outer probe's own ~30s budget.
const probeRegistryTCPDialTimeout = 5 * time.Second

func isProbeRegistryTCPInvocation(args []string) bool {
	return len(args) > 0 && args[0] == "probe-registry-tcp"
}

// probeRegistryTCPConnect reports whether host:port can be dialed over TCP
// (issue #3111). Only a real dial from inside the guest proves the route is
// live: `--add-host host-gateway` resolves to the bridge IP on a plain Linux
// bridge, and a remote-context daemon runs on another machine entirely. It
// returns the dial error so the caller can print the real diagnostic.
func probeRegistryTCPConnect(host string, port int) (bool, error) {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, probeRegistryTCPDialTimeout)
	if err != nil {
		return false, err
	}
	conn.Close()
	return true, nil
}

// runProbeRegistryTCP implements the `probe-registry-tcp` subcommand (ADR 0007's
// thin-exec-glue tier, issue #3111). It exits registryprobe.ExitCapable or
// ExitIncapable for the verdict (issue #3120) and leaves usage and flag-parse
// errors at 1, since those are not a verdict at all.
func runProbeRegistryTCP(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("probe-registry-tcp", flag.ContinueOnError)
	fs.SetOutput(stdout)
	host := fs.String("host", "", "the host the launcher's TCP registry-proxy fallback is expected to be reachable at (required)")
	port := fs.Int("port", 0, "the port the launcher's TCP registry-proxy fallback is expected to be reachable at (required)")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	if *host == "" {
		fmt.Fprintln(stdout, "driver-exec probe-registry-tcp: -host is required")
		return 1
	}
	if *port == 0 {
		fmt.Fprintln(stdout, "driver-exec probe-registry-tcp: -port is required")
		return 1
	}

	addr := net.JoinHostPort(*host, strconv.Itoa(*port))
	ok, err := probeRegistryTCPConnect(*host, *port)
	if !ok {
		fmt.Fprintf(stdout, "not connectable: %s: %v\n", addr, err)
		return registryprobe.ExitIncapable
	}

	fmt.Fprintln(stdout, "ok: "+addr)
	return registryprobe.ExitCapable
}
