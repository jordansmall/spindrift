package bundleout_test

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/bundleout"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/seambundle"
	"spindrift.dev/launcher/internal/testutil"
)

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
	testutil.GitRun(t, dir, "init", "-b", "main")
	testutil.GitRun(t, dir, "config", "user.name", "Test Bot")
	testutil.GitRun(t, dir, "config", "user.email", "bot@example.com")
	writeFile(t, filepath.Join(dir, "base.txt"), "base\n")
	testutil.GitRun(t, dir, "add", "base.txt")
	testutil.GitRun(t, dir, "commit", "-m", "base")
	testutil.GitRun(t, dir, "checkout", "-b", "feature")
	writeFile(t, filepath.Join(dir, "feature.txt"), "feature\n")
	testutil.GitRun(t, dir, "add", "feature.txt")
	testutil.GitRun(t, dir, "commit", "-m", "feature")
	return dir, "main", "feature"
}

// newRepoNoFeatureCommits creates a repo with one commit on "main" and a
// "feature" branch cut from the same tip with no commits ahead, so base..branch
// is empty. It stands in for a Box that claimed ready but never committed.
func newRepoNoFeatureCommits(t *testing.T) (dir, base, branch string) {
	t.Helper()
	dir = t.TempDir()
	testutil.GitRun(t, dir, "init", "-b", "main")
	testutil.GitRun(t, dir, "config", "user.name", "Test Bot")
	testutil.GitRun(t, dir, "config", "user.email", "bot@example.com")
	writeFile(t, filepath.Join(dir, "base.txt"), "base\n")
	testutil.GitRun(t, dir, "add", "base.txt")
	testutil.GitRun(t, dir, "commit", "-m", "base")
	testutil.GitRun(t, dir, "checkout", "-b", "feature")
	return dir, "main", "feature"
}

// Issue #1808: the harness, not the Agent, bundles base..branch under
// CODE_FORGE=local.
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

// A false ready must not settle silently (issue #1808). The corrective line is
// read back through outcome.Resolve, the launcher's own scan, not by matching
// stdout.
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

	// ADR 0055, issue #2274: the outcome scan does not gate on a nonce, so
	// Resolve must find the corrective line without one.
	resolved, err := outcome.Resolve([]outcome.PassLog{{Path: logPath}}, "")
	if err != nil {
		t.Fatalf("outcome.Resolve: %v", err)
	}
	if !resolved.Found || resolved.Provenance == outcome.ProvenanceSelfReport {
		t.Fatal("outcome.Resolve: no genuine outcome line found")
	}
	o := resolved.Outcome
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

// A legitimate "no change needed" (status=blocked, no commits) must not get a
// corrective line clobbering the Agent's own note (issue #1808).
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

// A Branch starting with "-" could be misread as a git option in the
// `base..branch` spec; forge/local's relayBundle applies the same guard.
func TestRun_RejectsRefStartingWithDash(t *testing.T) {
	dir, base, _ := newRepoWithFeatureBranch(t)
	err := bundleout.Run(bundleout.Config{
		Repo:      dir,
		Base:      base,
		Branch:    "--upload-pack=evil",
		OutboxDir: t.TempDir(),
		Issue:     "7",
	}, io.Discard)
	if err == nil {
		t.Fatal("Run: want an error for a branch starting with -, got nil")
	}
}
