package dispatch

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// boxNamePrefix is the prefix both BoxName and OrphanedIssues use, so the two
// directions of the naming scheme cannot drift apart.
const boxNamePrefix = "agent-issue-"

// BoxName returns the deterministic sandbox name a Dispatch launches issue
// number under. runOnce, Factory.Kill (issue #649) and reconcile's
// LivenessProbe (issue #1432) all call it, and the last two have no live
// *Dispatch to ask.
func BoxName(number string) string {
	return boxNamePrefix + number
}

// Kill force-stops and removes the sandbox for number, if any (ADR 0024,
// issue #649). It needs no *Dispatch, so a live Dispatch goroutine elsewhere
// in the process keeps running with its sandbox pulled out from under it.
func (f *Factory) Kill(number string) error {
	return f.runner.Kill(BoxName(number))
}

// OrphanedIssues returns the issue numbers of every sandbox the runner reports
// running, for Console startup orphan detection (issue #651): a crash or
// dropped SSH leaves these running with no live goroutine to account for them.
// A name that doesn't match the scheme, or whose suffix isn't a valid unsigned
// issue number, is skipped (issues #793, #1157).
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
		if _, err := strconv.ParseUint(num, 10, 64); err != nil {
			continue
		}
		nums = append(nums, num)
	}
	return nums, nil
}

// AppendTerminalLine appends note as its own line to number's most recently
// written pass log (ADR 0024, issue #649), so a drill-in sees where a run that
// never wrote SPINDRIFT_OUTCOME ended. It creates the initial run's log when
// no pass ever produced one.
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
