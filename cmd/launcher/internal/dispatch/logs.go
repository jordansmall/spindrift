package dispatch

import (
	"fmt"
	"os"
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

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
