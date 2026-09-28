package main

import (
	"os/exec"
	"strings"
	"testing"
)

// rawGitOutput runs git in dir and returns trimmed stdout, or an error --
// unlike runButlerGit (butler_test.go), it never t.Fatalf's, since
// TestRemoteLedger_FetchesBaseBranchAndCleansUp needs to see a post-cleanup
// rev-parse fail rather than aborting the test on it.
func rawGitOutput(dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir}, args...)
	out, err := exec.Command("git", full...).Output()
	return strings.TrimSpace(string(out)), err
}

// TestBackendRows_NewLedgerCoverage pins which CODE_FORGE rows can host a
// butler Ledger (issue #3876): local, github, and forgejo do; git and jira
// (jira is tracker-only, never a CODE_FORGE) do not yet.
func TestBackendRows_NewLedgerCoverage(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"local", true},
		{"github", true},
		{"forgejo", true},
		{"git", false},
		{"jira", false},
	}
	for _, tc := range cases {
		row, ok := backendByName(tc.name)
		if !ok {
			t.Fatalf("backendByName(%q) ok=false", tc.name)
		}
		if got := row.newLedger != nil; got != tc.want {
			t.Errorf("backendByName(%q).newLedger != nil = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestRemoteLedger_FetchesBaseBranchAndCleansUp drives remoteLedger against a
// local bare repo standing in for a hosted forge's remote (no network, no
// gitArgs needed for a plain filesystem URL). It checks the three things
// cmdButler relies on: the returned repo has refs/heads/<base> at the
// remote's head, the returned backend reads a zero Tip for an unclaimed
// chore, and cleanup removes the scratch dir.
func TestRemoteLedger_FetchesBaseBranchAndCleansUp(t *testing.T) {
	remoteRepo, head := newButlerTestRepo(t)

	backend, repo, cleanup, err := remoteLedger(config{schemaConfig: schemaConfig{baseBranch: "main"}}, remoteRepo)
	if err != nil {
		t.Fatalf("remoteLedger: %v", err)
	}

	gotHead, err := rawGitOutput(repo, "rev-parse", "refs/heads/main")
	if err != nil {
		t.Fatalf("rev-parse refs/heads/main in scratch repo: %v", err)
	}
	if gotHead != head {
		t.Errorf("scratch repo refs/heads/main = %q, want remote head %q", gotHead, head)
	}

	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if tip.Commit != "" {
		t.Errorf("Read(bugs).Commit = %q, want \"\" (unclaimed)", tip.Commit)
	}

	cleanup()
	if _, err := rawGitOutput(repo, "rev-parse", "--is-bare-repository"); err == nil {
		t.Errorf("scratch repo %s still present after cleanup", repo)
	}
}

// The full hosted-forge end-to-end Sweep -- refs/spindrift/butler/<chore>
// landing on the remote and refs/heads/main staying put -- moved to
// internal/butler's TestSweep_RemoteBackendEndToEndAgainstHostedForgeShape
// (issue #3990): it exercises ledger.Remote's own push/fetch behavior under a
// real Sweep, not remoteLedger's wiring, so it belongs at the Runner level.
