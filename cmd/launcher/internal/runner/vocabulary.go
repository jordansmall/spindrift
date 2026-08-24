package runner

import (
	"fmt"
	"strings"
)

// runtimeAliases maps an operator-facing runtime value to the CLI binary it
// invokes, for every runtime whose knob value differs from its binary name
// (currently just rancher -> nerdctl, Rancher Desktop's containerd mode).
// Every other valid runtime is its own binary. BinaryFor and AliasFor both
// resolve from this single map so the pairing can't drift in one direction
// (issue #2561).
var runtimeAliases = map[string]string{"rancher": "nerdctl"}

// BinaryFor maps a Config.Runtime value to the CLI binary it invokes — a
// forward lookup in runtimeAliases, or the runtime itself when it has no
// alias entry. Both NewOCI and ValidateRuntime consume this so the alias
// lives in exactly one spot.
func BinaryFor(runtime string) string {
	if bin, ok := runtimeAliases[runtime]; ok {
		return bin
	}
	return runtime
}

// AliasFor maps a probed binary name back to the operator-facing runtime
// value — the reverse of BinaryFor, found by scanning runtimeAliases' values.
// A binary with no reverse entry is its own operator-facing value.
func AliasFor(binary string) string {
	for runtime, bin := range runtimeAliases {
		if bin == binary {
			return runtime
		}
	}
	return binary
}

// Precedence is the order detection probes for an available container
// runtime (ADR 0027): podman first, then docker, then nerdctl (Rancher
// Desktop's containerd mode) after docker — since Rancher Desktop in dockerd
// mode already surfaces as "docker" and only containerd mode exposes
// nerdctl — then the daemonless bwrap fallback.
var Precedence = []string{"podman", "docker", "nerdctl", "bwrap"}

// Probe walks Precedence, calling lookPath on each binary name, and returns
// the operator-facing value (via AliasFor) for the first one found — or an
// actionable error naming every supported runtime (from ValidValues, so a
// new entry in lib/runtime-values.nix is named automatically) when none is
// available. lookPath is a plain function rather than an interface so
// callers (e.g. quickstart's Environment.LookPath) can pass their own lookup
// without an import-cycle-prone shared interface.
func Probe(lookPath func(string) (string, error)) (string, error) {
	for _, rt := range Precedence {
		if _, err := lookPath(rt); err == nil {
			return AliasFor(rt), nil
		}
	}
	return "", fmt.Errorf("no supported container runtime found on PATH — install one of: %s (\"rancher\" means nerdctl in Rancher Desktop containerd mode)", strings.Join(ValidValues, ", "))
}
