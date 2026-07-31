package forgetest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/seambundle"
)

// Run runs `git -C dir args...`, failing t on error.
func Run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// RevParse returns the commit ref resolves to inside the repo at dir.
func RevParse(t *testing.T, dir, ref string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", ref).CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse %s in %s: %v: %s", ref, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// WriteFile writes contents to path, failing t on error.
func WriteFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// SeedRelayBundle clones bare, creates branch one commit ahead of base
// carrying a marker file, and writes a git bundle of base..branch to
// outboxDir/seambundle.FileName -- standing in for the Box's code-out.
// Returns branch's HEAD sha.
func SeedRelayBundle(t *testing.T, bare, base, outboxDir, branch string) string {
	t.Helper()
	work := t.TempDir()
	Run(t, "", "clone", bare, work)
	Run(t, work, "checkout", base)
	Run(t, work, "checkout", "-b", branch)
	WriteFile(t, filepath.Join(work, "feature.txt"), "feature\n")
	Run(t, work, "add", "feature.txt")
	Run(t, work, "commit", "-m", "feature")
	Run(t, work, "bundle", "create", filepath.Join(outboxDir, seambundle.FileName), base+".."+branch)
	return RevParse(t, work, branch)
}
