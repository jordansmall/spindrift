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
