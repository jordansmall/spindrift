// Package toolchain is the Box's toolchain decision (issue #4297): which
// ecosystem the Target declares, whether its flake offers a devShell the
// Driver runs inside, and the prefetch hook's command.
package toolchain

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"spindrift.dev/launcher/internal/bindregistry"
)

// killWaitDelay is how long a cancelled nix gets after SIGTERM before it is
// killed, so a hung probe cannot stall the Box.
const killWaitDelay = 5 * time.Second

// Probe is the outcome of probing the Target's flake.nix for a devShell.
type Probe int

const (
	// NoFlake: no flake.nix, so nothing was probed and nothing is logged.
	NoFlake Probe = iota
	// Found: the flake offers the devShell.
	Found
	// Absent: the flake lacks the devShell, nix is missing, nix develop
	// failed, or the timeout was unparsable.
	Absent
	// TimedOut: the probe outlived its timeout.
	TimedOut
)

// Decision is the Box's toolchain decision for one Target checkout.
type Decision struct {
	Ecosystem string // bindregistry.Classify's classification, "" when none
	Probe     Probe
	Name      string // devShell attr; "default" when none was configured
	Timeout   string // DEV_SHELL_PROBE_TIMEOUT as given, for the timed-out line
}

// Devshell reports whether the Driver runs its lifecycle inside nix develop.
func (d Decision) Devshell() bool { return d.Probe == Found }

// Nix runs `nix args...` in dir with stdout to the Box's stdout and stderr
// discarded; ctx bounds it.
type Nix func(ctx context.Context, dir string, args ...string) error

// RunNix is the production Nix, writing nix's stdout to stdout (nil discards).
func RunNix(stdout io.Writer) Nix {
	return func(ctx context.Context, dir string, args ...string) error {
		cmd := exec.CommandContext(ctx, "nix", args...)
		cmd.Dir = dir
		cmd.Stdout = stdout
		// SIGTERM lets nix clean up; killWaitDelay bounds a nix that ignores
		// it or holds the pipes open.
		cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
		cmd.WaitDelay = killWaitDelay
		return cmd.Run()
	}
}

// Decide classifies workDir and probes its flake.nix for .#name with
// `nix develop .#<name> --command true`, bounded by timeout: a number with an
// optional s, m, h or d suffix (seconds when bare; 0 is unbounded). The hint
// (see Hint) and the probing line go to w before nix runs, since the probe can
// take minutes; the outcome is Decision.Line.
func Decide(ctx context.Context, w io.Writer, workDir, name, timeout, prefetch string, nix Nix) Decision {
	if name == "" {
		name = "default"
	}
	d := Decision{Ecosystem: bindregistry.Classify(workDir), Name: name, Timeout: timeout}
	if hint := d.Hint(prefetch); hint != "" {
		fmt.Fprintln(w, hint)
	}
	if info, err := os.Stat(filepath.Join(workDir, "flake.nix")); err != nil || info.IsDir() {
		return d
	}
	d.Probe = Absent
	fmt.Fprintln(w, "==> flake.nix found in cloned repo; probing for devShell")

	secs, ok := parseTimeout(timeout)
	if !ok {
		return d
	}
	if secs > 0 && secs < float64(math.MaxInt64/int64(time.Second)) {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(secs*float64(time.Second)))
		defer cancel()
	}

	switch err := nix(ctx, workDir, "develop", ".#"+name, "--command", "true"); {
	case err == nil:
		d.Probe = Found
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		d.Probe = TimedOut
	}
	return d
}

// parseTimeout reads a number with an optional s, m, h or d suffix as seconds.
// NaN and negatives are rejected.
func parseTimeout(s string) (float64, bool) {
	mult := 1.0
	if n := len(s); n > 0 {
		if m, ok := map[byte]float64{'s': 1, 'm': 60, 'h': 3600, 'd': 86400}[s[n-1]]; ok {
			mult, s = m, s[:n-1]
		}
	}
	secs, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(secs) || secs < 0 {
		return 0, false
	}
	return secs * mult, true
}

// PrefetchWarning is the line for a failed prefetch hook; the run carries on.
func PrefetchWarning(err error) string {
	return fmt.Sprintf("==> WARNING: prefetch hook failed (%v) — continuing", err)
}

// Line is the probe's outcome line; "" for NoFlake.
func (d Decision) Line() string {
	switch d.Probe {
	case NoFlake:
		return ""
	case Found:
		return "==> devShell found — lifecycle will run inside nix develop"
	case TimedOut:
		return "==> devShell probe timed out (" + d.Timeout + "s) — using baked toolchain"
	default:
		return "==> no devShell in flake (or nix develop failed) — using baked toolchain"
	}
}

// Hint is the toolchain nudge line Decide prints; "" when prefetch is set or no ecosystem
// was detected.
func (d Decision) Hint(prefetch string) string {
	if prefetch != "" || d.Ecosystem == "" {
		return ""
	}
	return "==> hint: " + d.Ecosystem + " project detected; set 'prefetch' to warm dependency caches per run, or 'packages' to bake a toolchain into the image"
}

// PrefetchCmd is the command running the prefetch hook in workDir, nil when
// hook is empty. The hook runs in a child bash (issue #3943) so a cd, exit or
// set inside it cannot leak. Inside a devShell, harnessPath (the PATH at
// probe time) is prepended because nix develop rewrites PATH.
func (d Decision) PrefetchCmd(hook, workDir, harnessPath string) *exec.Cmd {
	if hook == "" {
		return nil
	}
	var cmd *exec.Cmd
	if d.Devshell() {
		cmd = exec.Command("nix", "develop", ".#"+d.Name, "--command", "bash", "-c",
			`export PATH="$1:$PATH"; eval "$PREFETCH"`, "prefetch", harnessPath)
	} else {
		cmd = exec.Command("bash", "-c", hook)
	}
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "WORK_DIR="+workDir, "PREFETCH="+hook)
	return cmd
}
