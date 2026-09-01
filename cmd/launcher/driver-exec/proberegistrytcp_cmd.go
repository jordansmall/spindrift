package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// probeRegistryTCPDialTimeout bounds probeRegistryTCPConnect's dial: this
// verb runs inside a throwaway container against a proxy that should
// already be up (an --add-host host-gateway route into the Box), so a
// genuinely unreachable route should fail fast rather than eat into the
// outer probe's own ~30s budget.
const probeRegistryTCPDialTimeout = 5 * time.Second

// isProbeRegistryTCPInvocation reports whether args (os.Args[1:]) selects
// the probe-registry-tcp subcommand: a distinct verb, not a top-level flag,
// mirroring isProbeRegistrySocketInvocation.
func isProbeRegistryTCPInvocation(args []string) bool {
	return len(args) > 0 && args[0] == "probe-registry-tcp"
}

// probeRegistryTCPConnect reports whether host:port can be dialed over TCP
// -- the guest-side half of issue #3111's live reachability sub-probe: the
// TCP fallback binds a listener the Box is meant to reach via `--add-host
// host-gateway`, but on a plain Linux bridge that resolves to the bridge IP
// and a remote-context daemon runs on a different machine entirely, so
// nothing short of an actual dial from inside the guest proves the route is
// live. Returns the dial error on failure so the CLI wrapper can surface
// the real diagnostic.
func probeRegistryTCPConnect(host string, port int) (bool, error) {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, probeRegistryTCPDialTimeout)
	if err != nil {
		return false, err
	}
	conn.Close()
	return true, nil
}

// runProbeRegistryTCP is the `probe-registry-tcp` subcommand's thin CLI
// wrapper (ADR 0007's thin-exec-glue tier, issue #3111): it runs inside a
// throwaway container in the guest so the host-side capability prober can
// tell whether the configured --add-host host-gateway route actually
// reaches the launcher's TCP registry-proxy fallback, rather than trusting
// the route exists just because the flag was set. Unlike
// probe-registry-socket's CLI wrapper, this one calls the shared
// probeRegistryTCPConnect directly -- there is exactly one dial code path,
// not a production one and a separately-tested one. Returns the process
// exit code.
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
		return 1
	}

	fmt.Fprintln(stdout, "ok: "+addr)
	return 0
}
