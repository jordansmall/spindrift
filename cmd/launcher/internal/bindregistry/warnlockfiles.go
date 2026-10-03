package bindregistry

import (
	"errors"
	"fmt"
	"io"
	"os"

	"spindrift.dev/launcher/internal/registrymanifest"
)

// WarnStaleLockfiles is the settle-time lockfile scan (issue #3199): it warns
// on w about any git-tracked lockfile in workDir still naming the run's
// Forwarder URL, a stale pin that would otherwise ship silently in the PR. It
// parses REGISTRY_PROXY_MANIFEST directly and never resolves the Forwarder
// readiness gate, which probes and can spawn the Forwarder; a settle-time scan
// must never do that. An absent manifest (registry proxy off) is silent, and
// every failure path only warns.
func WarnStaleLockfiles(w io.Writer, workDir string) {
	if _, err := registrymanifest.Parse(os.Getenv(registrymanifest.EnvVar)); err != nil {
		if errors.Is(err, registrymanifest.ErrAbsent) {
			return
		}
		fmt.Fprintln(w, "==> WARNING: REGISTRY_PROXY_MANIFEST is malformed, skipping the lockfile Forwarder-URL scan: "+err.Error())
		return
	}

	hits, err := ScanLockfilesForForwarder(workDir, ForwarderPort)
	if err != nil {
		fmt.Fprintln(w, "==> WARNING: lockfile Forwarder-URL scan failed, skipping: "+err.Error())
		return
	}

	for _, hit := range hits {
		fmt.Fprintln(w, "==> WARNING: "+hit.Ecosystem+" lockfile "+hit.Path+" still names the registry proxy Forwarder URL "+hit.MatchedURL+" — this will ship in the PR (issue #3199)")
	}
}
