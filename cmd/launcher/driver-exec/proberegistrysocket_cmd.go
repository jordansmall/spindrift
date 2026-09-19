package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"os"

	"spindrift.dev/launcher/internal/registryprobe"
)

func isProbeRegistrySocketInvocation(args []string) bool {
	return len(args) > 0 && args[0] == "probe-registry-socket"
}

// probeRegistrySocketVisible reports whether path is a unix domain socket
// file. The host can mount a socket that the guest kernel puts no endpoint
// behind, so a visible file is necessary but not sufficient (issue #3111).
func probeRegistrySocketVisible(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeSocket != 0
}

// probeRegistrySocketConnect reports whether path can be dialed. A socket
// that is visible but has no endpoint behind it must fail here (issue #3111).
// A stale socket with no listener fails near-instantly with ECONNREFUSED, so
// no DialTimeout is needed. A far end that accepts and never responds still
// passes; this proves only that the guest can complete a connection.
func probeRegistrySocketConnect(path string) (bool, error) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return false, err
	}
	conn.Close()
	return true, nil
}

// runProbeRegistrySocket runs inside a throwaway guest container so the host
// prober learns whether the runtime really projects a connectable unix socket,
// rather than trusting the runtime's claims or GOOS (ADR 0007, issue #3111).
// Its exit code is the verdict (registryprobe.ExitCapable/ExitIncapable); a
// usage error exits 1 because it is not a verdict (issue #3120).
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
