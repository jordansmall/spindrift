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

// Refresh queries the tracker for the open backlog and wraps it in a Msg.
// It sorts by dispatch priority (forge.SortByPriority, ADR 0040) so the
// Backlog renders in the order the headless dispatch pool uses, and it keeps
// the network out of the pure Update.
func Refresh(tracker forge.IssueTracker) Msg {
	issues, err := tracker.ListOpenIssues()
	forge.SortByPriority(issues, func(i forge.Issue) forge.Priority { return i.Priority })
	// Resolved fresh per call (issue #2946) because no shared caller-side scope
	// exists to cache it against. cf is nil: Refresh has no CodeForge in scope.
	caps := forge.ResolveCapabilities(nil, tracker, backend.Descriptor{}, backend.Descriptor{})
	return IssuesLoadedMsg{Issues: issues, Err: err, RecoverableCount: countRecoverable(caps, issues)}
}

// countRecoverable counts the issues carrying caps' Recoverable label, reading
// the already-fetched list rather than querying the tracker again. A missing
// LabeledTracker, or one that leaves Recoverable unmapped (empty label
// string), reports zero rather than matching every issue (#1742).
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

// dogfoodPidFile names the pid-file dogfood.sh writes at the start of its run
// and deletes from an EXIT trap.
const dogfoodPidFile = ".spindrift/dogfood.pid"

// isProcessAlive is a package-level seam so tests can stub a dead pid without
// racing the OS's pid allocator (#952).
var isProcessAlive = func(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// DogfoodNotice reports whether pwd holds a pid-file naming a running process.
// It is informational, never a gate. A stale pid-file left by a crashed loop
// whose EXIT trap never fired (#565) reports Live false, like a missing or
// malformed one, because the signal-0 probe distinguishes a live session from
// bare file presence.
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

// PickIssue promotes num from Untriaged to Dispatchable and wraps the result
// in a Msg. A failed promotion never queues the issue.
func PickIssue(tracker forge.IssueTracker, num, title string, kind Kind) Msg {
	// The backlog snapshot the operator browses goes stale the moment someone
	// closes the issue on the forge, and a dispatch label must never pull a
	// closed issue back onto the lifecycle (#1851). This extra round-trip is
	// the only way to learn a single issue's live open/closed state.
	iss, err := tracker.Issue(num)
	if err != nil {
		return PickDissolvedMsg{Number: num, Title: title, Reason: err.Error()}
	}
	if iss.State == forge.IssueClosed {
		return PickDissolvedMsg{Number: num, Title: title, Reason: alreadyReason(num, "closed")}
	}
	// Resolved once here for both checks below rather than by each
	// issueInState call (issue #2946).
	caps := forge.ResolveCapabilities(nil, tracker, backend.Descriptor{}, backend.Descriptor{})
	// ListOpenIssues exposes every open issue whatever its dispatch state, so an
	// operator can pick one a Box is already working. Relabeling it Dispatchable
	// would leave both labels present and let Discover's claim launch a second
	// Box (#707). Reclaiming a terminal issue is Terminate's job.
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

func alreadyReason(num, name string) string {
	return "issue #" + num + " is already " + name
}

// transitionToDispatchable is PickIssue's promotion step alone, split out so
// PickAllReady can skip the two ListIssues round-trips PickIssue's
// terminal-state checks cost when the caller already knows num is
// Dispatchable (#987).
func transitionToDispatchable(tracker forge.IssueTracker, num, title string, kind Kind) Msg {
	if err := tracker.TransitionState(num, forge.Untriaged, forge.Dispatchable); err != nil {
		return PickDissolvedMsg{Number: num, Title: title, Reason: err.Error()}
	}
	return PickQueuedMsg{Number: num, Title: title, Kind: kind}
}

// issueInState reports whether num is currently in tracker's state list. Each
// adapter resolves state through its own native mechanism (labels, Jira
// workflow status), so this asks the tracker instead of comparing
// Issue.Labels itself.
func issueInState(tracker forge.IssueTracker, caps forge.Capabilities, num string, state forge.DispatchState) (bool, error) {
	// A state the label family leaves unmapped (empty label, e.g. research's
	// Complete under ADR 0022) can never hold an issue, and querying it would
	// false-match every open issue and dissolve every pick: GitHub ignores an
	// empty --label filter, and Local's empty frontmatter State matches every
	// untriaged issue (#1742).
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
	// A FullyPaginated adapter (forgejo, jira, #2265) walks every page itself, so
	// a result at the cap is a proven-complete set and skips the fail-safe below,
	// which would otherwise block a legitimate pick.
	if caps.FullyPaginated != nil && caps.FullyPaginated.WalksAllPages() {
		return false, nil
	}
	// ListIssues silently truncates a page at the limit (#986), and the
	// double-box guard (#707) trusts "not found" to mean "not in this state",
	// so a full page without num is inconclusive. Erroring also blocks a valid
	// pick when a state holds exactly a full page and num is genuinely absent,
	// which is the cheaper mistake.
	if len(issues) >= forge.ResultPageLimit {
		return false, fmt.Errorf("issue #%s not found among %d %s issues — list may be truncated at the page limit, refusing to assume it's not", num, len(issues), dispatchStateName(state))
	}
	return false, nil
}

// dispatchStateName renders state for a PickDissolvedMsg's operator-facing
// reason. The default is a real fallback, not dead code: forge.Failed is
// terminal and a future caller could pass it. Callers own the "already X"
// framing, so pass only states that read as terminal in that sentence.
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

// PickAllReady picks every issue currently Dispatchable (#647 AC3). It acts on
// one snapshot, never as standing discovery: an issue that becomes
// Dispatchable after this call returns waits for the operator to ask again.
func PickAllReady(tracker forge.IssueTracker) []Msg {
	issues, err := tracker.ListIssues(forge.Dispatchable)
	if err != nil {
		return []Msg{PickDissolvedMsg{Title: "pick all ready", Reason: err.Error()}}
	}
	msgs := make([]Msg, len(issues))
	// Driving transitionToDispatchable directly skips PickIssue's checks: every
	// issue here came off the Dispatchable list, dispatch-state labels are
	// mutually exclusive (#707), and ListIssues returns only open issues, so
	// those 2N round-trips reconfirm known facts (#987, #1851). The TOCTOU
	// window it reopens is accepted; the gesture is point-in-time either way.
	for i, iss := range issues {
		msgs[i] = transitionToDispatchable(tracker, iss.Number, iss.Title, KindWork)
	}
	return msgs
}
