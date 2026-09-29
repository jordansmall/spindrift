package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/butler"
	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/ledger/ledgertest"
	"spindrift.dev/launcher/internal/outcome"
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
	remoteRepo := ledgertest.NewRepo(t)
	head := strings.TrimSpace(string(runButlerGitOutput(t, remoteRepo, "rev-parse", "refs/heads/main")))

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

// trace2Event is the subset of a GIT_TRACE2_EVENT JSON line this file reads:
// "start" events carry the full argv for their sid (git's per-process
// session id, hierarchical for child processes); "cmd_name" events carry the
// dispatched subcommand name for that same sid.
type trace2Event struct {
	Event string   `json:"event"`
	SID   string   `json:"sid"`
	Name  string   `json:"name"`
	Argv  []string `json:"argv"`
}

// readTrace2Events parses every line of a GIT_TRACE2_EVENT log at path.
func readTrace2Events(t *testing.T, path string) []trace2Event {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read trace2 log %s: %v", path, err)
	}
	var events []trace2Event
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		var ev trace2Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("parse trace2 line %q: %v", line, err)
		}
		events = append(events, ev)
	}
	return events
}

// countCmdArgv counts top-level (cmd_name) processes named cmd whose argv
// (looked up by matching sid against a "start" event) contains substr, e.g.
// distinguishing a Ledger-refspec fetch from a base-branch fetch by which
// ref pattern the argv carries.
func countCmdArgv(events []trace2Event, cmd, substr string) int {
	argvBySID := map[string][]string{}
	for _, ev := range events {
		if ev.Event == "start" {
			argvBySID[ev.SID] = ev.Argv
		}
	}
	count := 0
	for _, ev := range events {
		if ev.Event != "cmd_name" || ev.Name != cmd {
			continue
		}
		for _, arg := range argvBySID[ev.SID] {
			if strings.Contains(arg, substr) {
				count++
				break
			}
		}
	}
	return count
}

// TestRemoteLedger_CountsOneFetchAndOnePushPerSweep drives a full
// butler.Sweep against a remoteLedger-built backend/tree -- the same wiring
// cmdButler uses for the github and forgejo rows -- with GIT_TRACE2_EVENT
// tracing. It asserts exactly two fetches (ledger.NewRemote's
// construction-time sync of the Ledger refspec, and butler.FetchTree's fetch
// of the base branch, issue #3995's "syncs once" for each), and one push per
// Ledger commit the run makes: Claim then Finish, two, with promotion off.
func TestRemoteLedger_CountsOneFetchAndOnePushPerSweep(t *testing.T) {
	remoteRepo := ledgertest.NewRepo(t)

	trace := filepath.Join(t.TempDir(), "trace2.log")
	t.Setenv("GIT_TRACE2_EVENT", trace)

	backend, tree, cleanup, err := remoteLedger(config{schemaConfig: schemaConfig{baseBranch: "main"}}, remoteRepo)
	if err != nil {
		t.Fatalf("remoteLedger: %v", err)
	}
	defer cleanup()

	d := dispatch.NewFake()
	d.RunResult = dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "butler-bugs", Status: outcome.StatusReady, Note: "swept"},
		},
		IssueIntentsFound: true,
		IssueIntents:      []string{`{"title":"bug found","body":"repro","dedupTerms":["a.go:Foo"]}`},
	}
	newBox := func(dispatch.Chore) dispatch.Dispatcher { return d }

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	policy := butler.Policy{
		Branch:         "main",
		Host:           "test-host",
		Chores:         []chore.Chore{{Name: "bugs"}},
		ClaimTimeout:   6 * time.Hour,
		Zone:           time.UTC,
		PromotionLabel: "ready-for-agent",
	}
	r := butler.New(backend, tree, forge.NewFake().AsIssueFiler(), newBox, policy, func() time.Time { return now })
	out, err := r.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != butler.Swept {
		t.Fatalf("Kind = %v, want Swept: %+v", out.Kind, out)
	}

	events := readTrace2Events(t, trace)
	if got := ledgertest.CmdCount(t, trace, "fetch"); got != 2 {
		t.Fatalf("fetch count = %d, want exactly 2 (one Ledger refspec, one base branch)", got)
	}
	if got := countCmdArgv(events, "fetch", ledger.RefPrefix); got != 1 {
		t.Errorf("fetches carrying the Ledger refspec %s = %d, want exactly 1", ledger.RefPrefix, got)
	}
	if got := countCmdArgv(events, "fetch", "refs/heads/main"); got != 1 {
		t.Errorf("fetches carrying refs/heads/main = %d, want exactly 1", got)
	}
	if got := ledgertest.CmdCount(t, trace, "push"); got != 2 {
		t.Errorf("push count = %d, want exactly 2 (Claim + Finish, promotion off)", got)
	}
}
