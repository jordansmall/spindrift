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
