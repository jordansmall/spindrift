package dispatch

import (
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/outcome"
)

// PassLog names one log file belonging to a Dispatch's history — the
// initial run, a fix pass, or conflict-resolve — in the chronological order
// LogPaths returns them. It is an alias for outcome.PassLog, the single
// source of truth for this shape.
type PassLog = outcome.PassLog

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

// AllAttemptLogPaths returns every attempt log a run produced for issue
// number under pwd, in chronological order -- including attempts a hold or
// transient-backoff retry rotated aside (issue #561), which LogPaths itself
// omits. For each pass LogPaths' probe walks (initial, each fix pass, then
// conflict-resolve), it first emits that pass's rotated-aside siblings
// oldest first (P.1, P.2, ... probed consecutively, stopping at the first
// missing suffix), labeled "<passLabel>.<N>", then the pass's own bare
// current log (if it exists) with the same bare label LogPaths uses. A pass
// with neither a rotated sibling nor a current log on disk contributes
// nothing -- existence on disk is the only source of truth (#648).
//
// Every P.N sibling this walk finds is guaranteed to be an attempt THIS run
// itself produced, never a leftover from an unrelated earlier run at the
// same path (a re-dispatch of the same issue in a persistent pwd -- agent-
// failed -> re-label, waves/continuous -- or issue #561's own "duplicate/
// collided launch" case) -- PROVIDED this issue's log lineage was
// established either by Dispatch.Run (which quarantines any pre-existing
// log for this issue out of this naming pattern before its very first
// attempt, box.go's quarantinePriorRunLogs) or, for a Dispatch that reaches
// this call without ever calling Run() itself -- main.go's recoverByNumber
// constructs one via Factory.New and, when settle finds an already-open PR
// (SettleAdopted) or an adoptable relayed branch, calls
// CumulativeUsage/UsageReport/Fix on it directly -- by an explicit
// Dispatch.EnsureRunLineage call first (issue #2575). Either establishes
// the same guarantee: by the time a P.N sibling exists under this scheme,
// only this run's own hold/backoff rotations could have put it there.
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

// ResolveFromLogs recovers, from disk, the single Resolved outcome for issue
// number under pwd by walking every pass log through the same outcome.Resolve
// seam a live dispatch's outcomeResult already applies to one log --
// reconstructed after the fact for callers like `spindrift recover` (issue
// #2225) whose original run's Box has long since exited. kind is forwarded to
// outcome.Resolve unchanged ("" normalizes to "work").
func ResolveFromLogs(pwd, num, kind string) (outcome.Resolved, error) {
	return outcome.Resolve(LogPaths(pwd, num), kind)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
