package dispatch

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"spindrift.dev/launcher/internal/terminate"
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
	// Close the latch before the reap, never after: a Dispatch that saw an
	// open latch goes on to create a container this reap has already swept
	// past, leaving a live Box owning an issue the abort released back to the
	// dispatchable pool (issue #3521).
	f.closeKillLatch(number)
	return f.runner.Kill(BoxName(number))
}

// armKillLatch mints a fresh open latch for number, replacing any latch a
// prior claim left closed. A kill applies to the claim that owned the issue
// when it landed, so a re-pick after a Console Terminate (issue #649) launches
// normally rather than inheriting that kill.
func (f *Factory) armKillLatch(number string) chan struct{} {
	f.killMu.Lock()
	defer f.killMu.Unlock()
	if f.killLatches == nil {
		f.killLatches = make(map[string]chan struct{})
	}
	ch := make(chan struct{})
	f.killLatches[number] = ch
	return ch
}

// closeKillLatch closes number's latch idempotently: Kill is legitimately
// called twice for one issue (a Console Terminate, then a signalled abort).
func (f *Factory) closeKillLatch(number string) {
	f.killMu.Lock()
	defer f.killMu.Unlock()
	ch := f.killLatchLocked(number)
	select {
	case <-ch:
	default:
		close(ch)
	}
}

func (f *Factory) killLatchLocked(number string) chan struct{} {
	if f.killLatches == nil {
		f.killLatches = make(map[string]chan struct{})
	}
	ch, ok := f.killLatches[number]
	if !ok {
		ch = make(chan struct{})
		f.killLatches[number] = ch
	}
	return ch
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

// AsReaper boxes f into terminate.Reaper, returning a nil interface rather
// than a typed nil for a nil Factory: a nil *Factory boxed unconditionally
// compares non-nil and defeats terminate.Reclaim's own nil guard
// (#3521/#3522).
func (f *Factory) AsReaper() terminate.Reaper {
	if f == nil {
		return nil
	}
	return f
}
