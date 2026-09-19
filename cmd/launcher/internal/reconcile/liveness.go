package reconcile

import (
	"os"
	"slices"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/runner"
)

// staleAfter is how long a Box log may go unwritten before its issue counts as
// dead. It spans a normal Box run plus a fix pass or two, without stranding a
// crashed run's issue for a full day.
const staleAfter = 3 * time.Hour

// FSProbe is LivenessProbe's real implementation (issue #1432), reading log
// staleness from pwd and container presence from r.
type FSProbe struct {
	pwd string
	r   runner.Runner
	now func() time.Time
}

// NewFSProbe returns an FSProbe probing pwd's Box logs and r's containers.
func NewFSProbe(pwd string, r runner.Runner) *FSProbe {
	return &FSProbe{pwd: pwd, r: r, now: time.Now}
}

// LogStale reports whether num's most recent Box log has gone unwritten beyond
// staleAfter. An issue with no log on disk at all counts as stale, since there
// is no history to hold it live on.
func (p *FSProbe) LogStale(num string) bool {
	passes := dispatch.LogPaths(p.pwd, num)
	if len(passes) == 0 {
		return true
	}
	info, err := os.Stat(passes[len(passes)-1].Path)
	if err != nil {
		return true
	}
	return p.now().Sub(info.ModTime()) > staleAfter
}

// ContainerLive reports whether num's Box container is running, using the same
// dispatch.BoxName scheme Kill and OrphanedIssues use. A ListRunning failure
// reports (false, false): no evidence either way, not a confirmed absence.
func (p *FSProbe) ContainerLive(num string) (live, reachable bool) {
	names, err := p.r.ListRunning()
	if err != nil {
		return false, false
	}
	return slices.Contains(names, dispatch.BoxName(num)), true
}

var _ LivenessProbe = (*FSProbe)(nil)
