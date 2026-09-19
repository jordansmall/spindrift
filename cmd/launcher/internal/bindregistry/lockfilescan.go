package bindregistry

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"spindrift.dev/launcher/internal/ecosystem"
)

// LockfileHit is one git-tracked lockfile pinning the run's Forwarder URL, a
// stale pin left by a prior run or a manual edit (issue #3199).
type LockfileHit struct {
	Path       string // repo-relative
	Ecosystem  string
	MatchedURL string
}

// ScanLockfilesForForwarder reports every git-tracked lockfile under repoDir
// whose content contains the literal "127.0.0.1:<port>". It matches lockfiles
// by basename across every tracked path rather than checking only the repo
// root, since lockfiles nest in workspace members and nested Go modules.
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
					// Tracked but absent from the working tree
					// (deleted, sparse checkout); not this scan's concern.
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

// trackedPaths returns every git-tracked path under repoDir, repo-relative.
// -z keeps the output NUL-separated, since a tracked path can contain a
// newline. Empty output means an empty repo, not an error.
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
