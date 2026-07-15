package dispatch

import (
	"fmt"
	"os"
)

// boxName returns the deterministic sandbox name a Dispatch launches issue
// number under. Shared between runOnce (which launches it) and Factory.Kill
// (Terminate, issue #649, which has no live *Dispatch to ask) so the two can
// never drift apart.
func boxName(number string) string {
	return "agent-issue-" + number
}

// Kill force-stops and removes the sandbox running (or last run) for number,
// if any — Terminate's reap step (ADR 0024, issue #649). It needs no
// *Dispatch: the box name is deterministic from number alone, so a live
// Dispatch goroutine elsewhere in the process is untouched by this call
// beyond losing its running sandbox out from under it.
func (f *Factory) Kill(number string) error {
	return f.runner.Kill(boxName(number))
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
