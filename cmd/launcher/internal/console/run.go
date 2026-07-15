package console

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
)

// errNoDriver is DrillInMsg's Err when "d <num>" is issued with no Driver
// available — a launch-less session, or a Launcher built without a Factory.
var errNoDriver = errors.New("drill-in unavailable: no Driver configured")

// defaultPollInterval is the background backlog poll's fixed cadence when a
// Launcher doesn't override it (production always uses this) — slow enough
// to never spend the rate-limit window the session's Agents share (#647 AC5).
const defaultPollInterval = 90 * time.Second

// Run drives the console's read-render loop: load the backlog, render it,
// then repeatedly read one command per line from in and re-render until the
// operator quits or in runs out. Between operator commands, it also
// re-renders on two other triggers: launch signaling a refresh after its own
// tracker write (a claim, a settle, a promotion, #647 AC4) — a no-op when
// launch is nil, since there is then no Queue to write to — and a slow fixed
// background poll (#647 AC5), which runs regardless of launch, re-querying
// the backlog even in a launch-less session. It is the only place that
// touches a real terminal in production; tests drive it with a scripted
// io.Reader instead. launch is nil for a launch-less session (Pick still
// promotes and queues, but nothing runs); production wires a real Launcher.
func Run(tracker forge.IssueTracker, pwd string, in io.Reader, out io.Writer, launch *Launcher) error {
	m := NewModel()
	m = Update(m, DogfoodNotice(pwd))
	m = Update(m, Refresh(tracker))
	m = syncQueue(m, launch, pwd)
	fmt.Fprint(out, View(m))

	scanner := bufio.NewScanner(in)
	lines := make(chan string)
	scanDone := make(chan error, 1)
	go func() {
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		// Written before the close so a receiver observing lines closed can
		// read scanDone without blocking — the goroutine's very last acts.
		scanDone <- scanner.Err()
		close(lines)
	}()

	var refresh <-chan struct{}
	if launch != nil {
		refresh = launch.Refreshes()
	}
	interval := defaultPollInterval
	if launch != nil && launch.pollInterval > 0 {
		interval = launch.pollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for !m.Quitting {
		select {
		case line, ok := <-lines:
			if !ok {
				// Input is exhausted, not a "q" — the pump goroutine has
				// already sent its result, so this never blocks.
				if launch != nil {
					launch.Wait()
				}
				return <-scanDone
			}
			m = applyCommand(m, tracker, pwd, launch, line)
		case <-refresh:
			m = doRefresh(m, tracker, pwd, launch)
		case <-ticker.C:
			// A held pick's blocker can clear out-of-band — another agent,
			// a human merge — with no sibling Dispatch in this session left
			// to settle and trigger Discover's own refill. The poll is the
			// only remaining re-evaluation trigger once the drain has gone
			// idle, so nudge it too (a no-op via l.launching when a drain
			// is already running or nothing is queued/held) (#650).
			if launch != nil {
				launch.tryLaunch(tracker, pwd)
			}
			m = doRefresh(m, tracker, pwd, launch)
		}
		if !m.Quitting {
			m = syncQueue(m, launch, pwd)
			fmt.Fprint(out, View(m))
		}
	}
	if launch != nil {
		launch.Wait()
	}
	// A "q"/"quit" command may leave the scan goroutine mid-Scan (or blocked
	// sending a line nobody will ever receive) if operator input carried
	// unread lines past the quit command — waiting on scanDone here could
	// hang forever, so a clean quit reports no scan error rather than racing
	// or blocking on the scanner it no longer owns.
	return nil
}

// doRefresh re-queries tracker for the backlog and, if a transcript is open,
// reloads it too — the effect "r"/"refresh" always had, now shared with the
// two triggers that fire it without an explicit command.
func doRefresh(m Model, tracker forge.IssueTracker, pwd string, launch *Launcher) Model {
	m = Update(m, Refresh(tracker))
	if m.DrillIn != nil {
		if drv := driverOf(launch); drv != nil {
			m = Update(m, DrillIn(drv, pwd, m.DrillIn.Number))
		}
	}
	return m
}

// syncQueue installs launch's live Queue state onto m, so every render —
// not just the one right after a pick — reflects claim/run/settle/dissolve
// transitions that happen entirely on the background Queue. Every running
// row's Heartbeat is also refreshed from its on-disk log on the way in
// (#647 AC2) — a plain local read, unlike the backlog refresh, so it is not
// gated behind the write/poll-triggered cadence the tracker's rate limit
// forces on Refresh. A nil launch leaves m untouched: Picks then tracks only
// what PickQueuedMsg/PickFailedMsg/UnpickMsg applied directly.
func syncQueue(m Model, launch *Launcher, pwd string) Model {
	if launch == nil {
		return m
	}
	picks := launch.Queue.Snapshot()
	if drv := driverOf(launch); drv != nil {
		for i := range picks {
			if picks[i].State == PickRunning {
				picks[i].Heartbeat = RunningHeartbeat(drv, pwd, picks[i].Number)
			}
		}
	}
	return Update(m, QueueSnapshotMsg{Picks: picks})
}

// applyCommand parses one line of operator input into a Msg and applies it.
// Recognized commands: "q"/"quit" to exit, "r"/"refresh" to re-query the
// tracker, "f" (bare) to clear the label filter, "f <text>" to set it,
// "p <num>" to pick an issue, "u <num>" to unpick a queued one,
// "k"/"kill"/"terminate" <num> to arm a terminate confirm (ADR 0024, issue
// #649) — the next line is read as its y/N answer instead of a new command,
// see applyTerminateConfirm. A successful pick also lands on launch.Queue
// and kicks off a background launch attempt, when launch is non-nil.
func applyCommand(m Model, tracker forge.IssueTracker, pwd string, launch *Launcher, line string) Model {
	if m.PendingTerminate != "" {
		return applyTerminateConfirm(m, tracker, launch, line)
	}
	cmd, arg, _ := strings.Cut(strings.TrimSpace(line), " ")
	switch cmd {
	case "q", "quit":
		return Update(m, QuitMsg{})
	case "r", "refresh":
		return doRefresh(m, tracker, pwd, launch)
	case "f", "filter":
		return Update(m, FilterChangedMsg{Filter: arg})
	case "p", "pick":
		if arg == "" {
			return m
		}
		msg := PickIssue(tracker, arg, titleOf(m, arg), KindWork)
		return applyPickMsg(m, tracker, pwd, launch, msg)
	case "pa", "pick-all-ready":
		for _, msg := range PickAllReady(tracker) {
			m = applyPickMsg(m, tracker, pwd, launch, msg)
		}
		return m
	case "u", "unpick":
		if arg == "" {
			return m
		}
		if launch != nil {
			launch.Queue.Remove(arg)
		}
		return Update(m, UnpickMsg{Number: arg})
	case "d", "drill":
		if arg == "" {
			return m
		}
		drv := driverOf(launch)
		if drv == nil {
			return Update(m, DrillInMsg{Number: arg, Err: errNoDriver})
		}
		return Update(m, DrillIn(drv, pwd, arg))
	case "t", "toggle":
		return Update(m, DrillInToggleMsg{})
	case "x", "close":
		return Update(m, DrillInCloseMsg{})
	case "k", "kill", "terminate":
		if arg == "" || launch == nil {
			return m
		}
		return Update(m, TerminateRequestedMsg{Number: arg})
	default:
		return m
	}
}

// applyTerminateConfirm interprets one line of input as the operator's
// answer to a pending "terminate #N? [y/N]" prompt: "y"/"yes"
// (case-insensitive) calls Launcher.Terminate and confirms; anything else
// declines and takes no action. Called instead of the normal command switch
// whenever m.PendingTerminate is set, so a stray issue number the operator
// types next is never misread as a new command.
func applyTerminateConfirm(m Model, tracker forge.IssueTracker, launch *Launcher, line string) Model {
	num := m.PendingTerminate
	answer := strings.ToLower(strings.TrimSpace(line))
	if answer != "y" && answer != "yes" {
		return Update(m, TerminateCancelledMsg{})
	}
	if launch != nil {
		if err := launch.Terminate(tracker, num); err != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: terminate: %v\n", num, err)
		}
	}
	return Update(m, TerminateConfirmedMsg{Number: num})
}

// applyPickMsg lands one PickIssue-adapter result (from a single "p <num>"
// or one issue of a "pa" batch) onto both launch.Queue and m — shared by
// both commands so a bulk pick's per-issue bookkeeping matches a single
// pick's exactly.
func applyPickMsg(m Model, tracker forge.IssueTracker, pwd string, launch *Launcher, msg Msg) Model {
	if launch != nil {
		switch msg := msg.(type) {
		case PickQueuedMsg:
			launch.Queue.Add(Pick{Number: msg.Number, Title: msg.Title, Kind: msg.Kind, State: PickQueued})
			launch.tryLaunch(tracker, pwd)
			launch.signalRefresh()
		case PickFailedMsg:
			// Landed on Queue too (already dissolved), not just Model via
			// the Update below — Run's per-render resync overwrites
			// Model.Picks from Queue, so a row that only ever touched
			// Update would vanish on the very next render.
			launch.Queue.Add(Pick{Number: msg.Number, Title: msg.Title, State: PickDissolved, Reason: msg.Reason})
		}
	}
	return Update(m, msg)
}

// driverOf returns the Driver a Launcher's Factory was constructed with, or
// nil when no Driver is available (a launch-less session, or a Launcher
// built without a Factory) — driver-less callers get errNoDriver instead of
// a nil-interface panic on drv.RenderTranscript.
func driverOf(launch *Launcher) driver.Driver {
	if launch == nil || launch.Factory == nil {
		return nil
	}
	return launch.Factory.Driver()
}

// titleOf returns num's title from m.All, or "" when the backlog hasn't
// (yet) loaded it — Pick still promotes and queues by number alone.
func titleOf(m Model, num string) string {
	for _, iss := range m.All {
		if iss.Number == num {
			return iss.Title
		}
	}
	return ""
}
