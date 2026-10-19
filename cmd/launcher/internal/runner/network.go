package runner

// OCINetwork is an OCI Box's network as the shared plan resolves it (ADR 0069
// decision 6): the OCI adapter renders it and decides nothing itself.
type OCINetwork struct {
	// Mode is the NETWORK_MODE knob as configured.
	Mode NetworkMode
	// Arg is the OCI `--network` value; empty omits the flag.
	Arg string
}

// OCINetwork resolves c's network knobs for an OCI runtime (issue #2562, #2666).
// podmanNetwork is the raw PODMAN_NETWORK escape hatch, a row knob the oci
// family reads itself rather than carrying on Config. noHostLoopbackArg is the
// runtime's own `--network` spelling for NETWORK_MODE=no-host-loopback; that
// dialect is the OCI adapter's, so core never compares a runtime value.
func (c Config) OCINetwork(podmanNetwork, noHostLoopbackArg string) OCINetwork {
	n := OCINetwork{Mode: c.NetworkMode}
	// Raw wins: mkHarness and main.go's checkNetworkModeRuntimeGate already
	// reject pairing it with a non-open mode.
	if podmanNetwork != "" {
		n.Arg = podmanNetwork
		return n
	}
	switch c.NetworkMode {
	case NetworkModeNoHostLoopback:
		n.Arg = noHostLoopbackArg
	case NetworkModeNone:
		n.Arg = string(NetworkModeNone)
	}
	return n
}

// BwrapNetwork is a bwrap Box's network as the shared plan resolves it (ADR
// 0069 decision 6): the bwrap adapter renders it and decides nothing itself.
type BwrapNetwork struct {
	// Isolate cuts the Box off the host network namespace.
	Isolate bool
	// Pasta restores an isolated Box's egress through the pasta wrapper.
	Pasta bool
}

// BwrapNetwork resolves c's network knobs for the bwrap runner (issue #2562,
// #2666). It reads no Runtime: RUNNER_KIND, not RUNTIME, picks the adapter
// (issue #2538), so RUNNER_KIND=bwrap/RUNTIME=podman must still isolate.
// unshareNet is the raw BWRAP_UNSHARE_NET row knob the bwrap family reads.
func (c Config) BwrapNetwork(unshareNet bool) BwrapNetwork {
	var n BwrapNetwork
	// Every mode except "host" isolates, including the zero value. The raw
	// unshareNet can only force isolation on; kept for defense in depth.
	n.Isolate = unshareNet || c.NetworkMode != NetworkModeHost
	// "none" is fully offline: bare --unshare-net, no pasta helper.
	n.Pasta = n.Isolate && c.NetworkMode != NetworkModeNone
	return n
}
