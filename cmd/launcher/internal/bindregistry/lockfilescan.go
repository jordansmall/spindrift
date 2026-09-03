package bindregistry

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"spindrift.dev/launcher/internal/ecosystem"
)

// LockfileHit is one git-tracked ecosystem lockfile whose content names the
// run's Forwarder URL (issue #3199) -- a stale pin that survived whatever
// ecosystem tool wrote it, left over from a prior run or a manual edit.
type LockfileHit struct {
	Path       string // repo-relative
	Ecosystem  string
	MatchedURL string
}

// ScanLockfilesForForwarder walks every git-tracked path in the repo rooted
// at repoDir, matches each by basename against the shared ecosystem table
// (ecosystem.Table), and reports every match whose content contains
// the literal "127.0.0.1:<port>" -- the Forwarder's own fixed address
// (bindregistry.ForwarderPort). Matching happens by basename across every
// tracked path rather than a repo-root stat, since lockfiles nest (workspace
// members, nested Go modules).
func ScanLockfilesForForwarder(repoDir string, port int) ([]LockfileHit, error) {
	needle := fmt.Sprintf("127.0.0.1:%d", port)

	paths, err := trackedPaths(repoDir)
	if err != nil {
		return nil, err
	}

	var hits []LockfileHit
	for _, row := range ecosystem.Table {
		names := make(map[string]bool, len(row.LockfileNames))
		for _, name := range row.LockfileNames {
			names[name] = true
		}

		for _, path := range paths {
			if !names[filepath.Base(path)] {
				continue
			}

			content, err := os.ReadFile(filepath.Join(repoDir, path))
			if err != nil {
				if os.IsNotExist(err) {
					// git-tracked but missing from the working tree
					// (deleted, sparse-checkout) -- not this scan's
					// concern to surface.
					continue
				}
				return nil, err
			}

			if strings.Contains(string(content), needle) {
				hits = append(hits, LockfileHit{
					Path:       path,
					Ecosystem:  row.Name,
					MatchedURL: needle,
				})
			}
		}
	}

	return hits, nil
}

// trackedPaths returns every git-tracked path in the repo rooted at
// repoDir, repo-relative. -z: NUL-separated output, since a tracked path
// can itself contain a newline; empty output means an empty repo, not an
// error.
func trackedPaths(repoDir string) ([]string, error) {
	cmd := exec.Command("git", "-C", repoDir, "ls-files", "-z")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	raw := strings.Split(string(out), "\x00")
	paths := make([]string, 0, len(raw))
	for _, p := range raw {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}
