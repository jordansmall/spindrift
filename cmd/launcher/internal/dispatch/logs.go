package dispatch

import (
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/outcome"
)

// PassLog names one log file in a Dispatch's history, in the order LogPaths
// returns them. outcome.PassLog is the single source of truth for the shape.
type PassLog = outcome.PassLog

// LogPaths returns every pass log on disk for issue number under pwd, in
// chronological order: initial, each fix pass, conflict-resolve. No registry
// records which passes a Dispatch ran, so existence on disk decides (#648),
// and a pass with no log (never run, or rotated aside per #561) is omitted
// rather than reported as an empty entry.
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

// AllAttemptLogPaths returns every attempt log for issue number under pwd in
// chronological order: each pass's rotated-aside siblings (#561, labeled
// "<pass>.<N>") oldest first, then its bare current log. A P.N sibling
// belongs to this run only because Dispatch.Run or an explicit
// EnsureRunLineage quarantined any earlier run's logs first (#2575).
func AllAttemptLogPaths(pwd, number string) []PassLog {
	var out []PassLog

	appendPass := func(label, path string) {
		for n := 1; ; n++ {
			candidate := fmt.Sprintf("%s.%d", path, n)
			if !fileExists(candidate) {
				break
			}
			out = append(out, PassLog{Label: fmt.Sprintf("%s.%d", label, n), Path: candidate})
		}
		if fileExists(path) {
			out = append(out, PassLog{Label: label, Path: path})
		}
	}

	appendPass("initial", logPathFor(pwd, number))
	for pass := 1; ; pass++ {
		p := fixLogPathFor(pwd, number, pass)
		if !fileExists(p) && !fileExists(fmt.Sprintf("%s.1", p)) {
			break
		}
		appendPass(fmt.Sprintf("fix-%d", pass), p)
	}
	appendPass("conflict-resolve", conflictLogPathFor(pwd, number))

	return out
}

// ResolveFromLogs rebuilds the single Resolved outcome for issue number under
// pwd from the pass logs on disk, for callers like `spindrift recover` (issue
// #2225) whose original run's Box has long since exited. kind is forwarded to
// outcome.Resolve unchanged ("" normalizes to "work").
func ResolveFromLogs(pwd, num, kind string) (outcome.Resolved, error) {
	return outcome.Resolve(LogPaths(pwd, num), kind)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
