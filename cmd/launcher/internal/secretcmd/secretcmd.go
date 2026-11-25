// Package secretcmd resolves a credential's `_CMD` form: run a secret-fetch
// command and take its trimmed stdout. The Launcher and the Daemon are
// separate binaries, so the value-free resolution logic lives here and each
// supplies its own runner.
package secretcmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// UnlockHint stays tool-agnostic because the command is opaque to spindrift:
// rbw, op, pass, vault, or anything else that prints a secret to stdout.
const UnlockHint = "your vault may be locked; unlock it (e.g. `rbw unlock`) and re-run"

// Resolve runs a secret-fetch command through run and returns its trimmed
// stdout. Errors stay value-free: the command's stdout and stderr may carry a
// partial secret, so neither ever reaches an error message or a log.
func Resolve(env, cmd string, run func(string) (string, error)) (string, error) {
	out, err := run(cmd)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() >= 0 {
			return "", fmt.Errorf("%s: command failed (exit %d) — %s", env, exitErr.ExitCode(), UnlockHint)
		}
		return "", fmt.Errorf("%s: command failed — %s", env, UnlockHint)
	}
	trimmed := strings.TrimRight(out, "\r\n")
	if trimmed == "" {
		return "", fmt.Errorf("%s: command produced no output — %s", env, UnlockHint)
	}
	return trimmed, nil
}

// Template renders a `--secret-cmd`/SECRET_CMD template for env, substituting
// {name} with the kebab-case env name. It mirrors lib/renderers.nix's toKebab,
// so the name matches each secret's vault item in harness.env.example.
func Template(tmpl, env string) string {
	kebab := strings.ToLower(strings.ReplaceAll(env, "_", "-"))
	return strings.ReplaceAll(tmpl, "{name}", kebab)
}

// Run runs cmd through the shell and returns its raw stdout, which the caller
// treats as the secret and never prints. Interactively the command inherits
// stdin and stderr so a vault tool's unlock prompt reaches the operator;
// otherwise stdin is /dev/null, so a blocking read fails fast.
func Run(cmd string, interactive bool) (string, error) {
	c := exec.Command("sh", "-c", cmd)
	if interactive {
		c.Stdin = os.Stdin
		c.Stderr = os.Stderr
	}
	out, err := c.Output()
	return string(out), err
}
