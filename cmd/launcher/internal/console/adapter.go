package console

import (
	"os"
	"path/filepath"

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

// DogfoodNotice checks whether a live dogfood pid-file exists under pwd and
// wraps the result into a Msg — informational only, never a gate.
func DogfoodNotice(pwd string) Msg {
	_, err := os.Stat(filepath.Join(pwd, dogfoodPidFile))
	return DogfoodNoticeMsg{Live: err == nil}
}

// PickIssue promotes num through the Untriaged->Dispatchable transition —
// the Pick's human-launch-button record, durable on the tracker whether the
// issue was unlabeled or already Dispatchable (the transition is a no-op
// relabel in the latter case) — and wraps the result into a Msg. A failed
// promotion (raced, closed, relabeled) never queues the issue.
func PickIssue(tracker forge.IssueTracker, num, title string, kind Kind) Msg {
	if err := tracker.TransitionState(num, forge.Untriaged, forge.Dispatchable); err != nil {
		return PickFailedMsg{Number: num, Title: title, Reason: err.Error()}
	}
	return PickQueuedMsg{Number: num, Title: title, Kind: kind}
}

// PickAllReady queries tracker for exactly the issues currently Dispatchable
// and picks each one — the "pick all ready" bulk gesture (#647 AC3). It is
// an explicit action on one snapshot of the tracker's Dispatchable set, never
// standing discovery: an issue that becomes Dispatchable after this call
// returns is not picked until the operator asks again. Each issue picks
// through the same PickIssue adapter a single "p <num>" uses, so an
// already-Dispatchable issue's promotion is the same idempotent relabel.
func PickAllReady(tracker forge.IssueTracker) []Msg {
	issues, err := tracker.ListIssues(forge.Dispatchable)
	if err != nil {
		return nil
	}
	msgs := make([]Msg, len(issues))
	for i, iss := range issues {
		msgs[i] = PickIssue(tracker, iss.Number, iss.Title, KindWork)
	}
	return msgs
}
