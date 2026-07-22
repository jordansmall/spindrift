package bundleout_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/bundleout"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/seambundle"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v (dir=%s): %v: %s", args, dir, err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newRepoWithFeatureBranch creates a repo with one commit on "main" and a
// "feature" branch one commit ahead, so base..branch is non-empty.
func newRepoWithFeatureBranch(t *testing.T) (dir, base, branch string) {
	t.Helper()
	dir = t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.name", "Test Bot")
	runGit(t, dir, "config", "user.email", "bot@example.com")
	writeFile(t, filepath.Join(dir, "base.txt"), "base\n")
	runGit(t, dir, "add", "base.txt")
	runGit(t, dir, "commit", "-m", "base")
	runGit(t, dir, "checkout", "-b", "feature")
	writeFile(t, filepath.Join(dir, "feature.txt"), "feature\n")
	runGit(t, dir, "add", "feature.txt")
	runGit(t, dir, "commit", "-m", "feature")
	return dir, "main", "feature"
}

// newRepoNoFeatureCommits creates a repo with one commit on "main" and a
// "feature" branch cut from the same tip, no commits ahead — so base..branch
// is empty, standing in for a Box that claimed ready but never committed.
func newRepoNoFeatureCommits(t *testing.T) (dir, base, branch string) {
	t.Helper()
	dir = t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.name", "Test Bot")
	runGit(t, dir, "config", "user.email", "bot@example.com")
	writeFile(t, filepath.Join(dir, "base.txt"), "base\n")
	runGit(t, dir, "add", "base.txt")
	runGit(t, dir, "commit", "-m", "base")
	runGit(t, dir, "checkout", "-b", "feature")
	return dir, "main", "feature"
}

// TestRun_NonEmptyRange_CreatesBundle verifies Run bundles base..branch into
// outboxDir/seambundle.FileName when the range holds commits — the
// harness-owned code-out step that replaces the Agent's own `git bundle
// create` instruction under CODE_FORGE=local (issue #1808).
func TestRun_NonEmptyRange_CreatesBundle(t *testing.T) {
	dir, base, branch := newRepoWithFeatureBranch(t)
	outbox := t.TempDir()

	var stdout bytes.Buffer
	err := bundleout.Run(bundleout.Config{
		Repo:      dir,
		Base:      base,
		Branch:    branch,
		OutboxDir: outbox,
		Issue:     "7",
	}, &stdout)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	bundlePath := filepath.Join(outbox, seambundle.FileName)
	if _, err := os.Stat(bundlePath); err != nil {
		t.Fatalf("bundle not created at %s: %v", bundlePath, err)
	}
	if out, err := exec.Command("git", "-C", dir, "bundle", "verify", bundlePath).CombinedOutput(); err != nil {
		t.Fatalf("bundle verify failed: %v: %s", err, out)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty (no corrective line when work exists)", stdout.String())
	}
}

// TestRun_EmptyRangeAfterReadyClaim_AppendsCorrectiveOutcome verifies that an
// empty base..branch range after the Agent's own status=ready claim gets no
// bundle and a corrective SPINDRIFT_OUTCOME line instead — a false `ready`
// can't settle silently (issue #1808). Read back through
// outcome.LastInLog, the same log-scan seam the launcher itself uses, so the
// test exercises the actual contract rather than string-matching stdout.
func TestRun_EmptyRangeAfterReadyClaim_AppendsCorrectiveOutcome(t *testing.T) {
	dir, base, branch := newRepoNoFeatureCommits(t)
	outbox := t.TempDir()

	priorLine := outcome.Outcome{Issue: "7", Landing: branch, Status: "ready", Note: "done"}.Line()

	logPath := filepath.Join(t.TempDir(), "box.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(logFile, priorLine); err != nil {
		t.Fatal(err)
	}

	err = bundleout.Run(bundleout.Config{
		Repo:             dir,
		Base:             base,
		Branch:           branch,
		OutboxDir:        outbox,
		Issue:            "7",
		PriorOutcomeLine: priorLine,
	}, logFile)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	logFile.Close()

	if _, err := os.Stat(filepath.Join(outbox, seambundle.FileName)); err == nil {
		t.Fatalf("bundle created for an empty range, want none")
	}

	o, found, err := outcome.LastInLog(logPath)
	if err != nil {
		t.Fatalf("outcome.LastInLog: %v", err)
	}
	if !found {
		t.Fatal("outcome.LastInLog: no outcome line found")
	}
	if o.Status != "blocked" {
		t.Errorf("corrective outcome status = %q, want %q", o.Status, "blocked")
	}
	if o.Landing != "none" {
		t.Errorf("corrective outcome landing = %q, want %q", o.Landing, "none")
	}
	if !strings.Contains(o.Note, "ready") || !strings.Contains(o.Note, branch) {
		t.Errorf("corrective outcome note = %q, want it to name the ready/no-commits contradiction on %s", o.Note, branch)
	}
}

// TestRun_EmptyRangeAfterBlockedClaim_WritesNothing verifies a legitimate
// "no change needed" verdict (status=blocked, no commits) is left alone: no
// bundle, and no corrective line clobbering the Agent's own more specific
// note — only a status=ready claim is a contradiction Run needs to correct
// (issue #1808).
func TestRun_EmptyRangeAfterBlockedClaim_WritesNothing(t *testing.T) {
	dir, base, branch := newRepoNoFeatureCommits(t)
	outbox := t.TempDir()

	priorLine := outcome.Outcome{Issue: "7", Landing: branch, Status: "blocked", Note: "no change needed"}.Line()

	var stdout bytes.Buffer
	err := bundleout.Run(bundleout.Config{
		Repo:             dir,
		Base:             base,
		Branch:           branch,
		OutboxDir:        outbox,
		Issue:            "7",
		PriorOutcomeLine: priorLine,
	}, &stdout)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if _, err := os.Stat(filepath.Join(outbox, seambundle.FileName)); err == nil {
		t.Fatalf("bundle created for an empty range, want none")
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty (an already-blocked claim needs no correction)", stdout.String())
	}
}
