package dispatch

import (
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/outcome"
)

// PassLog names one log file belonging to a Dispatch's history — the
// initial run, a fix pass, or conflict-resolve — in the chronological order
// LogPaths returns them.
type PassLog struct {
	Label string
	Path  string
}

// LogPaths returns every pass log that exists on disk for issue number under
// pwd, in chronological order: the initial run, each fix pass (probed
// consecutively by number until one is missing), then conflict-resolve. A
// pass with no log on disk (never run, or rotated aside per issue #561) is
// omitted rather than reported as an empty entry — there is no central
// registry of which passes a Dispatch ran, so existence on disk is the only
// source of truth (#648).
func LogPaths(pwd, number string) []PassLog {
	var out []PassLog
	if p := logPathFor(pwd, number); fileExists(p) {
		out = append(out, PassLog{Label: "initial", Path: p})
	}
	for pass := 1; ; pass++ {
		p := fixLogPathFor(pwd, number, pass)
		if !fileExists(p) {
			break
		}
		out = append(out, PassLog{Label: fmt.Sprintf("fix-%d", pass), Path: p})
	}
	if p := conflictLogPathFor(pwd, number); fileExists(p) {
		out = append(out, PassLog{Label: "conflict-resolve", Path: p})
	}
	return out
}

// LastSelfReportFromLogs recovers, from disk, the driver's last genuine
// (non-synthetic) leading-token SPINDRIFT_OUTCOME self-report for issue
// number under pwd — the same signal outcomeResult (retry.go) surfaces as
// Result.SelfReport for a live dispatch, but reconstructed after the fact for
// callers like `spindrift recover` (issue #2225) whose original run's Box
// has long since exited.
//
// It walks LogPaths(pwd, num) in chronological order and keeps the last
// self-report found across all pass logs, so a later pass (e.g. a fix pass)
// overrides an earlier one exactly as outcomeResult does within one log.
// A per-pass scan error is reported to stderr in the house diagnostic style
// and does not abort the walk — later passes are still consulted. Returns
// (SelfReport{}, false) when no pass log carried a non-synthetic
// leading-token line at all.
func LastSelfReportFromLogs(pwd, num string) (outcome.SelfReport, bool) {
	var (
		last  outcome.SelfReport
		found bool
	)
	for _, pl := range LogPaths(pwd, num) {
		report, ok, err := outcome.LastSelfReportInLog(pl.Path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: self-report scan: %v\n", num, err)
			continue
		}
		if ok {
			last = report
			found = true
		}
	}
	return last, found
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
