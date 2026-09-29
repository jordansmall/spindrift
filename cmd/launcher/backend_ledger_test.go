package main

import (
	"testing"
)

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
// cmdButler relies on: the returned Tree resolves refs/heads/<base> to the
// remote's head, the returned backend reads a zero Tip for an unclaimed
// chore, and cleanup removes the scratch dir (the Tree can no longer resolve
// anything once it's gone).
func TestRemoteLedger_FetchesBaseBranchAndCleansUp(t *testing.T) {
	remoteRepo, head := newButlerTestRepo(t)

	backend, tree, cleanup, err := remoteLedger(config{schemaConfig: schemaConfig{baseBranch: "main"}}, remoteRepo)
	if err != nil {
		t.Fatalf("remoteLedger: %v", err)
	}

	gotHead, err := tree.Head("main")
	if err != nil {
		t.Fatalf("tree.Head(main): %v", err)
	}
	if gotHead != head {
		t.Errorf("tree.Head(main) = %q, want remote head %q", gotHead, head)
	}

	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if tip.Commit != "" {
		t.Errorf("Read(bugs).Commit = %q, want \"\" (unclaimed)", tip.Commit)
	}

	cleanup()
	if _, err := tree.Head("main"); err == nil {
		t.Error("tree.Head(main) succeeded after cleanup, want error (scratch dir removed)")
	}
}

// The full hosted-forge end-to-end Sweep -- refs/spindrift/butler/<chore>
// landing on the remote and refs/heads/main staying put -- moved to
// internal/butler's TestSweep_RemoteBackendEndToEndAgainstHostedForgeShape
// (issue #3990): it exercises ledger.Remote's own push/fetch behavior under a
// real Sweep, not remoteLedger's wiring, so it belongs at the Runner level.
