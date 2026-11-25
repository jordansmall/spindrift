//go:build integration

package bwrap

import (
	"fmt"
	"testing"

	"spindrift.dev/launcher/internal/fixturebuild"
	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/runnertest/escape"
)

// productionSyscallFilter builds lib/seccomp.nix, the filter every real
// Connector sandbox runs under, so the escape tests see the shipped denylist.
// No flake output exposes it, so it is built from the nixpkgs the flake locks.
func productionSyscallFilter() (path, skipReason string, err error) {
	return fixturebuild.ResolveBuild("", "", func(root string) []string {
		return []string{"--impure", "--expr", fmt.Sprintf(
			`let pkgs = (builtins.getFlake %q).inputs.nixpkgs.legacyPackages.${builtins.currentSystem}; in import %s/lib/seccomp.nix { inherit pkgs; }`,
			root, root)}
	})
}

// escapeSandbox returns the real adapter over the escape-probe connector-host
// and the production syscall filter, and the host's store directory. It skips
// without bwrap, user namespaces or nix.
func escapeSandbox(t *testing.T) (runner.Runner, string) {
	t.Helper()
	requireRealBwrapProc(t)
	host, skip, err := escape.HostDir()
	if err != nil {
		t.Fatal(err)
	}
	if skip != "" {
		skipUnlessRequired(t, "%s", skip)
	}
	filter, skip, err := productionSyscallFilter()
	if err != nil {
		t.Fatal(err)
	}
	if skip != "" {
		skipUnlessRequired(t, "%s", skip)
	}
	art := inputdoc.BwrapArtifacts{ConnectorHost: inputdoc.ValueOf(host), SyscallFilter: inputdoc.ValueOf(filter)}
	r, err := New(runner.Config{}, art, func(string) (string, bool) { return "", false }, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return r, host
}

func TestSandboxEscapeReachesNothingOfTheHost(t *testing.T) {
	r, host := escapeSandbox(t)
	escape.Sandbox(t, r, host)
}

func TestSandboxRefusesAndAuditsAnUndeclaredRoute(t *testing.T) {
	r, _ := escapeSandbox(t)
	escape.RouteRefusal(t, r)
}

func TestSandboxRefusesAFrameForAnotherConnectorsSession(t *testing.T) {
	r, _ := escapeSandbox(t)
	escape.Impersonation(t, r)
}
