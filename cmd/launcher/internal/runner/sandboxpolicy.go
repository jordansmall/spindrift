package runner

import (
	"fmt"
	"slices"
	"strings"
)

// sandboxForbidden lists long flags that would give the sandbox a way out or
// a secret; each is matched as the bare flag or as flag=value.
var sandboxForbidden = []string{
	"--tty", "--env", "--env-file", "--volume", "--mount", "--tmpfs", "--add-host",
	"--privileged", "--cap-add", "--device", "--publish", "--pid", "--ipc",
	"--userns", "--security-opt=seccomp=unconfined",
}

// sandboxForbiddenShort are the short flags (tty, env, volume, publish) that
// are forbidden wherever they sit in a single-dash token: `-it`, `-eFOO=bar`
// and `-vX:Y` carry one as surely as a bare `-t`.
const sandboxForbiddenShort = "tevp"

// SandboxViolations lists how an argv for `run` of a Connector sandbox
// departs from the isolation the sandbox promises: no env, mounts, ports or
// network, a bounded footprint, and the host's frame stream on raw stdio.
// It is the single isolation policy the adapter contract and the seamtest
// fake share.
func SandboxViolations(argv []string) []string {
	var bad []string
	has := func(flag string) bool { return slices.Contains(argv, flag) }
	hasPrefix := func(prefix string) bool {
		return slices.ContainsFunc(argv, func(a string) bool { return strings.HasPrefix(a, prefix) })
	}
	for _, want := range []string{"-i", "--rm", "--network=none", "--cap-drop=all", "--security-opt=no-new-privileges", "--read-only"} {
		if !has(want) {
			bad = append(bad, "lacks "+want)
		}
	}
	for _, prefix := range []string{"--pids-limit=", "--memory="} {
		if !hasPrefix(prefix) {
			bad = append(bad, "lacks "+prefix+"...")
		}
	}
	i := slices.Index(argv, "--name")
	if i < 0 || i+1 >= len(argv) || !strings.HasPrefix(argv[i+1], ConnectorSandboxPrefix) {
		bad = append(bad, fmt.Sprintf("--name is not a %q name", ConnectorSandboxPrefix))
	}
	for _, a := range argv {
		flag, _, _ := strings.Cut(a, "=")
		short := !strings.HasPrefix(a, "--") && strings.HasPrefix(a, "-") && strings.ContainsAny(a[1:], sandboxForbiddenShort)
		if short || slices.Contains(sandboxForbidden, a) || slices.Contains(sandboxForbidden, flag) {
			bad = append(bad, "carries forbidden "+a)
		}
		if strings.HasPrefix(a, "--network") && a != "--network=none" {
			bad = append(bad, fmt.Sprintf("carries %s, want only --network=none", a))
		}
	}
	return bad
}

// bwrapSandboxRequired are the argv runs, in order, a bwrap Connector sandbox
// carries besides the host bind: the isolation the Box's own argv gives up
// (network, env, store) and the ADR 0054 filter.
var bwrapSandboxRequired = [][]string{
	{"--unshare-all"}, {"--clearenv"}, {"--cap-drop", "ALL"}, {"--new-session"},
	{"--die-with-parent"}, {"--proc", "/proc"}, {"--dev", "/dev"},
	{"--remount-ro", "/"}, {"--seccomp", "3"},
}

// bwrapSandboxForbidden are the flags that would widen the sandbox past the one
// read-only host bind: more mounts, a secret in the env, or the network.
var bwrapSandboxForbidden = []string{
	"--bind", "--bind-try", "--dev-bind", "--dev-bind-try", "--ro-bind-try",
	"--bind-fd", "--ro-bind-fd", "--file", "--bind-data", "--ro-bind-data",
	"--overlay", "--overlay-src", "--ro-overlay", "--tmp-overlay",
	"--setenv", "--share-net", "--cap-add", "--userns", "--userns2", "--pidns",
	"--args", "--tmpfs", "--dir",
}

// BwrapSandboxViolations lists how a bwrap argv for a Connector sandbox departs
// from the isolation the sandbox promises: only host, the connector-host store
// path, bound read-only (never the whole /nix/store), no network or env, the
// seccomp filter on fd 3 (the one fd the adapter hands it), and connector-host
// as the program. The bwrap adapter contract and the seamtest fake share it.
func BwrapSandboxViolations(argv []string, host string) []string {
	var bad []string
	hasRun := func(run ...string) bool {
		for i := range argv {
			if slices.Equal(argv[i:min(i+len(run), len(argv))], run) {
				return true
			}
		}
		return false
	}
	for _, run := range slices.Concat(bwrapSandboxRequired, [][]string{{"--ro-bind", host, host}}) {
		if !hasRun(run...) {
			bad = append(bad, "lacks "+strings.Join(run, " "))
		}
	}
	if n := len(slices.DeleteFunc(slices.Clone(argv), func(a string) bool { return a != "--ro-bind" })); n > 1 {
		bad = append(bad, "binds more than the connector-host path")
	}
	if want := host + "/bin/connector-host"; len(argv) == 0 || argv[len(argv)-1] != want {
		bad = append(bad, "does not exec "+want)
	}
	for _, a := range argv {
		if slices.Contains(bwrapSandboxForbidden, a) {
			bad = append(bad, "carries forbidden "+a)
		}
		// The sandbox gets its one host path alone; a Box's whole-store bind
		// would hand the Connector every other package, secrets in builds included.
		if a == "/nix/store" {
			bad = append(bad, "binds the whole /nix/store")
		}
	}
	return bad
}
