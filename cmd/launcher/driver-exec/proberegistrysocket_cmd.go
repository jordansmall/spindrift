package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"os"

	"spindrift.dev/launcher/internal/registryprobe"
)

// isProbeRegistrySocketInvocation reports whether args (os.Args[1:])
// selects the probe-registry-socket subcommand: a distinct verb, not a
// top-level flag, mirroring isBindRegistryInvocation.
func isProbeRegistrySocketInvocation(args []string) bool {
	return len(args) > 0 && args[0] == "probe-registry-socket"
}

// probeRegistrySocketVisible reports whether path exists and is a unix
// domain socket file -- the guest-side half of issue #3111's capability
// probe: the host may have mounted a socket that the guest kernel doesn't
// actually project an endpoint behind, so seeing the file is necessary but
// not sufficient.
func probeRegistrySocketVisible(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeSocket != 0
}

// probeRegistrySocketConnect reports whether path can be dialed as a unix
// domain socket -- the guest-side half of issue #3111's capability probe
// that a visible-but-unconnectable socket (a passthrough sharing layer that
// presents the inode with no kernel endpoint behind it) must fail. A
// no-listener/stale-socket connect fails near-instantly with ECONNREFUSED
// at the kernel level, so no DialTimeout wrapper is needed here; a wedged
// (accepts-but-never-responds) far end is out of scope -- this only proves
// the guest can complete a connection, not that the far end is healthy.
// Returns the dial error on failure so the CLI wrapper can surface the real
// diagnostic, mirroring probeRegistryTCPConnect.
func probeRegistrySocketConnect(path string) (bool, error) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return false, err
	}
	conn.Close()
	return true, nil
}

// runProbeRegistrySocket is the `probe-registry-socket` subcommand's thin
// CLI wrapper (ADR 0007's thin-exec-glue tier, issue #3111): it runs inside
// a throwaway container in the guest so the host-side capability prober can
// tell whether the configured container runtime actually projects a
// connectable unix socket into the guest, rather than just asserting the
// runtime claims to support socket mounts or checking GOOS=="darwin". Exits
// registryprobe.ExitCapable/ExitIncapable for the verdict (issue #3120) and
// leaves usage/flag-parse errors at 1, since those aren't a verdict at all.
// Returns the process exit code.
func runProbeRegistrySocket(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("probe-registry-socket", flag.ContinueOnError)
	fs.SetOutput(stdout)
	path := fs.String("path", "", "the in-box path where the host is expected to have mounted a unix socket for this probe (required)")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	if *path == "" {
		fmt.Fprintln(stdout, "driver-exec probe-registry-socket: -path is required")
		return 1
	}

	if !probeRegistrySocketVisible(*path) {
		fmt.Fprintln(stdout, "not visible: "+*path)
		return registryprobe.ExitIncapable
	}

	ok, err := probeRegistrySocketConnect(*path)
	if !ok {
		fmt.Fprintf(stdout, "not connectable: %s: %v\n", *path, err)
		return registryprobe.ExitIncapable
	}

	fmt.Fprintln(stdout, "ok: "+*path)
	return registryprobe.ExitCapable
}
