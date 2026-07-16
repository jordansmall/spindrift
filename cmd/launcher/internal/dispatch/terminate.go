package dispatch

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// boxNamePrefix is boxName's deterministic prefix — shared with
// OrphanedIssues, the reverse direction of the same naming scheme.
const boxNamePrefix = "agent-issue-"

// boxName returns the deterministic sandbox name a Dispatch launches issue
// number under. Shared between runOnce (which launches it) and Factory.Kill
// (Terminate, issue #649, which has no live *Dispatch to ask) so the two can
// never drift apart.
func boxName(number string) string {
	return boxNamePrefix + number
}

// Kill force-stops and removes the sandbox running (or last run) for number,
// if any — Terminate's reap step (ADR 0024, issue #649). It needs no
// *Dispatch: the box name is deterministic from number alone, so a live
// Dispatch goroutine elsewhere in the process is untouched by this call
// beyond losing its running sandbox out from under it.
func (f *Factory) Kill(number string) error {
	return f.runner.Kill(boxName(number))
}

// OrphanedIssues returns the issue numbers of every sandbox the runner
// currently reports running, parsed from the deterministic boxName scheme —
// Console startup orphan detection (issue #651): a crash or dropped SSH
// leaves these running with no live goroutine in a fresh process to account
// for them. A name that doesn't match the scheme, or whose suffix isn't a
// valid issue number, is silently skipped (issue #793).
func (f *Factory) OrphanedIssues() ([]string, error) {
	names, err := f.runner.ListRunning()
	if err != nil {
		return nil, err
	}
	var nums []string
	for _, name := range names {
		num, ok := strings.CutPrefix(name, boxNamePrefix)
		if !ok {
			continue
		}
		if _, err := strconv.Atoi(num); err != nil {
			continue
		}
		nums = append(nums, num)
	}
	return nums, nil
}

// AppendTerminalLine appends note, as its own line, to number's most
// recently written pass log (LogPaths' last entry) — Terminate's Box-log
// record (ADR 0024, issue #649), so a drill-in reading the transcript sees
// where the run actually ended even though no SPINDRIFT_OUTCOME line was
// ever written. Falls back to creating the initial run's log when no pass
// ever produced one (Terminate landed before dispatch got that far).
func (f *Factory) AppendTerminalLine(number, note string) error {
	path := logPathFor(f.pwd, number)
	if passes := LogPaths(f.pwd, number); len(passes) > 0 {
		path = passes[len(passes)-1].Path
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = fmt.Fprintf(file, "\n[terminate] %s\n", note)
	return err
}
