package console

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"spindrift.dev/launcher/internal/forge"
)

// Refresh queries tracker for the full open backlog and wraps the result
// into a Msg — the thin adapter between the forge.IssueTracker seam and the
// pure Update, so Update itself never touches the network.
func Refresh(tracker forge.IssueTracker) Msg {
	issues, err := tracker.ListOpenIssues()
	return IssuesLoadedMsg{Issues: issues, Err: err}
}

// dogfoodPidFile is the pid-file dogfood.sh writes at repo root for the
// duration of its run (`echo $$ > .dogfood.pid`, removed by an EXIT trap).
const dogfoodPidFile = ".dogfood.pid"

// isProcessAlive is DogfoodNotice's liveness probe, a package-level seam so
// tests can stub a dead pid without racing the OS's pid allocator (#952)
// instead of spawning and reaping a real process.
var isProcessAlive = func(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// DogfoodNotice checks whether pwd holds a pid-file naming a still-running
// process and wraps the result into a Msg — informational only, never a
// gate. A stale pid-file left behind by a crashed loop (EXIT trap never
// fired, #565) reports Live false, same as a missing or malformed one: a
// signal-0 probe on the parsed pid distinguishes a live session from bare
// file presence.
func DogfoodNotice(pwd string) Msg {
	raw, err := os.ReadFile(filepath.Join(pwd, dogfoodPidFile))
	if err != nil {
		return DogfoodNoticeMsg{Live: false}
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return DogfoodNoticeMsg{Live: false}
	}
	return DogfoodNoticeMsg{Live: isProcessAlive(pid)}
}

// PickIssue promotes num through the Untriaged->Dispatchable transition —
// the Pick's human-launch-button record, durable on the tracker whether the
// issue was unlabeled or already Dispatchable (the transition is a no-op
// relabel in the latter case) — and wraps the result into a Msg. A failed
// promotion (raced, closed, relabeled) never queues the issue.
//
// num is rejected outright, before any TransitionState call, if the tracker
// already has it InProgress or Complete: the backlog (ListOpenIssues) exposes
// every open issue regardless of dispatch state, so an operator can highlight
// and pick an issue a Box is already working. Relabeling it Dispatchable on
// top of its existing label would leave both present and let Discover's
// claim launch a second Box for the same issue (#707) — reclaiming a
// terminal issue is Terminate's job, not a stray pick's.
func PickIssue(tracker forge.IssueTracker, num, title string, kind Kind) Msg {
	for _, state := range []forge.DispatchState{forge.InProgress, forge.Complete} {
		active, err := issueInState(tracker, num, state)
		if err != nil {
			return PickDissolvedMsg{Number: num, Title: title, Reason: err.Error()}
		}
		if active {
			return PickDissolvedMsg{Number: num, Title: title, Reason: "issue #" + num + " is already " + dispatchStateName(state)}
		}
	}
	return transitionToDispatchable(tracker, num, title, kind)
}

// transitionToDispatchable is PickIssue's promotion step alone, split out so
// PickAllReady can drive it directly without re-paying PickIssue's
// terminal-state checks (#987) — the two ListIssues round-trips those checks
// cost are wasted work when the caller already knows num is Dispatchable.
func transitionToDispatchable(tracker forge.IssueTracker, num, title string, kind Kind) Msg {
	if err := tracker.TransitionState(num, forge.Untriaged, forge.Dispatchable); err != nil {
		return PickDissolvedMsg{Number: num, Title: title, Reason: err.Error()}
	}
	return PickQueuedMsg{Number: num, Title: title, Kind: kind}
}

// issueInState reports whether num is currently in tracker's state list —
// each IssueTracker adapter resolves state via its own native mechanism
// (GitHub/local/fake labels, Jira workflow status), so this asks the tracker
// rather than re-deriving state from a raw Issue.Labels comparison.
func issueInState(tracker forge.IssueTracker, num string, state forge.DispatchState) (bool, error) {
	issues, err := tracker.ListIssues(state)
	if err != nil {
		return false, err
	}
	for _, iss := range issues {
		if iss.Number == num {
			return true, nil
		}
	}
	return false, nil
}

// dispatchStateName renders state for a PickDissolvedMsg's operator-facing
// reason — InProgress and Complete are the only states PickIssue ever
// rejects on, so this deliberately doesn't cover the full enum.
func dispatchStateName(state forge.DispatchState) string {
	switch state {
	case forge.InProgress:
		return "in progress"
	case forge.Complete:
		return "complete"
	default:
		return "in a terminal state"
	}
}

// PickAllReady queries tracker for exactly the issues currently Dispatchable
// and picks each one — the "pick all ready" bulk gesture (#647 AC3). It is
// an explicit action on one snapshot of the tracker's Dispatchable set, never
// standing discovery: an issue that becomes Dispatchable after this call
// returns is not picked until the operator asks again.
//
// Unlike a single "p <num>" pick, the loop below drives
// transitionToDispatchable directly instead of going through PickIssue: every
// issue here was just read off the Dispatchable list, and dispatch-state
// labels are mutually exclusive (#707), so PickIssue's InProgress/Complete
// checks are guaranteed false for all of them — re-querying ListIssues twice
// per issue to reconfirm a fact already known wastes 2N network round-trips
// on top of the one ListIssues(Dispatchable) call above (#987). This
// reopens a narrow TOCTOU window — an issue that transitions to InProgress or
// Complete after the snapshot above but before its turn in this loop (e.g. a
// concurrent pick from another operator or automation) is relabeled
// Dispatchable anyway — accepted here because the bulk "pick everything
// ready right now" gesture is inherently a point-in-time batch, not a
// standing guarantee, and the window is the same single-snapshot race
// PickAllReady already accepts against issues becoming Dispatchable after it
// returns.
func PickAllReady(tracker forge.IssueTracker) []Msg {
	issues, err := tracker.ListIssues(forge.Dispatchable)
	if err != nil {
		return []Msg{PickDissolvedMsg{Title: "pick all ready", Reason: err.Error()}}
	}
	msgs := make([]Msg, len(issues))
	for i, iss := range issues {
		msgs[i] = transitionToDispatchable(tracker, iss.Number, iss.Title, KindWork)
	}
	return msgs
}
