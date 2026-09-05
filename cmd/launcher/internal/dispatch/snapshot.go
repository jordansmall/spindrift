package dispatch

import (
	"fmt"
	"os"
	"path/filepath"
)

// HostSnapshotDirFor returns the host-side directory issue-read snapshot
// files live under, mirroring HostLogDirFor's shape.
func HostSnapshotDirFor(pwd string) string {
	return filepath.Join(pwd, ".spindrift", "snapshots")
}

// SnapshotPathFor returns the host path of number's frozen issue-read
// snapshot file.
func SnapshotPathFor(pwd, number string) string {
	return filepath.Join(HostSnapshotDirFor(pwd), "issue-"+number+".md")
}

// writeIssueSnapshot resolves number's frozen issue-read text via resolve
// (cfg.IssueSnapshot) and writes it to SnapshotPathFor(pwd, number),
// creating the snapshot directory if needed. Returns the empty string, nil
// when resolve is nil -- the snapshot-disabled case (a research dispatch, or
// a test Config that never wires IssueSnapshot) -- so callers can treat an
// empty returned path as "no mount" exactly like OutboxDir's own
// empty-omits-the-mount convention.
//
// A resolve failure (e.g. a transient GitHub API error) propagates as a
// real error rather than being swallowed -- the box would otherwise start
// with NO issue text at all, since every issue-read fragment now reads this
// file instead of the tracker.
func writeIssueSnapshot(resolve func(number string) (string, error), pwd, number string) (string, error) {
	if resolve == nil {
		return "", nil
	}
	text, err := resolve(number)
	if err != nil {
		return "", fmt.Errorf("resolve issue snapshot: %w", err)
	}
	dir := HostSnapshotDirFor(pwd)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create snapshot dir: %w", err)
	}
	path := SnapshotPathFor(pwd, number)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return "", fmt.Errorf("write issue snapshot: %w", err)
	}
	return path, nil
}

// snapshotPathForFix returns the frozen issue-read snapshot path Fix should
// mount: SnapshotPathFor(d.pwd, d.number) unchanged when that file already
// exists (Run froze it earlier this logical run -- do not re-resolve, issue
// #2547 review finding), "" for a research dispatch (which never calls Fix
// anyway, ADR 0022), and otherwise a fresh writeIssueSnapshot resolve/write:
// the agent-recover shape, where a Dispatch is built straight into Fix via
// SettleAdopted and never calls Run in that checkout, so nothing ever froze
// a file for it to reuse.
func (d *Dispatch) snapshotPathForFix() (string, error) {
	path := SnapshotPathFor(d.pwd, d.number)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	if d.cfg.Kind == "research" {
		return "", nil
	}
	return writeIssueSnapshot(d.cfg.IssueSnapshot, d.pwd, d.number)
}
