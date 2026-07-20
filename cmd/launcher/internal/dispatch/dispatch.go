// Package dispatch is the per-issue execution module (issue #441): every Box
// launched for one issue — initial run, fix passes, conflict-resolve — plus
// its results and its driver-cache entry, from claim to verdict. No caller
// outside this package constructs a runner.Box, opens an issue log file for
// writing, or classifies a Driver exit directly.
package dispatch

import (
	"io"
	"os"
	"strconv"
	"strings"
)

// Config carries the subset of launcher config a Dispatch needs to build a
// Box's env and drive its retry policy.
type Config struct {
	// BoxEnvVars is a space-separated list of env var names forwarded into
	// every Box (schema boxEnv=true entries).
	BoxEnvVars string

	// ResolveEnv resolves one BoxEnvVars name to its forwarded value.
	// Defaults to os.Getenv when nil (every pre-#625 caller and test).
	// main.go wires this to the same document/flag/env chain loadConfig()
	// uses (getenvSchema), so a boxEnv knob's document-baked value still
	// reaches the Box even when the operator sets it nowhere (ADR 0020: the
	// wrapper exports no per-var env any more).
	ResolveEnv func(name string) string

	// TransientRetryMax caps both the hold-cycle count (429 with a known
	// reset) and the backoff-retry count (other transients) before a
	// dispatch gives up.
	TransientRetryMax int

	// TransientBackoffSecs is the linear backoff unit for non-hold
	// transients: attempt N waits TransientBackoffSecs*N.
	TransientBackoffSecs int

	// HoldJitterSecs is added to a rate-limit hold's wait, and is the whole
	// wait when the known reset time has already passed.
	HoldJitterSecs int

	// DriverSessionCacheDir is the selected Driver's declared in-box
	// session-cache mount target (ADR 0009). Empty when the Driver declares
	// none, in which case the Factory creates no per-issue cache directory
	// at all -- there is nowhere in-box to mount it (issue #448).
	DriverSessionCacheDir string

	// Kind is the dispatch kind ("work" or "research", ADR 0022) forwarded
	// into every Box as DISPATCH_KIND, so the entrypoint can select its
	// prompt and skip clone-branch/PR/CI phases for research. Empty defaults
	// to "work" in buildBoxEnv, matching every pre-existing (kind-unaware)
	// construction site.
	Kind string

	// CodeForge is the CODE_FORGE knob value. runOnce consults it to decide
	// whether a Box needs a writable outbox directory at all (ADR 0033):
	// only "local" ever mounts one, so every other value (every
	// pre-existing construction site) skips creating
	// .spindrift/outbox/<num> entirely rather than leaving a harmless but
	// pointless empty directory behind on every dispatch.
	CodeForge string

	// OpenPRForIssue reports whether an open PR already exists for the
	// issue's agent branch. Consulted before a zero-exit, no-outcome box is
	// held-and-retried on a transient classification (issue #565), so a box
	// whose work already landed a PR is never re-run -- the same guard
	// settle's status=missing path applies. Always set by the sole
	// production constructor (dispatchConfig); a push-only Code Forge with
	// no PR lookup is handled inside that closure (ResolveOpenPR resolves
	// to Found: false there), not by leaving this field nil -- callers may
	// rely on it being non-nil.
	OpenPRForIssue func(number string) (bool, error)

	// HeartbeatOut is the human-facing sink every Box's heartbeat writer
	// echoes to, alongside its unconditional pass-log file capture. Nil
	// defaults to os.Stdout in box.go (every pre-#1583 caller and test). The
	// console entry point sets this to io.Discard via Factory.SetHeartbeatOut
	// -- Bubble Tea owns the terminal in alt-screen/raw mode there, and a
	// bare-\n heartbeat line stairsteps down the screen instead of returning
	// to column 0, while the sidebar activity feed already re-renders the
	// same lines by independently re-reading the pass log from disk.
	HeartbeatOut io.Writer
}

// buildBoxEnv assembles the env map forwarded into a Box. It combines the
// schema boxEnv=true vars (read from the ambient env by name) with per-issue
// vars.
func buildBoxEnv(cfg Config, number, title string, fixPass int, ciFailureSummary string) map[string]string {
	resolve := cfg.ResolveEnv
	if resolve == nil {
		resolve = os.Getenv
	}
	env := make(map[string]string)
	for _, name := range strings.Fields(cfg.BoxEnvVars) {
		env[name] = resolve(name)
	}
	env["ISSUE_NUMBER"] = number
	env["ISSUE_TITLE"] = title
	kind := cfg.Kind
	if kind == "" {
		kind = "work"
	}
	env["DISPATCH_KIND"] = kind
	if fixPass > 0 {
		env["FIX_PASS"] = strconv.Itoa(fixPass)
	}
	if ciFailureSummary != "" {
		env["CI_FAILURE_SUMMARY"] = ciFailureSummary
	}
	return env
}
