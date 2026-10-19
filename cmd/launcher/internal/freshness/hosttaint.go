// hosttaint.go holds NonConverging, the repeat-stale check that gates the
// host-taint diagnostic (Artifact.HostTaintDiagnostic).
package freshness

// NonConverging reports whether staleRev repeats the rev the prior launcher
// process already exited stale on, meaning a rebuild against the same base tip
// left it stale. An empty staleRev, meaning no rev was fetched, is never
// non-converging.
func NonConverging(staleRev, priorStaleRev string) bool {
	return staleRev != "" && staleRev == priorStaleRev
}
