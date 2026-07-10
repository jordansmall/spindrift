// Package dispatch is the per-issue execution module (issue #441): every Box
// launched for one issue — initial run, fix passes, conflict-resolve — plus
// its results and its driver-cache entry, from claim to verdict. No caller
// outside this package constructs a runner.Box, opens an issue log file for
// writing, or classifies a Driver exit directly.
package dispatch

import (
	"os"
	"strconv"
	"strings"
)

// Config carries the subset of launcher config a Dispatch needs to build a
// Box's env and drive its retry policy.
type Config struct {
	// BoxEnvVars is a space-separated list of env var names forwarded from
	// the ambient environment into every Box (schema boxEnv=true entries).
	BoxEnvVars string

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
}

// buildBoxEnv assembles the env map forwarded into a Box. It combines the
// schema boxEnv=true vars (read from the ambient env by name) with per-issue
// vars.
func buildBoxEnv(cfg Config, number, title string, fixPass int, ciFailureSummary string) map[string]string {
	env := make(map[string]string)
	for _, name := range strings.Fields(cfg.BoxEnvVars) {
		env[name] = os.Getenv(name)
	}
	env["ISSUE_NUMBER"] = number
	env["ISSUE_TITLE"] = title
	if fixPass > 0 {
		env["FIX_PASS"] = strconv.Itoa(fixPass)
	}
	if ciFailureSummary != "" {
		env["CI_FAILURE_SUMMARY"] = ciFailureSummary
	}
	return env
}
