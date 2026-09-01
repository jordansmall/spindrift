package console

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/forge"
)

// Refresh queries tracker for the full open backlog, orders it by dispatch
// priority (ADR 0040), and wraps the result into a Msg — the adapter between
// the forge.IssueTracker seam and the pure Update, so Update itself never
// touches the network. The Backlog therefore renders in the same priority
// order the headless dispatch pool uses, without the view re-deriving it.
func Refresh(tracker forge.IssueTracker) Msg {
	issues, err := tracker.ListOpenIssues()
	forge.SortByPriority(issues, func(i forge.Issue) forge.Priority { return i.Priority })
	// Resolved fresh per call — tracker never varies, but there is no shared
	// caller-side scope to cache against. cf is nil: Refresh has no CodeForge
	// in scope, and none of the handles read here live on that side.
	caps := forge.ResolveCapabilities(nil, tracker, backend.Descriptor{}, backend.Descriptor{})
	return IssuesLoadedMsg{Issues: issues, Err: err, RecoverableCount: countRecoverable(caps, issues)}
}

// countRecoverable reports how many of issues carry caps' Recoverable
// dispatch-state label, from the already-fetched ListOpenIssues result rather
// than a second tracker query. A tracker with no LabeledTracker handle, or
// one leaving Recoverable unmapped (empty label), reports zero rather than
// matching every issue — see issueInState for why an unmapped label must
// never be treated as a filter.
func countRecoverable(caps forge.Capabilities, issues []forge.Issue) int {
	if caps.LabeledTracker == nil {
		return 0
	}
	label := caps.LabeledTracker.StateLabels().Label(forge.Recoverable)
	if label == "" {
		return 0
	}
	count := 0
	for _, iss := range issues {
		for _, l := range iss.Labels {
			if l == label {
				count++
				break
			}
		}
	}
	return count
}

// dogfoodPidFile is the pid-file dogfood.sh writes for the duration of its
// run, removed by an EXIT trap.
const dogfoodPidFile = ".spindrift/dogfood.pid"

// isProcessAlive is DogfoodNotice's liveness probe, a package-level seam so
// tests can stub a dead pid without racing the OS's pid allocator.
var isProcessAlive = func(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// DogfoodNotice checks whether pwd holds a pid-file naming a still-running
// process and wraps the result into a Msg — informational only, never a
// gate. A stale pid-file left by a crashed loop reports Live false, same as
// a missing or malformed one: the signal-0 probe distinguishes a live
// session from bare file presence.
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
// issue was unlabeled or already Dispatchable — and wraps the result into a
// Msg. A failed promotion (raced, closed, relabeled) never queues the issue.
//
// Two rejections happen before any TransitionState call, both because the
// backlog snapshot the operator is browsing can go stale under them:
//
// Closed on the tracker — a stray dispatch label on a closed issue must never
// relabel it back onto the dispatch lifecycle. Costs one extra Issue()
// round-trip per pick, paid deliberately: nothing cheaper reveals a single
// issue's live open/closed state.
//
// Already InProgress or Complete — the backlog exposes every open issue
// regardless of dispatch state, so an operator can pick an issue a Box is
// already working. Relabeling it Dispatchable would leave both labels present
// and let Discover's claim launch a second Box for the same issue; reclaiming
// a terminal issue is Terminate's job, not a stray pick's.
func PickIssue(tracker forge.IssueTracker, num, title string, kind Kind) Msg {
	iss, err := tracker.Issue(num)
	if err != nil {
		return PickDissolvedMsg{Number: num, Title: title, Reason: err.Error()}
	}
	if iss.State == forge.IssueClosed {
		return PickDissolvedMsg{Number: num, Title: title, Reason: alreadyReason(num, "closed")}
	}
	// Resolved once here for both terminal-state checks below, rather than
	// issueInState re-deriving it on each call.
	caps := forge.ResolveCapabilities(nil, tracker, backend.Descriptor{}, backend.Descriptor{})
	for _, state := range []forge.DispatchState{forge.InProgress, forge.Complete} {
		active, err := issueInState(tracker, caps, num, state)
		if err != nil {
			return PickDissolvedMsg{Number: num, Title: title, Reason: err.Error()}
		}
		if active {
			return PickDissolvedMsg{Number: num, Title: title, Reason: alreadyReason(num, dispatchStateName(state))}
		}
	}
	return transitionToDispatchable(tracker, num, title, kind)
}

// alreadyReason keeps PickIssue's closed-issue and terminal-state rejections
// reading as the same sentence shape.
func alreadyReason(num, name string) string {
	return "issue #" + num + " is already " + name
}

// transitionToDispatchable is PickIssue's promotion step alone, split out so
// PickAllReady can drive it without re-paying PickIssue's terminal-state
// checks — two wasted ListIssues round-trips when the caller already knows
// num is Dispatchable.
func transitionToDispatchable(tracker forge.IssueTracker, num, title string, kind Kind) Msg {
	if err := tracker.TransitionState(num, forge.Untriaged, forge.Dispatchable); err != nil {
		return PickDissolvedMsg{Number: num, Title: title, Reason: err.Error()}
	}
	return PickQueuedMsg{Number: num, Title: title, Kind: kind}
}

// issueInState reports whether num is currently in tracker's state list. Each
// adapter resolves state natively (labels, Jira workflow status), so this
// asks the tracker rather than comparing raw Issue.Labels.
//
// A tracker leaving state unmapped — an empty label string, e.g. research's
// Complete (ADR 0022), which reaches its terminal state through verdict
// labels — returns false without calling ListIssues. Skipping the round-trip
// is not an optimization: GitHub ignores an empty --label filter and Local's
// frontmatter.State == "" matches every untriaged issue, so querying an
// unmapped state would false-match every open issue and dissolve every pick.
//
// ListIssues caps a single page at forge.ResultPageLimit and silently drops
// the tail. PickIssue's double-box guard trusts "not found" to mean "not in
// this state", so a truncated page would wrongly declare num safe to pick. A
// page that hit the cap without containing num is therefore reported as an
// error rather than a false "not in state" — deliberately conservative: an
// exactly-at-the-limit state with num genuinely absent also errors, blocking
// a valid pick rather than risking a double-box.
//
// That fail-safe is skipped for a forge.FullyPaginated tracker, which walks
// every page itself, so a result at the cap is a proven-complete set rather
// than a truncated page.
func issueInState(tracker forge.IssueTracker, caps forge.Capabilities, num string, state forge.DispatchState) (bool, error) {
	if caps.LabeledTracker != nil && caps.LabeledTracker.StateLabels().Label(state) == "" {
		return false, nil
	}
	issues, err := tracker.ListIssues(state)
	if err != nil {
		return false, err
	}
	for _, iss := range issues {
		if iss.Number == num {
			return true, nil
		}
	}
	if caps.FullyPaginated != nil && caps.FullyPaginated.WalksAllPages() {
		return false, nil
	}
	if len(issues) >= forge.ResultPageLimit {
		return false, fmt.Errorf("issue #%s not found among %d %s issues — list may be truncated at the page limit, refusing to assume it's not", num, len(issues), dispatchStateName(state))
	}
	return false, nil
}

// dispatchStateName renders state for a PickDissolvedMsg's reason. It
// deliberately covers only the states PickIssue's terminal-state loop rejects
// on; the default is a real fallback for the terminal states a future caller
// could pass, not dead code. Callers own the "already X" framing, so only
// pass states that read correctly as terminal in that sentence.
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
// and picks each one. It is an explicit action on one snapshot of that set,
// never standing discovery: an issue that becomes Dispatchable after this
// call returns is not picked until the operator asks again.
//
// The loop drives transitionToDispatchable directly rather than PickIssue:
// every issue here was just read off the Dispatchable list, dispatch-state
// labels are mutually exclusive, and ListIssues only returns open issues, so
// PickIssue's closed and InProgress/Complete checks are known false — paying
// them would cost 2N extra round-trips to reconfirm known facts.
//
// That reopens a narrow TOCTOU window: an issue that goes InProgress or
// Complete between the snapshot and its turn in this loop is relabeled
// Dispatchable anyway. Accepted — a bulk "pick everything ready right now"
// gesture is inherently point-in-time, and this is the same single-snapshot
// race PickAllReady already accepts on the other side.
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
