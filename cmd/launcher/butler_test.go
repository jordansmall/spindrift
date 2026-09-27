package main

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/outcome"
)

// newButlerTestRepo builds a bare repo with one commit on "main" holding one
// tracked file, the fixture every runButlerChore test claims and sweeps
// against (same exec-git convention as internal/butler/git_test.go and
// internal/settle/butler_test.go's own bare-repo fixtures).
func newButlerTestRepo(t *testing.T) (repo, head string) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")

	dir := t.TempDir()
	bare := filepath.Join(dir, "repo.git")
	runButlerGit(t, "", "init", "--bare", "-q", bare)
	runButlerGit(t, bare, "config", "gc.auto", "0")

	hashCmd := exec.Command("git", "-C", bare, "hash-object", "-w", "--stdin")
	hashCmd.Stdin = strings.NewReader("package a\n")
	out, err := hashCmd.Output()
	if err != nil {
		t.Fatalf("hash-object: %v", err)
	}
	blob := strings.TrimSpace(string(out))

	mktreeCmd := exec.Command("git", "-C", bare, "mktree")
	mktreeCmd.Stdin = strings.NewReader("100644 blob " + blob + "\ta.go\n")
	treeOut, err := mktreeCmd.Output()
	if err != nil {
		t.Fatalf("mktree: %v", err)
	}
	tree := strings.TrimSpace(string(treeOut))

	commitOut, err := exec.Command("git", "-C", bare, "commit-tree", tree, "-m", "base").Output()
	if err != nil {
		t.Fatalf("commit-tree: %v", err)
	}
	head = strings.TrimSpace(string(commitOut))
	runButlerGit(t, bare, "update-ref", "refs/heads/main", head)
	return bare, head
}

func runButlerGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := args
	if dir != "" {
		full = append([]string{"-C", dir}, args...)
	}
	if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", full, err, out)
	}
}

// readyDispatcher builds a dispatch.Fake whose Run() reports a ready outcome
// carrying one filed-issue intent, the "clean run" shape ButlerSettle expects
// (internal/settle/butler_test.go's readyResult mirrors this).
func readyDispatcher() *dispatch.Fake {
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
	return d
}

// crashedDispatcher builds a dispatch.Fake whose Run() reports no outcome
// line at all, the shape a killed or crashed Box leaves (ButlerSettle.fail's
// "no ready outcome line" branch).
func crashedDispatcher() *dispatch.Fake {
	d := dispatch.NewFake()
	d.RunResult = dispatch.Result{Success: false}
	return d
}

// lostRaceBackend wraps a ledger.Backend so every Append fails with
// ledger.ErrLostRace, the shape a rival worker's commit landing between this
// run's Read and its own Claim produces (ledger.Local's compare-and-swap
// losing in local.go). Read and History pass through unchanged.
type lostRaceBackend struct {
	ledger.Backend
}

func (b lostRaceBackend) Append(_, _ string, _ ledger.State, _ time.Time) (string, error) {
	return "", ledger.ErrLostRace
}

// gitLogSubjects returns ref's commit subjects in refs, newest first, the
// same shape the issue's acceptance criterion inspects with `git log
// --format=%s`.
func gitLogSubjects(t *testing.T, repo, ref string) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "log", "--format=%s", ref).Output()
	if err != nil {
		t.Fatalf("git log %s: %v", ref, err)
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// testButlerRun is the butlerRun every runButlerChore test sweeps: the
// "bugs" Chore on repo's main branch.
func testButlerRun(repo string) butlerRun {
	return butlerRun{repo: repo, branch: "main", chore: "bugs", host: "test-host"}
}

// (a) A clean run claims, dispatches, files its one finding, and writes a
// done commit carrying lastSwept=head and the filed URL -- visible in the
// Accumulation repo's refs/spindrift/butler/<chore> ref, the issue's own
// acceptance criterion (`git log refs/spindrift/butler/bugs`). A
// Consumer-declared Chore ("tidy-deps") runs exactly like a built-in one.
func TestRunButlerChore_CleanRunFilesAndWritesDoneCommit(t *testing.T) {
	for _, chore := range []string{"bugs", "tidy-deps"} {
		t.Run(chore, func(t *testing.T) {
			repo, head := newButlerTestRepo(t)
			backend := ledger.Local{Repo: repo}

			fc := forge.NewFake()
			fc.PostIssueURL = "https://example.com/issues/9001"

			d := readyDispatcher()
			newDispatcher := func(c dispatch.Chore) dispatch.Dispatcher {
				if c.Name != chore || c.Branch != "main" {
					t.Fatalf("newDispatcher chore = %+v, want Name=%s Branch=main", c, chore)
				}
				if c.Scope.Head != head {
					t.Fatalf("newDispatcher scope.Head = %q, want %q", c.Scope.Head, head)
				}
				return d
			}

			br := testButlerRun(repo)
			br.chore = chore

			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			err := runButlerChore(backend, fc.AsIssueFiler(), br, newDispatcher, func() time.Time { return now })
			if err != nil {
				t.Fatalf("runButlerChore: %v", err)
			}

			subjects := gitLogSubjects(t, repo, ledger.RefPrefix+chore)
			if len(subjects) != 2 || subjects[0] != chore+": done" || subjects[1] != chore+": claimed" {
				t.Fatalf("git log subjects = %v, want [%s: done, %s: claimed]", subjects, chore, chore)
			}

			tip, err := backend.Read(chore)
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if tip.State.Phase != ledger.Done {
				t.Errorf("Phase = %q, want %q", tip.State.Phase, ledger.Done)
			}
			if tip.State.LastSwept != head {
				t.Errorf("LastSwept = %q, want %q", tip.State.LastSwept, head)
			}
			if len(tip.State.Filed) != 1 || tip.State.Filed[0] != fc.PostIssueURL {
				t.Errorf("Filed = %v, want [%s]", tip.State.Filed, fc.PostIssueURL)
			}
			if d.CloseCalls != 1 {
				t.Errorf("CloseCalls = %d, want 1", d.CloseCalls)
			}
		})
	}
}

// (b) A crashed run (no ready outcome) leaves the claim standing: Settle
// writes nothing, so the Ledger tip after the run is still the claim, and
// runButlerChore reports the failure via a non-nil error.
func TestRunButlerChore_CrashedRunLeavesClaimStanding(t *testing.T) {
	repo, _ := newButlerTestRepo(t)
	backend := ledger.Local{Repo: repo}

	fc := forge.NewFake()
	newDispatcher := func(c dispatch.Chore) dispatch.Dispatcher { return crashedDispatcher() }

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	err := runButlerChore(backend, fc.AsIssueFiler(), testButlerRun(repo), newDispatcher, func() time.Time { return now })
	if err == nil {
		t.Fatal("runButlerChore: got nil error, want one reporting the crashed run")
	}
	if errors.Is(err, errQueueEmpty) {
		t.Errorf("runButlerChore err = %v, want a real failure, not errQueueEmpty", err)
	}

	tip, rerr := backend.Read("bugs")
	if rerr != nil {
		t.Fatalf("Read: %v", rerr)
	}
	if tip.State.Phase != ledger.Claimed {
		t.Errorf("Phase = %q, want %q (claim left standing)", tip.State.Phase, ledger.Claimed)
	}
}

// (c) A live (non-stale) claim held by another run makes runButlerChore
// report "no work" (errQueueEmpty) without dispatching a Box or writing a new
// commit.
func TestRunButlerChore_LiveClaimReportsNoWork(t *testing.T) {
	repo, _ := newButlerTestRepo(t)
	backend := ledger.Local{Repo: repo}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim, err := ledger.Claim(backend, "bugs", ledger.Tip{}, ledger.ClaimedBy{Host: "other-host", Start: now})
	if err != nil {
		t.Fatalf("seed Claim: %v", err)
	}

	dispatched := false
	newDispatcher := func(c dispatch.Chore) dispatch.Dispatcher {
		dispatched = true
		return dispatch.NewFake()
	}

	err = runButlerChore(backend, forge.NewFake().AsIssueFiler(), testButlerRun(repo), newDispatcher, func() time.Time { return now.Add(time.Minute) })
	if !errors.Is(err, errQueueEmpty) {
		t.Fatalf("runButlerChore err = %v, want errQueueEmpty", err)
	}
	if dispatched {
		t.Error("newDispatcher was called; want no Box dispatched against a live claim")
	}

	tip, rerr := backend.Read("bugs")
	if rerr != nil {
		t.Fatalf("Read: %v", rerr)
	}
	if tip.Commit != claim.Commit {
		t.Errorf("tip.Commit = %s, want unchanged from the seeded claim %s", tip.Commit, claim.Commit)
	}
}

// (d) A claim older than butlerClaimTimeout is a crashed worker's leftover
// (ADR 0056): the next run takes it over rather than reporting "no work",
// and a clean sweep against it still ends in a done commit.
func TestRunButlerChore_StaleClaimIsTakenOver(t *testing.T) {
	repo, _ := newButlerTestRepo(t)
	backend := ledger.Local{Repo: repo}

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := ledger.Claim(backend, "bugs", ledger.Tip{}, ledger.ClaimedBy{Host: "dead-host", Start: start}); err != nil {
		t.Fatalf("seed Claim: %v", err)
	}

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9002"
	dispatched := false
	newDispatcher := func(c dispatch.Chore) dispatch.Dispatcher {
		dispatched = true
		return readyDispatcher()
	}

	now := start.Add(butlerClaimTimeout + time.Minute)
	err := runButlerChore(backend, fc.AsIssueFiler(), testButlerRun(repo), newDispatcher, func() time.Time { return now })
	if err != nil {
		t.Fatalf("runButlerChore: %v", err)
	}
	if !dispatched {
		t.Fatal("newDispatcher was not called; want the stale claim taken over and a Box dispatched")
	}

	tip, rerr := backend.Read("bugs")
	if rerr != nil {
		t.Fatalf("Read: %v", rerr)
	}
	if tip.State.Phase != ledger.Done {
		t.Errorf("Phase = %q, want %q (stale claim taken over and the sweep completed)", tip.State.Phase, ledger.Done)
	}
}

// (e) A claim just younger than butlerClaimTimeout is still live: runButlerChore
// reports errQueueEmpty without taking it over or dispatching a Box.
func TestRunButlerChore_ClaimJustUnderTimeoutStillLive(t *testing.T) {
	repo, _ := newButlerTestRepo(t)
	backend := ledger.Local{Repo: repo}

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim, err := ledger.Claim(backend, "bugs", ledger.Tip{}, ledger.ClaimedBy{Host: "other-host", Start: start})
	if err != nil {
		t.Fatalf("seed Claim: %v", err)
	}

	dispatched := false
	newDispatcher := func(c dispatch.Chore) dispatch.Dispatcher {
		dispatched = true
		return dispatch.NewFake()
	}

	now := start.Add(butlerClaimTimeout - time.Minute)
	err = runButlerChore(backend, forge.NewFake().AsIssueFiler(), testButlerRun(repo), newDispatcher, func() time.Time { return now })
	if !errors.Is(err, errQueueEmpty) {
		t.Fatalf("runButlerChore err = %v, want errQueueEmpty", err)
	}
	if dispatched {
		t.Error("newDispatcher was called; want no Box dispatched against a claim not yet stale")
	}

	tip, rerr := backend.Read("bugs")
	if rerr != nil {
		t.Fatalf("Read: %v", rerr)
	}
	if tip.Commit != claim.Commit {
		t.Errorf("tip.Commit = %s, want unchanged from the seeded claim %s", tip.Commit, claim.Commit)
	}
}

// (f) ledger.Claim losing the race (a rival's commit landed between this run's
// Read and its own Claim) is the same "nothing to do right now" signal as a
// live claim, not a real error, and dispatches no Box.
func TestRunButlerChore_ClaimLostRaceReportsNoWork(t *testing.T) {
	repo, _ := newButlerTestRepo(t)
	backend := lostRaceBackend{Backend: ledger.Local{Repo: repo}}

	dispatched := false
	newDispatcher := func(c dispatch.Chore) dispatch.Dispatcher {
		dispatched = true
		return dispatch.NewFake()
	}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	err := runButlerChore(backend, forge.NewFake().AsIssueFiler(), testButlerRun(repo), newDispatcher, func() time.Time { return now })
	if !errors.Is(err, errQueueEmpty) {
		t.Fatalf("runButlerChore err = %v, want errQueueEmpty", err)
	}
	if dispatched {
		t.Error("newDispatcher was called; want no Box dispatched when Claim loses the race")
	}
}

// (g) cmdButler rejects a chore not named in BUTLER_CHORES, before ever
// touching the factory or issue tracker.
func TestCmdButler_RejectsChoreNotEnabled(t *testing.T) {
	lc := &launchContext{
		config:  config{schemaConfig: schemaConfig{codeForge: "local", butlerChores: "other-chore"}},
		cleanup: func() {},
	}
	code := cmdButler(lc, "bugs")
	if code != 1 {
		t.Errorf("cmdButler code = %d, want 1", code)
	}
}

// (h) cmdButler rejects a CODE_FORGE with no butler Ledger backend (issue
// #3876: only local, github, and forgejo have one).
func TestCmdButler_RejectsForgeWithNoLedger(t *testing.T) {
	lc := &launchContext{
		config:  config{schemaConfig: schemaConfig{codeForge: "git", butlerChores: "bugs"}},
		cleanup: func() {},
	}
	code := cmdButler(lc, "bugs")
	if code != 1 {
		t.Errorf("cmdButler code = %d, want 1", code)
	}
}

// (h2) A fresh Consumer -- BUTLER_CHORES left at its schema default, "" --
// never starts a butler run for any built-in Chore: cmdButler must refuse
// before ever touching lc.factory (left nil here), since a nil factory would
// panic on NewChore.
func TestCmdButler_FreshConsumerNeverStartsRun(t *testing.T) {
	if def := schemaDefault("BUTLER_CHORES"); def != "" {
		t.Fatalf("schemaDefault(BUTLER_CHORES) = %q, want \"\" (test assumes opt-in-only default)", def)
	}
	for _, chore := range []string{"bugs", "refactor", "docs-drift"} {
		t.Run(chore, func(t *testing.T) {
			lc := &launchContext{
				config:  config{schemaConfig: schemaConfig{codeForge: "local", butlerChores: schemaDefault("BUTLER_CHORES")}},
				cleanup: func() {},
			}
			code := cmdButler(lc, chore)
			if code != 1 {
				t.Errorf("cmdButler(%q) code = %d, want 1", chore, code)
			}
		})
	}
}

// (i) butlerPreflight guards codeForge, BUTLER_CHORES membership, and the
// Filer gate in that order, before cmdButler ever claims a Ledger -- in
// particular a chore run with no provisioned Filer (DRIVER=opencode, or
// FILER_MODEL="") must be refused up front rather than sweep and silently
// drop findings.
func TestButlerPreflight(t *testing.T) {
	cases := []struct {
		name               string
		codeForge          string
		butlerChores       string
		butlerChoreClasses string
		chore              string
		filerEnabled       bool
		wantErr            string // substring of the error, checked in guard order; "" means no error
	}{
		{"all clear", "local", "bugs", "bugs=error-handling", "bugs", true, ""},
		{"github clear", "github", "bugs", "", "bugs", true, ""},
		{"forgejo clear", "forgejo", "bugs", "", "bugs", true, ""},
		{"forge with no ledger rejected", "git", "bugs", "", "bugs", true, "cannot host a butler Ledger (supported: github, forgejo, local)"},
		{"chore not enabled", "local", "other-chore", "", "bugs", true, "is not enabled"},
		{"malformed classes rejected", "local", "bugs", "bugs", "bugs", true, "BUTLER_CHORE_CLASSES"},
		{"filer not provisioned", "local", "bugs", "", "bugs", false, "needs a provisioned Filer"},
		{"forge checked before chore", "git", "other-chore", "", "bugs", false, "cannot host a butler Ledger"},
		{"chore checked before filer", "local", "other-chore", "", "bugs", false, "is not enabled"},
		{"classes checked before filer", "local", "bugs", "bugs", "bugs", false, "BUTLER_CHORE_CLASSES"},
		{"chore with no classes entry passes", "local", "tidy-deps", "bugs=error-handling", "tidy-deps", true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config{schemaConfig: schemaConfig{
				codeForge:          tc.codeForge,
				butlerChores:       tc.butlerChores,
				butlerChoreClasses: tc.butlerChoreClasses,
			}}
			err := butlerPreflight(cfg, tc.chore, tc.filerEnabled)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("butlerPreflight(%+v): %v", tc, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("butlerPreflight(%+v) err = %v, want substring %q", tc, err, tc.wantErr)
			}
		})
	}
}

// (j) parseButlerArgs: --chore is required and takes exactly one value;
// --no-build is the one other flag butler shares with dispatch/research.
func TestParseButlerArgs(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantChore string
		wantNo    bool
		wantErr   bool
	}{
		{"chore only", []string{"--chore", "bugs"}, "bugs", false, false},
		{"chore plus no-build", []string{"--chore", "bugs", "--no-build"}, "bugs", true, false},
		{"no-build before chore", []string{"--no-build", "--chore", "bugs"}, "bugs", true, false},
		{"missing --chore entirely", []string{}, "", false, true},
		{"missing --chore entirely with no-build", []string{"--no-build"}, "", false, true},
		{"--chore with no value", []string{"--chore"}, "", false, true},
		{"unrecognized token", []string{"bogus"}, "", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chore, noBuild, err := parseButlerArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseButlerArgs(%v): got nil error, want one", tc.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseButlerArgs(%v): %v", tc.args, err)
			}
			if chore != tc.wantChore || noBuild != tc.wantNo {
				t.Errorf("parseButlerArgs(%v) = (%q, %v), want (%q, %v)", tc.args, chore, noBuild, tc.wantChore, tc.wantNo)
			}
		})
	}
}

// choreEnabled is BUTLER_CHORES's membership test: space-separated, exact
// name match.
func TestChoreEnabled(t *testing.T) {
	cases := []struct {
		list  string
		chore string
		want  bool
	}{
		{"", "bugs", false},
		{"bugs", "bugs", true},
		{"bugs other", "other", true},
		{"bugs other", "bug", false},
	}
	for i, tc := range cases {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			if got := choreEnabled(tc.list, tc.chore); got != tc.want {
				t.Errorf("choreEnabled(%q, %q) = %v, want %v", tc.list, tc.chore, got, tc.want)
			}
		})
	}
}
