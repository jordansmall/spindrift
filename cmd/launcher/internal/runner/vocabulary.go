package runner

import (
	"fmt"
	"strings"
)

// runtimeAliases maps an operator-facing runtime value to the CLI binary it
// invokes, for the runtimes whose knob value differs from their binary name.
// BinaryFor and AliasFor both resolve from this one map so the pairing cannot
// drift in one direction (issue #2561).
var runtimeAliases = map[string]string{"rancher": "nerdctl"}

// BinaryFor maps a Config.Runtime value to the CLI binary it invokes.
func BinaryFor(runtime string) string {
	if bin, ok := runtimeAliases[runtime]; ok {
		return bin
	}
	return runtime
}

// AliasFor maps a probed binary name back to its operator-facing runtime value.
func AliasFor(binary string) string {
	for runtime, bin := range runtimeAliases {
		if bin == binary {
			return runtime
		}
	}
	return binary
}

// Precedence is the order in which Probe looks for a container runtime (ADR
// 0027). nerdctl comes after docker because Rancher Desktop in dockerd mode
// already answers to "docker", and only its containerd mode ships nerdctl.
// bwrap is the daemonless fallback.
var Precedence = []string{"podman", "docker", "nerdctl", "bwrap"}

// Probe returns the operator-facing value of the first runtime in Precedence
// that lookPath finds. lookPath is a plain function rather than an interface so
// callers such as quickstart's Environment.LookPath can pass their own lookup
// without a shared interface that would risk an import cycle.
func Probe(lookPath func(string) (string, error)) (string, error) {
	for _, rt := range Precedence {
		if _, err := lookPath(rt); err == nil {
			return AliasFor(rt), nil
		}
	}
	return "", fmt.Errorf("no supported container runtime found on PATH — install one of: %s (\"rancher\" means nerdctl in Rancher Desktop containerd mode)", strings.Join(ValidValues, ", "))
}
