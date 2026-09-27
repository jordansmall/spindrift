package main

import (
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/butler"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/outcome"
)

// testClaimTimeout is the claim timeout every test passes explicitly now
// that BUTLER_CLAIM_TIMEOUT is an operator knob rather than a package const.
const testClaimTimeout = 6 * time.Hour

// noEvery is a butlerEveryConfig with no bare default and no overrides: every
// chore has interval zero, so IntervalNotElapsed never blocks a due check --
// the shape most tests want when the interval itself isn't what's under test.
var noEvery = butlerEveryConfig{}

// testButlerPolicy builds a butlerPolicy for tests that don't exercise
// budgets or the day zone: testClaimTimeout, an unlimited (zero) Budgets,
// UTC, and enabled set to chores -- everything but budget/zone tests, which
// build their own butlerPolicy explicitly.
func testButlerPolicy(every butlerEveryConfig, chores ...string) butlerPolicy {
	return butlerPolicy{
		every:        every,
		claimTimeout: testClaimTimeout,
		zone:         time.UTC,
		enabled:      chores,
	}
}

// newButlerTestRepo builds a bare repo with one commit on "main" holding one
// tracked file, the fixture every runButler test claims and sweeps against
// (same exec-git convention as internal/butler/git_test.go and
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

// addButlerCommit commits one new tracked file (name/content) on top of
// repo's current refs/heads/main tip, moves the branch to it, and returns the
// new commit sha -- simulating real work landing on the branch between
// butler runs, so a run's DiffRange and tree-walk slice have something new to
// see.
func addButlerCommit(t *testing.T, repo, name, content string) string {
	t.Helper()
	parent := strings.TrimSpace(string(runButlerGitOutput(t, repo, "rev-parse", "refs/heads/main")))

	hashCmd := exec.Command("git", "-C", repo, "hash-object", "-w", "--stdin")
	hashCmd.Stdin = strings.NewReader(content)
	blobOut, err := hashCmd.Output()
	if err != nil {
		t.Fatalf("hash-object: %v", err)
	}
	blob := strings.TrimSpace(string(blobOut))

	lsOut := runButlerGitOutput(t, repo, "ls-tree", parent)
	entries := string(lsOut) + "100644 blob " + blob + "\t" + name + "\n"

	mktreeCmd := exec.Command("git", "-C", repo, "mktree")
	mktreeCmd.Stdin = strings.NewReader(entries)
	treeOut, err := mktreeCmd.Output()
	if err != nil {
		t.Fatalf("mktree: %v", err)
	}
	tree := strings.TrimSpace(string(treeOut))

	commitOut, err := exec.Command("git", "-C", repo, "commit-tree", tree, "-p", parent, "-m", "add "+name).Output()
	if err != nil {
		t.Fatalf("commit-tree: %v", err)
	}
	commit := strings.TrimSpace(string(commitOut))
	runButlerGit(t, repo, "update-ref", "refs/heads/main", commit)
	return commit
}

func runButlerGitOutput(t *testing.T, repo string, args ...string) []byte {
	t.Helper()
	full := append([]string{"-C", repo}, args...)
	out, err := exec.Command("git", full...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", full, err)
	}
	return out
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

// testButlerRun is the butlerRun every runButler test sweeps against: repo's
// main branch, claimed as "test-host".
func testButlerRun(repo string) butlerRun {
	return butlerRun{repo: repo, branch: "main", host: "test-host"}
}

// (a) A clean run claims, dispatches, files its one finding, and writes a
// done commit carrying lastSwept=head and the filed URL -- visible in the
// Accumulation repo's refs/spindrift/butler/<chore> ref, the issue's own
// acceptance criterion (`git log refs/spindrift/butler/bugs`). A
// Consumer-declared Chore ("tidy-deps") runs exactly like a built-in one.
func TestRunButler_CleanRunFilesAndWritesDoneCommit(t *testing.T) {
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

			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			err := runButler(backend, fc.AsIssueFiler(), testButlerRun(repo), []string{chore}, testButlerPolicy(noEvery, chore), newDispatcher, func() time.Time { return now })
			if err != nil {
				t.Fatalf("runButler: %v", err)
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
// runButler reports the failure via a non-nil error.
func TestRunButler_CrashedRunLeavesClaimStanding(t *testing.T) {
	repo, _ := newButlerTestRepo(t)
	backend := ledger.Local{Repo: repo}

	fc := forge.NewFake()
	newDispatcher := func(c dispatch.Chore) dispatch.Dispatcher { return crashedDispatcher() }

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	err := runButler(backend, fc.AsIssueFiler(), testButlerRun(repo), []string{"bugs"}, testButlerPolicy(noEvery, "bugs"), newDispatcher, func() time.Time { return now })
	if err == nil {
		t.Fatal("runButler: got nil error, want one reporting the crashed run")
	}
	if errors.Is(err, errQueueEmpty) {
		t.Errorf("runButler err = %v, want a real failure, not errQueueEmpty", err)
	}

	tip, rerr := backend.Read("bugs")
	if rerr != nil {
		t.Fatalf("Read: %v", rerr)
	}
	if tip.State.Phase != ledger.Claimed {
		t.Errorf("Phase = %q, want %q (claim left standing)", tip.State.Phase, ledger.Claimed)
	}
}

// A claim is stamped with the instant its due check used, not a second now()
// reading: with a clock that advances on every call, the crashed run's
// standing claim must still carry the first reading.
func TestRunButler_ClaimStartIsDueCheckInstant(t *testing.T) {
	repo, _ := newButlerTestRepo(t)
	backend := ledger.Local{Repo: repo}

	newDispatcher := func(c dispatch.Chore) dispatch.Dispatcher { return crashedDispatcher() }

	first := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	calls := 0
	now := func() time.Time {
		calls++
		return first.Add(time.Duration(calls-1) * time.Hour)
	}
	_ = runButler(backend, forge.NewFake().AsIssueFiler(), testButlerRun(repo), []string{"bugs"}, testButlerPolicy(noEvery, "bugs"), newDispatcher, now)

	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if tip.State.ClaimedBy == nil || !tip.State.ClaimedBy.Start.Equal(first) {
		t.Errorf("ClaimedBy = %+v, want Start %v (the due check's reading)", tip.State.ClaimedBy, first)
	}
}

// (c) A live (non-stale) claim held by another run makes runButler report
// "no work" (errQueueEmpty) without dispatching a Box or writing a new
// commit.
func TestRunButler_LiveClaimReportsNoWork(t *testing.T) {
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

	err = runButler(backend, forge.NewFake().AsIssueFiler(), testButlerRun(repo), []string{"bugs"}, testButlerPolicy(noEvery, "bugs"), newDispatcher, func() time.Time { return now.Add(time.Minute) })
	if !errors.Is(err, errQueueEmpty) {
		t.Fatalf("runButler err = %v, want errQueueEmpty", err)
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

// (d) A claim older than the claim timeout is a crashed worker's leftover
// (ADR 0056): the next run takes it over rather than reporting "no work",
// and a clean sweep against it still ends in a done commit.
func TestRunButler_StaleClaimIsTakenOver(t *testing.T) {
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

	now := start.Add(testClaimTimeout + time.Minute)
	err := runButler(backend, fc.AsIssueFiler(), testButlerRun(repo), []string{"bugs"}, testButlerPolicy(noEvery, "bugs"), newDispatcher, func() time.Time { return now })
	if err != nil {
		t.Fatalf("runButler: %v", err)
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

// (d2) Stale-claim takeover, covered end to end (issue #3877): a crashed
// run's claim sits on top of an earlier done commit that carries lastSwept
// and a mid-tree cursor forward. The next run, after the branch has moved,
// takes the stale claim over rather than reporting "no work", sweeps
// lastSwept..newHead, resumes the tree walk strictly after the old cursor
// (never restarting it), and ends in a done commit -- the full claimed(seed)
// -> done(seed) -> claimed(stale) -> done(final) chain, newest first.
func TestRunButler_StaleClaimTakeoverEndToEnd(t *testing.T) {
	repo, head1 := newButlerTestRepo(t)
	backend := ledger.Local{Repo: repo}

	seedStart := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedClaim, err := ledger.Claim(backend, "bugs", ledger.Tip{}, ledger.ClaimedBy{Host: "seed-host", Start: seedStart})
	if err != nil {
		t.Fatalf("seed Claim: %v", err)
	}
	seedDone, err := ledger.Finish(backend, "bugs", seedClaim, ledger.State{LastSwept: head1, Cursor: "a.go"}, seedStart)
	if err != nil {
		t.Fatalf("seed Finish: %v", err)
	}

	deadStart := seedStart.Add(time.Hour)
	if _, err := ledger.Claim(backend, "bugs", seedDone, ledger.ClaimedBy{Host: "dead-host", Start: deadStart}); err != nil {
		t.Fatalf("seed dead Claim: %v", err)
	}

	newHead := addButlerCommit(t, repo, "b.go", "package b\n")

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9003"
	var dispatchedChore dispatch.Chore
	dispatched := false
	newDispatcher := func(c dispatch.Chore) dispatch.Dispatcher {
		dispatched = true
		dispatchedChore = c
		return readyDispatcher()
	}

	now := deadStart.Add(testClaimTimeout + time.Minute)
	err = runButler(backend, fc.AsIssueFiler(), testButlerRun(repo), []string{"bugs"}, testButlerPolicy(noEvery, "bugs"), newDispatcher, func() time.Time { return now })
	if err != nil {
		t.Fatalf("runButler: %v", err)
	}
	if !dispatched {
		t.Fatal("newDispatcher was not called; want the stale claim taken over and a Box dispatched")
	}

	wantDiff := head1 + ".." + newHead
	if dispatchedChore.Scope.DiffRange != wantDiff {
		t.Errorf("Scope.DiffRange = %q, want %q", dispatchedChore.Scope.DiffRange, wantDiff)
	}
	if len(dispatchedChore.Scope.Slice) != 1 || dispatchedChore.Scope.Slice[0] != "b.go" {
		t.Errorf("Scope.Slice = %v, want [b.go] (resumed strictly after the old cursor a.go, not restarted)", dispatchedChore.Scope.Slice)
	}

	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if tip.State.Phase != ledger.Done {
		t.Errorf("Phase = %q, want %q", tip.State.Phase, ledger.Done)
	}
	if tip.State.LastSwept != newHead {
		t.Errorf("LastSwept = %q, want %q", tip.State.LastSwept, newHead)
	}

	// Newest first: the final done commit, the takeover's own new claimed
	// commit (ledger.Claim always appends a fresh state, never reusing the
	// stale one), the dead host's now-superseded claim, and the seed done
	// commit it was taken over on top of.
	subjects := gitLogSubjects(t, repo, ledger.RefPrefix+"bugs")
	want := []string{"bugs: done", "bugs: claimed", "bugs: claimed", "bugs: done", "bugs: claimed"}
	if !reflect.DeepEqual(subjects, want) {
		t.Errorf("git log subjects = %v, want %v", subjects, want)
	}
}

// (e) A claim just younger than the claim timeout is still live: runButler
// reports errQueueEmpty without taking it over or dispatching a Box.
func TestRunButler_ClaimJustUnderTimeoutStillLive(t *testing.T) {
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

	now := start.Add(testClaimTimeout - time.Minute)
	err = runButler(backend, forge.NewFake().AsIssueFiler(), testButlerRun(repo), []string{"bugs"}, testButlerPolicy(noEvery, "bugs"), newDispatcher, func() time.Time { return now })
	if !errors.Is(err, errQueueEmpty) {
		t.Fatalf("runButler err = %v, want errQueueEmpty", err)
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
func TestRunButler_ClaimLostRaceReportsNoWork(t *testing.T) {
	repo, _ := newButlerTestRepo(t)
	backend := lostRaceBackend{Backend: ledger.Local{Repo: repo}}

	dispatched := false
	newDispatcher := func(c dispatch.Chore) dispatch.Dispatcher {
		dispatched = true
		return dispatch.NewFake()
	}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	err := runButler(backend, forge.NewFake().AsIssueFiler(), testButlerRun(repo), []string{"bugs"}, testButlerPolicy(noEvery, "bugs"), newDispatcher, func() time.Time { return now })
	if !errors.Is(err, errQueueEmpty) {
		t.Fatalf("runButler err = %v, want errQueueEmpty", err)
	}
	if dispatched {
		t.Error("newDispatcher was called; want no Box dispatched when Claim loses the race")
	}
}

// (g) A named --chore not yet due by interval reports why and exits "no
// work", without claiming or dispatching.
func TestRunButler_NamedChoreNotDueByIntervalReportsWhy(t *testing.T) {
	repo, head := newButlerTestRepo(t)
	backend := ledger.Local{Repo: repo}

	doneAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim, err := ledger.Claim(backend, "bugs", ledger.Tip{}, ledger.ClaimedBy{Host: "seed-host", Start: doneAt})
	if err != nil {
		t.Fatalf("seed Claim: %v", err)
	}
	if _, err := ledger.Finish(backend, "bugs", claim, ledger.State{LastSwept: head}, doneAt); err != nil {
		t.Fatalf("seed Finish: %v", err)
	}
	beforeSubjects := gitLogSubjects(t, repo, ledger.RefPrefix+"bugs")

	dispatched := false
	newDispatcher := func(c dispatch.Chore) dispatch.Dispatcher {
		dispatched = true
		return dispatch.NewFake()
	}

	every, everr := parseButlerEvery("6h")
	if everr != nil {
		t.Fatalf("parseButlerEvery: %v", everr)
	}
	now := doneAt.Add(time.Hour) // well inside the 6h interval
	err = runButler(backend, forge.NewFake().AsIssueFiler(), testButlerRun(repo), []string{"bugs"}, testButlerPolicy(every, "bugs"), newDispatcher, func() time.Time { return now })
	if !errors.Is(err, errQueueEmpty) {
		t.Fatalf("runButler err = %v, want errQueueEmpty", err)
	}
	if !strings.Contains(err.Error(), `chore "bugs" not due`) || !strings.Contains(err.Error(), "interval not elapsed") {
		t.Errorf("runButler err = %q, want it to name the chore and say interval not elapsed", err)
	}
	if dispatched {
		t.Error("newDispatcher was called; want no Box dispatched against a chore not due")
	}

	afterSubjects := gitLogSubjects(t, repo, ledger.RefPrefix+"bugs")
	if !reflect.DeepEqual(afterSubjects, beforeSubjects) {
		t.Errorf("git log subjects = %v, want unchanged %v", afterSubjects, beforeSubjects)
	}
}

// (h) A named --chore fully rotated through the tree, with head unmoved
// since the last sweep, reports "nothing to scan" and exits "no work".
func TestRunButler_NamedChoreNothingToScanReportsWhy(t *testing.T) {
	repo, head := newButlerTestRepo(t)
	backend := ledger.Local{Repo: repo}

	fc := forge.NewFake()
	newDispatcher := func(c dispatch.Chore) dispatch.Dispatcher { return readyDispatcher() }
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := runButler(backend, fc.AsIssueFiler(), testButlerRun(repo), []string{"bugs"}, testButlerPolicy(noEvery, "bugs"), newDispatcher, func() time.Time { return now }); err != nil {
		t.Fatalf("seed clean run: %v", err)
	}

	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if tip.State.LastSwept != head || tip.State.Cursor != "" {
		t.Fatalf("seeded tip = %+v, want LastSwept=%s Cursor=\"\" (single-file repo, fully swept)", tip.State, head)
	}

	dispatched := false
	newDispatcher2 := func(c dispatch.Chore) dispatch.Dispatcher {
		dispatched = true
		return dispatch.NewFake()
	}
	err = runButler(backend, fc.AsIssueFiler(), testButlerRun(repo), []string{"bugs"}, testButlerPolicy(noEvery, "bugs"), newDispatcher2, func() time.Time { return now.Add(time.Hour) })
	if !errors.Is(err, errQueueEmpty) {
		t.Fatalf("runButler err = %v, want errQueueEmpty", err)
	}
	if !strings.Contains(err.Error(), "nothing to scan") {
		t.Errorf("runButler err = %q, want it to say nothing to scan", err)
	}
	if dispatched {
		t.Error("newDispatcher was called; want no Box dispatched with nothing left to scan")
	}
}

// (i) With no --chore, runButler picks the first due candidate in order,
// skipping one that isn't due yet and leaving its Ledger untouched.
func TestRunButler_NoChorePicksFirstDueCandidate(t *testing.T) {
	repo, head := newButlerTestRepo(t)
	backend := ledger.Local{Repo: repo}

	doneAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim, err := ledger.Claim(backend, "bugs", ledger.Tip{}, ledger.ClaimedBy{Host: "seed-host", Start: doneAt})
	if err != nil {
		t.Fatalf("seed Claim: %v", err)
	}
	if _, err := ledger.Finish(backend, "bugs", claim, ledger.State{LastSwept: head}, doneAt); err != nil {
		t.Fatalf("seed Finish: %v", err)
	}
	beforeSubjects := gitLogSubjects(t, repo, ledger.RefPrefix+"bugs")

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9004"
	var dispatchedChore dispatch.Chore
	newDispatcher := func(c dispatch.Chore) dispatch.Dispatcher {
		dispatchedChore = c
		return readyDispatcher()
	}

	every, everr := parseButlerEvery("6h")
	if everr != nil {
		t.Fatalf("parseButlerEvery: %v", everr)
	}
	now := doneAt.Add(time.Hour)
	err = runButler(backend, fc.AsIssueFiler(), testButlerRun(repo), []string{"bugs", "refactor"}, testButlerPolicy(every, "bugs", "refactor"), newDispatcher, func() time.Time { return now })
	if err != nil {
		t.Fatalf("runButler: %v", err)
	}
	if dispatchedChore.Name != "refactor" {
		t.Errorf("dispatched chore = %q, want refactor (bugs is not due yet)", dispatchedChore.Name)
	}

	afterSubjects := gitLogSubjects(t, repo, ledger.RefPrefix+"bugs")
	if !reflect.DeepEqual(afterSubjects, beforeSubjects) {
		t.Errorf("bugs ledger subjects = %v, want unchanged %v", afterSubjects, beforeSubjects)
	}
	refactorTip, err := backend.Read("refactor")
	if err != nil {
		t.Fatalf("Read refactor: %v", err)
	}
	if refactorTip.State.Phase != ledger.Done {
		t.Errorf("refactor Phase = %q, want %q", refactorTip.State.Phase, ledger.Done)
	}
}

// (j) With no --chore, when nothing is due, runButler reports errQueueEmpty
// naming every candidate's own reason.
func TestRunButler_NoChoreNoneDueReportsEachReason(t *testing.T) {
	repo, head := newButlerTestRepo(t)
	backend := ledger.Local{Repo: repo}

	doneAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, chore := range []string{"bugs", "refactor"} {
		claim, err := ledger.Claim(backend, chore, ledger.Tip{}, ledger.ClaimedBy{Host: "seed-host", Start: doneAt})
		if err != nil {
			t.Fatalf("seed Claim(%s): %v", chore, err)
		}
		if _, err := ledger.Finish(backend, chore, claim, ledger.State{LastSwept: head}, doneAt); err != nil {
			t.Fatalf("seed Finish(%s): %v", chore, err)
		}
	}

	dispatched := false
	newDispatcher := func(c dispatch.Chore) dispatch.Dispatcher {
		dispatched = true
		return dispatch.NewFake()
	}

	every, everr := parseButlerEvery("6h")
	if everr != nil {
		t.Fatalf("parseButlerEvery: %v", everr)
	}
	now := doneAt.Add(time.Hour)
	err := runButler(backend, forge.NewFake().AsIssueFiler(), testButlerRun(repo), []string{"bugs", "refactor"}, testButlerPolicy(every, "bugs", "refactor"), newDispatcher, func() time.Time { return now })
	if !errors.Is(err, errQueueEmpty) {
		t.Fatalf("runButler err = %v, want errQueueEmpty", err)
	}
	if !strings.Contains(err.Error(), `chore "bugs" not due: interval not elapsed`) || !strings.Contains(err.Error(), `chore "refactor" not due: interval not elapsed`) {
		t.Errorf("runButler err = %q, want both candidates' reasons named", err)
	}
	if dispatched {
		t.Error("newDispatcher was called; want no Box dispatched when nothing is due")
	}
}

// An empty chores slice is unreachable from cmdButler (butlerPreflight
// guards it), but runButler guards it too rather than formatting the
// reasons-join of nothing into "butler: : queue empty".
func TestRunButler_NoChoresIsError(t *testing.T) {
	repo, _ := newButlerTestRepo(t)
	backend := ledger.Local{Repo: repo}

	err := runButler(backend, forge.NewFake().AsIssueFiler(), testButlerRun(repo), nil, testButlerPolicy(noEvery), nil, func() time.Time { return time.Time{} })
	if err == nil || errors.Is(err, errQueueEmpty) {
		t.Fatalf("runButler err = %v, want a plain error, not errQueueEmpty", err)
	}
}

// (k) cmdButler rejects a chore not named in BUTLER_CHORES, before ever
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

// cmdButler rejects a BUTLER_EVERY override naming a Chore not in
// BUTLER_CHORES, so a misspelled key fails loudly instead of never firing.
func TestCmdButler_RejectsOverrideForChoreNotEnabled(t *testing.T) {
	t.Setenv("FILER_MODEL", "test-model")
	lc := &launchContext{
		config:  config{schemaConfig: schemaConfig{codeForge: "local", butlerChores: "bugs", butlerEvery: "6h bgus=1h"}},
		cleanup: func() {},
	}
	var code int
	stderr := captureStderrFile(t, func() { code = cmdButler(lc, "") })
	if code != 1 {
		t.Errorf("cmdButler code = %d, want 1", code)
	}
	if !strings.Contains(stderr, `override for chore "bgus"`) {
		t.Errorf("stderr = %q, want the rejected override named", stderr)
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

// (m) butlerPreflight guards codeForge, BUTLER_CHORES membership (or, with
// no --chore, BUTLER_CHORES being non-empty at all), BUTLER_CHORE_CLASSES
// syntax, and the Filer gate in that order, before cmdButler ever claims a
// Ledger -- in particular a chore run with no provisioned Filer
// (DRIVER=opencode, or FILER_MODEL="") must be refused up front rather than
// sweep and silently drop findings.
func TestButlerPreflight(t *testing.T) {
	cases := []struct {
		name               string
		codeForge          string
		butlerChores       string
		butlerChoreClasses string
		butlerMaxPerSweep  int
		butlerMaxPerDay    int
		chore              string
		filerEnabled       bool
		wantErr            string // substring of the error, checked in guard order; "" means no error
	}{
		{"all clear", "local", "bugs", "bugs=error-handling", 0, 0, "bugs", true, ""},
		{"github clear", "github", "bugs", "", 0, 0, "bugs", true, ""},
		{"forgejo clear", "forgejo", "bugs", "", 0, 0, "bugs", true, ""},
		{"forge with no ledger rejected", "git", "bugs", "", 0, 0, "bugs", true, "cannot host a butler Ledger (supported: github, forgejo, local)"},
		{"chore not enabled", "local", "other-chore", "", 0, 0, "bugs", true, "is not enabled"},
		{"malformed classes rejected", "local", "bugs", "bugs", 0, 0, "bugs", true, "BUTLER_CHORE_CLASSES"},
		{"filer not provisioned", "local", "bugs", "", 0, 0, "bugs", false, "needs a provisioned Filer"},
		{"forge checked before chore", "git", "other-chore", "", 0, 0, "bugs", false, "cannot host a butler Ledger"},
		{"chore checked before filer", "local", "other-chore", "", 0, 0, "bugs", false, "is not enabled"},
		{"classes checked before filer", "local", "bugs", "bugs", 0, 0, "bugs", false, "BUTLER_CHORE_CLASSES"},
		{"chore with no classes entry passes", "local", "tidy-deps", "bugs=error-handling", 0, 0, "tidy-deps", true, ""},
		{"no chore: all clear with chores enabled", "local", "bugs", "", 0, 0, "", true, ""},
		{"no chore: empty BUTLER_CHORES rejected", "local", "", "", 0, 0, "", true, "BUTLER_CHORES is empty"},
		{"no chore: filer checked after empty-chores guard", "local", "", "", 0, 0, "", false, "BUTLER_CHORES is empty"},
		{"sweep exceeds day rejected", "local", "bugs", "", 6, 5, "bugs", true, "BUTLER_MAX_FINDINGS_PER_SWEEP (6) exceeds BUTLER_MAX_FINDINGS_PER_DAY (5); no run could ever start"},
		{"sweep equals day ok", "local", "bugs", "", 5, 5, "bugs", true, ""},
		{"day zero with sweep set ok", "local", "bugs", "", 5, 0, "bugs", true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config{schemaConfig: schemaConfig{
				codeForge:                 tc.codeForge,
				butlerChores:              tc.butlerChores,
				butlerChoreClasses:        tc.butlerChoreClasses,
				butlerMaxFindingsPerSweep: tc.butlerMaxPerSweep,
				butlerMaxFindingsPerDay:   tc.butlerMaxPerDay,
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

// (n) parseButlerArgs: --chore is optional (an empty return means "pick a
// due Chore"); --no-build is the one other flag butler shares with
// dispatch/research.
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
		{"no args at all: pick a due chore", []string{}, "", false, false},
		{"no-build alone: pick a due chore", []string{"--no-build"}, "", true, false},
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

func TestParseButlerEvery(t *testing.T) {
	cases := []struct {
		name       string
		value      string
		wantErr    bool
		wantChores map[string]time.Duration // For(chore) results to check
	}{
		{"empty value falls back to the 6h default for everyone", "", false, map[string]time.Duration{"bugs": 6 * time.Hour, "other": 6 * time.Hour}},
		{"bare default applies to every chore", "6h", false, map[string]time.Duration{"bugs": 6 * time.Hour, "other": 6 * time.Hour}},
		{"override wins over default", "6h docs-drift=168h", false, map[string]time.Duration{"docs-drift": 168 * time.Hour, "bugs": 6 * time.Hour}},
		{"override with no bare default falls back to the 6h default for others", "docs-drift=168h", false, map[string]time.Duration{"docs-drift": 168 * time.Hour, "bugs": 6 * time.Hour}},
		{"zero duration is valid and does not fall back", "0s", false, map[string]time.Duration{"bugs": 0}},
		{"two bare defaults is an error", "6h 12h", true, nil},
		{"duplicate override is an error", "docs-drift=1h docs-drift=2h", true, nil},
		{"empty chore name is an error", "=1h", true, nil},
		{"unparseable duration is an error", "bogus", true, nil},
		{"negative bare default is an error", "-1h", true, nil},
		{"negative override is an error", "docs-drift=-1h", true, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseButlerEvery(tc.value)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseButlerEvery(%q): got nil error, want one", tc.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseButlerEvery(%q): %v", tc.value, err)
			}
			for chore, want := range tc.wantChores {
				if d := got.For(chore); d != want {
					t.Errorf("parseButlerEvery(%q).For(%q) = %v, want %v", tc.value, chore, d, want)
				}
			}
		})
	}
}

// butlerEveryDefault must match BUTLER_EVERY's schema default, or an unset
// value and an overrides-only value would get different intervals.
func TestButlerEveryDefaultMatchesSchema(t *testing.T) {
	for _, f := range schemaFlags {
		if f.env != "BUTLER_EVERY" {
			continue
		}
		if d, err := time.ParseDuration(f.dflt); err != nil || d != butlerEveryDefault {
			t.Errorf("schema default %q, want %v (butlerEveryDefault)", f.dflt, butlerEveryDefault)
		}
		return
	}
	t.Fatal("BUTLER_EVERY missing from schemaFlags")
}

func TestButlerEveryConfigCheckOverrides(t *testing.T) {
	cases := []struct {
		name         string
		value        string
		butlerChores string
		wantErr      string // substring, or "" for nil
	}{
		{"no overrides", "6h", "bugs", ""},
		{"override matches an enabled chore", "docs-drift=168h", "bugs docs-drift", ""},
		{"override names a chore not enabled", "docs-drift=168h", "bugs", "docs-drift"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			every, err := parseButlerEvery(tc.value)
			if err != nil {
				t.Fatalf("parseButlerEvery(%q): %v", tc.value, err)
			}
			err = every.checkOverrides(tc.butlerChores)
			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("checkOverrides(%q) = %v, want nil", tc.butlerChores, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("checkOverrides(%q) = %v, want error containing %q", tc.butlerChores, err, tc.wantErr)
			}
		})
	}
}

func TestParseButlerClaimTimeout(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		want    time.Duration
		wantErr bool
	}{
		{"valid duration", "6h", 6 * time.Hour, false},
		{"short valid duration", "30m", 30 * time.Minute, false},
		{"zero is rejected", "0s", 0, true},
		{"negative is rejected", "-1h", 0, true},
		{"unparseable is rejected", "bogus", 0, true},
		{"empty is rejected", "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseButlerClaimTimeout(tc.value)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseButlerClaimTimeout(%q): got nil error, want one", tc.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseButlerClaimTimeout(%q): %v", tc.value, err)
			}
			if got != tc.want {
				t.Errorf("parseButlerClaimTimeout(%q) = %v, want %v", tc.value, got, tc.want)
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

// (j) Budgets are global across every enabled Chore, not per Chore (ADR
// 0056): a sweep already claimed today on one enabled Chore spends the
// shared day's sweep budget, so a distinct due candidate on another enabled
// Chore reports the budget spent rather than running.
func TestRunButler_BudgetSpentByAnotherEnabledChore(t *testing.T) {
	repo, _ := newButlerTestRepo(t)
	backend := ledger.Local{Repo: repo}

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	// A Claimed commit on "refactor" -- a run already under way -- counts
	// toward today's cross-Chore Claims regardless of which Chore it is on.
	if _, err := ledger.Claim(backend, "refactor", ledger.Tip{}, ledger.ClaimedBy{Host: "seed-host", Start: now}); err != nil {
		t.Fatalf("seed Claim: %v", err)
	}

	dispatched := false
	newDispatcher := func(c dispatch.Chore) dispatch.Dispatcher {
		dispatched = true
		return dispatch.NewFake()
	}

	policy := testButlerPolicy(noEvery, "bugs", "refactor")
	policy.budgets = butler.Budgets{MaxSweepsPerDay: 1}
	err := runButler(backend, forge.NewFake().AsIssueFiler(), testButlerRun(repo), []string{"bugs"}, policy, newDispatcher, func() time.Time { return now })
	if !errors.Is(err, errQueueEmpty) {
		t.Fatalf("runButler err = %v, want errQueueEmpty", err)
	}
	if !strings.Contains(err.Error(), "daily sweep budget spent") {
		t.Errorf("runButler err = %q, want it to say daily sweep budget spent", err)
	}
	if dispatched {
		t.Error("newDispatcher was called; want no Box dispatched once the shared sweep budget is spent")
	}
}

// (k) The day a budget resets in runs against policy.zone, not UTC or the
// host's own zone: a Claimed+Done run at 23:30 America/New_York on one date
// is 04:30 UTC the *next* date, so this instant lands on different calendar
// dates depending which zone decides the boundary. Reading at 23:50 the same
// NY evening still finds the budget spent; reading at 00:10 the following NY
// morning finds a fresh day and runs.
func TestRunButler_DayBoundaryUsesConfiguredZoneNotUTC(t *testing.T) {
	repo, head := newButlerTestRepo(t)
	backend := ledger.Local{Repo: repo}

	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	seedAt := time.Date(2026, 1, 10, 23, 30, 0, 0, loc) // 2026-01-11 04:30 UTC
	claim, err := ledger.Claim(backend, "bugs", ledger.Tip{}, ledger.ClaimedBy{Host: "seed-host", Start: seedAt})
	if err != nil {
		t.Fatalf("seed Claim: %v", err)
	}
	if _, err := ledger.Finish(backend, "bugs", claim, ledger.State{LastSwept: head}, seedAt); err != nil {
		t.Fatalf("seed Finish: %v", err)
	}
	// Something new since the seeded run, so the only thing left to block a
	// later due check is the budget itself.
	addButlerCommit(t, repo, "newfile.go", "package a\n")

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9099"
	dispatched := false
	newDispatcher := func(c dispatch.Chore) dispatch.Dispatcher {
		dispatched = true
		return readyDispatcher()
	}

	policy := testButlerPolicy(noEvery, "bugs")
	policy.budgets = butler.Budgets{MaxSweepsPerDay: 1}
	policy.zone = loc

	t.Run("same NY day: budget still spent", func(t *testing.T) {
		dispatched = false
		now := time.Date(2026, 1, 10, 23, 50, 0, 0, loc)
		err := runButler(backend, fc.AsIssueFiler(), testButlerRun(repo), []string{"bugs"}, policy, newDispatcher, func() time.Time { return now })
		if !errors.Is(err, errQueueEmpty) {
			t.Fatalf("runButler err = %v, want errQueueEmpty", err)
		}
		if !strings.Contains(err.Error(), "daily sweep budget spent") {
			t.Errorf("runButler err = %q, want daily sweep budget spent", err)
		}
		if dispatched {
			t.Error("newDispatcher was called; want no Box dispatched while still inside the spent NY day")
		}
	})

	t.Run("next NY day: due again", func(t *testing.T) {
		dispatched = false
		now := time.Date(2026, 1, 11, 0, 10, 0, 0, loc)
		if err := runButler(backend, fc.AsIssueFiler(), testButlerRun(repo), []string{"bugs"}, policy, newDispatcher, func() time.Time { return now }); err != nil {
			t.Fatalf("runButler: %v", err)
		}
		if !dispatched {
			t.Error("newDispatcher was not called; want a Box dispatched once the NY day rolled over and the budget reset")
		}
	})
}

// (l) MaxFindingsPerSweep reaches settle.NewButlerSettle through
// runOneButlerChore: a run that relays more findings than the cap gets the
// overflow dropped, and the Ledger done commit records it (Dropped).
func TestRunButler_PerSweepCapDropsExcessFindingsInLedger(t *testing.T) {
	repo, _ := newButlerTestRepo(t)
	backend := ledger.Local{Repo: repo}

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9100"

	d := dispatch.NewFake()
	d.RunResult = dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "butler-bugs", Status: outcome.StatusReady, Note: "swept"},
		},
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"bug one","body":"repro","dedupTerms":["a.go:One"]}`,
			`{"title":"bug two","body":"repro","dedupTerms":["a.go:Two"]}`,
		},
	}
	newDispatcher := func(c dispatch.Chore) dispatch.Dispatcher { return d }

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	policy := testButlerPolicy(noEvery, "bugs")
	policy.budgets = butler.Budgets{MaxFindingsPerSweep: 1}
	if err := runButler(backend, fc.AsIssueFiler(), testButlerRun(repo), []string{"bugs"}, policy, newDispatcher, func() time.Time { return now }); err != nil {
		t.Fatalf("runButler: %v", err)
	}

	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if tip.State.Dropped != 1 {
		t.Errorf("Dropped = %d, want 1", tip.State.Dropped)
	}
	if len(tip.State.Filed) != 1 {
		t.Errorf("len(Filed) = %d, want 1", len(tip.State.Filed))
	}
}
