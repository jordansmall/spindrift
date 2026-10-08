package butler

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	bkd "spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/ledger/ledgertest"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/testutil"
)

// testRunClaimTimeout is the claim timeout every Sweep test below passes
// explicitly, migrated from cmd/launcher's retired testClaimTimeout (issue
// #3990).
const testRunClaimTimeout = 6 * time.Hour

// noRunEvery is the zero Every: it never blocks a due check
// (IntervalNotElapsed), the shape most tests below want when the interval
// itself isn't what's under test. Distinct from sweep_test.go's fixed-shape
// testPolicy, since the tests below each build their own Policy (chores,
// promotion knobs) per case.
const noRunEvery time.Duration = 0

// testRunPolicy builds a Policy for the tests below: testRunClaimTimeout, an
// unlimited (zero) Budgets, UTC, ready-for-agent as the promotion label, and
// Chores built from chores, each with the same Every and no PromotionClasses --
// migrated from cmd/launcher's retired testButlerPolicy.
func testRunPolicy(every time.Duration, chores ...string) Policy {
	cs := make([]chore.Chore, len(chores))
	for i, name := range chores {
		cs[i] = chore.Chore{Name: name, Every: every}
	}
	return Policy{
		Branch:         "main",
		Host:           "test-host",
		Chores:         cs,
		ClaimTimeout:   testRunClaimTimeout,
		Zone:           time.UTC,
		PromotionLabel: "ready-for-agent",
	}
}

// withClasses returns chores with classes attached to the entry named name,
// the shape promotion tests build their Policy.Chores from.
func withClasses(chores []chore.Chore, name string, classes ...string) []chore.Chore {
	for i, c := range chores {
		if c.Name == name {
			chores[i].PromotionClasses = classes
		}
	}
	return chores
}

// withClassList sets the Chore's closed finding-class list, which (unlike
// PromotionClasses) reaches the Box whatever the promotion budget.
func withClassList(chores []chore.Chore, name string, classes ...string) []chore.Chore {
	for i, c := range chores {
		if c.Name == name {
			chores[i].ClassList = classes
		}
	}
	return chores
}

// withPatchClasses is withClasses' patch-rung sibling (issue #4072, ADR
// 0057).
func withPatchClasses(chores []chore.Chore, name string, classes ...string) []chore.Chore {
	for i, c := range chores {
		if c.Name == name {
			chores[i].PatchClasses = classes
		}
	}
	return chores
}

func runRepoGitOutput(t *testing.T, repo string, args ...string) []byte {
	t.Helper()
	full := append([]string{"-C", repo}, args...)
	out, err := exec.Command("git", full...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", full, err)
	}
	return out
}

// readyDispatcher builds a dispatch.Fake whose Run() reports a ready outcome
// carrying one filed-issue intent, the "clean run" shape settle expects
// (settle_test.go's readyResult mirrors this).
func readyDispatcher() *dispatch.Fake {
	d := dispatch.NewFake()
	d.RunResult = dispatch.Succeeded(dispatch.Result{
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "butler-bugs", Status: outcome.StatusReady, Note: "swept"},
		},
		IssueIntentsFound: true,
		IssueIntents:      []string{`{"title":"bug found","body":"repro","dedupTerms":["a.go:Foo"]}`},
	})
	return d
}

// crashedDispatcher builds a dispatch.Fake whose Run() reports no outcome
// line at all, the shape a killed or crashed Box leaves.
func crashedDispatcher() *dispatch.Fake {
	d := dispatch.NewFake()
	d.RunResult = dispatch.Failed(dispatch.Result{})
	return d
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

// promotableDispatcher builds a dispatch.Fake whose one relayed finding
// carries class, one file, and a reviewer concurrence -- the shape that
// clears every settle-side promotion gate whenever the host policy allows
// class (issue #3880). Mirrors readyDispatcher's outcome shape.
func promotableDispatcher(class string) *dispatch.Fake {
	d := dispatch.NewFake()
	d.RunResult = dispatch.Succeeded(dispatch.Result{
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "butler-bugs", Status: outcome.StatusReady, Note: "swept"},
		},
		IssueIntentsFound: true,
		IssueIntents: []string{
			fmt.Sprintf(`{"title":"bug found","body":"repro","dedupTerms":["a.go:Foo"],"class":%q,"concurrence":"agreed"}`, class),
		},
	})
	return d
}

// (a) A clean run claims, dispatches, files its one finding, and writes a
// done commit carrying lastSwept=head and the filed URL -- visible in the
// Accumulation repo's refs/spindrift/butler/<chore> ref, the issue's own
// acceptance criterion (`git log refs/spindrift/butler/bugs`). A
// Consumer-declared Chore ("tidy-deps") runs exactly like a built-in one.
func TestSweep_CleanRunFilesAndWritesDoneCommit(t *testing.T) {
	for _, choreName := range []string{"bugs", "tidy-deps"} {
		t.Run(choreName, func(t *testing.T) {
			repo := ledgertest.NewRepo(t)
			backend := ledger.Local{Repo: repo}
			const head = "headsha"
			tree := fakeTree{head: head, files: []string{"a.go"}}

			fc := forge.NewFake()
			fc.PostIssueURL = "https://example.com/issues/9001"

			d := readyDispatcher()
			newBox := func(c dispatch.Chore) dispatch.Dispatcher {
				if c.Name != choreName || c.Branch != "main" {
					t.Fatalf("newBox chore = %+v, want Name=%s Branch=main", c, choreName)
				}
				if c.Scope.Head != head {
					t.Fatalf("newBox scope.Head = %q, want %q", c.Scope.Head, head)
				}
				return d
			}

			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			r := New(backend, tree, fc.AsIssueFiler(), newBox, testRunPolicy(noRunEvery, choreName), func() time.Time { return now })
			out, err := r.Sweep([]string{choreName})
			if err != nil {
				t.Fatalf("Sweep: %v", err)
			}
			if out.Kind != Swept || out.Chore != choreName || out.Filed != 1 {
				t.Fatalf("Outcome = %+v, want Kind=Swept Chore=%s Filed=1", out, choreName)
			}

			subjects := gitLogSubjects(t, repo, ledger.RefPrefix+choreName)
			if len(subjects) != 2 || subjects[0] != choreName+": done" || subjects[1] != choreName+": claimed" {
				t.Fatalf("git log subjects = %v, want [%s: done, %s: claimed]", subjects, choreName, choreName)
			}

			tip, err := backend.Read(choreName)
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

// TestSweep_PassesRoomFindingsAsBoxMaxFindings pins issue #3994's butler
// half: run() reads room.Findings (chore.Budgets.Room, computed once per
// Sweep) into dispatch.Chore.MaxFindings, so the Box this run dispatches
// carries the day's actual per-sweep cap rather than the socket's own
// hardcoded default.
func TestSweep_PassesRoomFindingsAsBoxMaxFindings(t *testing.T) {
	const choreName = "bugs"
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}

	d := readyDispatcher()
	var got dispatch.Chore
	newBox := func(c dispatch.Chore) dispatch.Dispatcher {
		got = c
		return d
	}

	policy := testRunPolicy(noRunEvery, choreName)
	policy.Budgets = chore.Budgets{MaxFindingsPerSweep: 12}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := New(backend, fakeTree{head: "headsha", files: []string{"a.go"}}, forge.NewFake().AsIssueFiler(), newBox, policy, func() time.Time { return now })
	if _, err := r.Sweep([]string{choreName}); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	if got.MaxFindings != 12 {
		t.Errorf("newBox chore.MaxFindings = %d, want 12 (Budgets.MaxFindingsPerSweep)", got.MaxFindings)
	}
}

// (b) A crashed run (no ready outcome) leaves the claim standing: settle
// writes nothing, so the Ledger tip after the run is still the claim, and
// Sweep reports ClaimLeft.
func TestSweep_CrashedRunLeavesClaimStanding(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	tree := fakeTree{head: "headsha", files: []string{"a.go"}}

	newBox := func(c dispatch.Chore) dispatch.Dispatcher { return crashedDispatcher() }

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := New(backend, tree, forge.NewFake().AsIssueFiler(), newBox, testRunPolicy(noRunEvery, "bugs"), func() time.Time { return now })
	out, err := r.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != ClaimLeft || out.Chore != "bugs" {
		t.Errorf("Outcome = %+v, want Kind=ClaimLeft Chore=bugs", out)
	}

	tip, rerr := backend.Read("bugs")
	if rerr != nil {
		t.Fatalf("Read: %v", rerr)
	}
	if tip.State.Phase != ledger.Claimed {
		t.Errorf("Phase = %q, want %q (claim left standing)", tip.State.Phase, ledger.Claimed)
	}
}

// (b2) A Box skipped because a live run already holds the Chore (issue #562,
// #3705) is not a failure: Sweep reports ClaimLeft without settling, so no
// settled record, no status=failed line, no tracker call, and no Ledger
// commit past the claim.
func TestSweep_AlreadyInFlightSkipsSettle(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)
	repo := ledgertest.NewRepo(t)
	backend := ledger.Local{Repo: repo}
	tree := fakeTree{head: "headsha", files: []string{"a.go"}}
	fc := forge.NewFake()

	d := dispatch.NewFake()
	d.RunResult = dispatch.Skipped()
	newBox := func(c dispatch.Chore) dispatch.Dispatcher { return d }

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := New(backend, tree, fc.AsIssueFiler(), newBox, testRunPolicy(noRunEvery, "bugs"), func() time.Time { return now })
	var out Outcome
	var err error
	stdout := captureStdout(t, func() {
		out, err = r.Sweep([]string{"bugs"})
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != ClaimLeft || out.Chore != "bugs" {
		t.Errorf("Outcome = %+v, want Kind=ClaimLeft Chore=bugs", out)
	}
	if d.RunCalls != 1 {
		t.Errorf("dispatcher Run calls = %d, want 1 (no retry)", d.RunCalls)
	}
	if recs := readRecords(); len(recs) != 0 {
		t.Errorf("report records = %+v, want none", recs)
	}
	if strings.Contains(stdout, "status=failed") || !strings.Contains(stdout, "status=already-in-flight") {
		t.Errorf("stdout = %q, want an already-in-flight skip line and no status=failed", stdout)
	}
	if len(fc.PostIssueCalls) != 0 {
		t.Errorf("PostIssueCalls = %v, want none", fc.PostIssueCalls)
	}
	subjects := gitLogSubjects(t, repo, ledger.RefPrefix+"bugs")
	if len(subjects) != 1 || subjects[0] != "bugs: claimed" {
		t.Errorf("git log subjects = %v, want [bugs: claimed]", subjects)
	}
}

// A claim is stamped with the instant its due check used, not a second now()
// reading: with a clock that advances on every call, the crashed run's
// standing claim must still carry the first reading.
func TestSweep_ClaimStartIsDueCheckInstant(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	tree := fakeTree{head: "headsha", files: []string{"a.go"}}

	newBox := func(c dispatch.Chore) dispatch.Dispatcher { return crashedDispatcher() }

	first := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	calls := 0
	now := func() time.Time {
		calls++
		return first.Add(time.Duration(calls-1) * time.Hour)
	}
	r := New(backend, tree, forge.NewFake().AsIssueFiler(), newBox, testRunPolicy(noRunEvery, "bugs"), now)
	if _, err := r.Sweep([]string{"bugs"}); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if tip.State.ClaimedBy == nil || !tip.State.ClaimedBy.Start.Equal(first) {
		t.Errorf("ClaimedBy = %+v, want Start %v (the due check's reading)", tip.State.ClaimedBy, first)
	}
}

// (c) A live (non-stale) claim held by another run makes Sweep report
// NotDue without dispatching a Box or writing a new commit.
func TestSweep_LiveClaimReportsNoWork(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	tree := fakeTree{head: "headsha", files: []string{"a.go"}}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim, err := ledger.Claim(backend, "bugs", ledger.Tip{}, ledger.ClaimedBy{Host: "other-host", Start: now})
	if err != nil {
		t.Fatalf("seed Claim: %v", err)
	}

	dispatched := false
	newBox := func(c dispatch.Chore) dispatch.Dispatcher {
		dispatched = true
		return dispatch.NewFake()
	}

	r := New(backend, tree, forge.NewFake().AsIssueFiler(), newBox, testRunPolicy(noRunEvery, "bugs"), func() time.Time { return now.Add(time.Minute) })
	out, err := r.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != NotDue {
		t.Fatalf("Kind = %v, want NotDue: %+v", out.Kind, out)
	}
	want := `chore "bugs" not due: claimed by another run`
	if len(out.Reasons) != 1 || out.Reasons[0] != want {
		t.Errorf("Reasons = %v, want [%q]", out.Reasons, want)
	}
	if dispatched {
		t.Error("newBox was called; want no Box dispatched against a live claim")
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
// (ADR 0056): the next run takes it over rather than reporting NotDue, and a
// clean sweep against it still ends in a done commit.
func TestSweep_StaleClaimIsTakenOver(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	tree := fakeTree{head: "headsha", files: []string{"a.go"}}

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := ledger.Claim(backend, "bugs", ledger.Tip{}, ledger.ClaimedBy{Host: "dead-host", Start: start}); err != nil {
		t.Fatalf("seed Claim: %v", err)
	}

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9002"
	dispatched := false
	newBox := func(c dispatch.Chore) dispatch.Dispatcher {
		dispatched = true
		return readyDispatcher()
	}

	now := start.Add(testRunClaimTimeout + time.Minute)
	r := New(backend, tree, fc.AsIssueFiler(), newBox, testRunPolicy(noRunEvery, "bugs"), func() time.Time { return now })
	out, err := r.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if !dispatched {
		t.Fatal("newBox was not called; want the stale claim taken over and a Box dispatched")
	}
	if out.Kind != Swept || out.Chore != "bugs" {
		t.Errorf("Outcome = %+v, want Kind=Swept Chore=bugs", out)
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
// takes the stale claim over rather than reporting NotDue, sweeps
// lastSwept..newHead, resumes the tree walk strictly after the old cursor
// (never restarting it), and ends in a done commit -- the full claimed(seed)
// -> done(seed) -> claimed(stale) -> done(final) chain, newest first. head1
// and head2 stand in for the branch's tip before and after work "lands"
// between the seed and this run (fakeTree, no real git needed -- issue
// #3995).
func TestSweep_StaleClaimTakeoverEndToEnd(t *testing.T) {
	const head1, head2 = "head1", "head2"
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}

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

	tree := fakeTree{head: head2, files: []string{"a.go", "b.go"}}

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9003"
	var dispatchedChore dispatch.Chore
	dispatched := false
	newBox := func(c dispatch.Chore) dispatch.Dispatcher {
		dispatched = true
		dispatchedChore = c
		return readyDispatcher()
	}

	now := deadStart.Add(testRunClaimTimeout + time.Minute)
	r := New(backend, tree, fc.AsIssueFiler(), newBox, testRunPolicy(noRunEvery, "bugs"), func() time.Time { return now })
	out, err := r.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if !dispatched {
		t.Fatal("newBox was not called; want the stale claim taken over and a Box dispatched")
	}
	if out.Kind != Swept {
		t.Errorf("Outcome.Kind = %v, want Swept", out.Kind)
	}

	wantDiff := head1 + ".." + head2
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
	if tip.State.LastSwept != head2 {
		t.Errorf("LastSwept = %q, want %q", tip.State.LastSwept, head2)
	}

	// Newest first: the final done commit, the takeover's own new claimed
	// commit (ledger.Claim always appends a fresh state, never reusing the
	// stale one), the dead host's now-superseded claim, and the seed done
	// commit it was taken over on top of.
	subjects := gitLogSubjects(t, backend.Repo, ledger.RefPrefix+"bugs")
	want := []string{"bugs: done", "bugs: claimed", "bugs: claimed", "bugs: done", "bugs: claimed"}
	if !reflect.DeepEqual(subjects, want) {
		t.Errorf("git log subjects = %v, want %v", subjects, want)
	}
}

// (e) A claim just younger than the claim timeout is still live: Sweep
// reports NotDue without taking it over or dispatching a Box.
func TestSweep_ClaimJustUnderTimeoutStillLive(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	tree := fakeTree{head: "headsha", files: []string{"a.go"}}

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim, err := ledger.Claim(backend, "bugs", ledger.Tip{}, ledger.ClaimedBy{Host: "other-host", Start: start})
	if err != nil {
		t.Fatalf("seed Claim: %v", err)
	}

	dispatched := false
	newBox := func(c dispatch.Chore) dispatch.Dispatcher {
		dispatched = true
		return dispatch.NewFake()
	}

	now := start.Add(testRunClaimTimeout - time.Minute)
	r := New(backend, tree, forge.NewFake().AsIssueFiler(), newBox, testRunPolicy(noRunEvery, "bugs"), func() time.Time { return now })
	out, err := r.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != NotDue {
		t.Fatalf("Kind = %v, want NotDue: %+v", out.Kind, out)
	}
	if dispatched {
		t.Error("newBox was called; want no Box dispatched against a claim not yet stale")
	}

	tip, rerr := backend.Read("bugs")
	if rerr != nil {
		t.Fatalf("Read: %v", rerr)
	}
	if tip.Commit != claim.Commit {
		t.Errorf("tip.Commit = %s, want unchanged from the seeded claim %s", tip.Commit, claim.Commit)
	}
}

// (g) A named chore not yet due by interval reports why and NotDue, without
// claiming or dispatching. (ledger.Claim losing the race, the "f" case in
// cmd/launcher's retired suite, is covered by TestSweep_LostRace above.)
func TestSweep_NamedChoreNotDueByIntervalReportsWhy(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	const head = "headsha"
	tree := fakeTree{head: head, files: []string{"a.go"}}

	doneAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim, err := ledger.Claim(backend, "bugs", ledger.Tip{}, ledger.ClaimedBy{Host: "seed-host", Start: doneAt})
	if err != nil {
		t.Fatalf("seed Claim: %v", err)
	}
	if _, err := ledger.Finish(backend, "bugs", claim, ledger.State{LastSwept: head}, doneAt); err != nil {
		t.Fatalf("seed Finish: %v", err)
	}
	beforeSubjects := gitLogSubjects(t, backend.Repo, ledger.RefPrefix+"bugs")

	dispatched := false
	newBox := func(c dispatch.Chore) dispatch.Dispatcher {
		dispatched = true
		return dispatch.NewFake()
	}

	now := doneAt.Add(time.Hour) // well inside the 6h interval
	r := New(backend, tree, forge.NewFake().AsIssueFiler(), newBox, testRunPolicy(6*time.Hour, "bugs"), func() time.Time { return now })
	out, err := r.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != NotDue {
		t.Fatalf("Kind = %v, want NotDue: %+v", out.Kind, out)
	}
	want := `chore "bugs" not due: interval not elapsed`
	if len(out.Reasons) != 1 || out.Reasons[0] != want {
		t.Errorf("Reasons = %v, want [%q]", out.Reasons, want)
	}
	if dispatched {
		t.Error("newBox was called; want no Box dispatched against a chore not due")
	}

	afterSubjects := gitLogSubjects(t, backend.Repo, ledger.RefPrefix+"bugs")
	if !reflect.DeepEqual(afterSubjects, beforeSubjects) {
		t.Errorf("git log subjects = %v, want unchanged %v", afterSubjects, beforeSubjects)
	}
}

// (h) A named chore fully rotated through the tree, with head unmoved since
// the last sweep, reports "nothing to scan" and NotDue.
func TestSweep_NamedChoreNothingToScanReportsWhy(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	const head = "headsha"
	tree := fakeTree{head: head, files: []string{"a.go"}}

	fc := forge.NewFake()
	newBox := func(c dispatch.Chore) dispatch.Dispatcher { return readyDispatcher() }
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := New(backend, tree, fc.AsIssueFiler(), newBox, testRunPolicy(noRunEvery, "bugs"), func() time.Time { return now })
	if _, err := r.Sweep([]string{"bugs"}); err != nil {
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
	newBox2 := func(c dispatch.Chore) dispatch.Dispatcher {
		dispatched = true
		return dispatch.NewFake()
	}
	r2 := New(backend, tree, fc.AsIssueFiler(), newBox2, testRunPolicy(noRunEvery, "bugs"), func() time.Time { return now.Add(time.Hour) })
	out, err := r2.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != NotDue {
		t.Fatalf("Kind = %v, want NotDue: %+v", out.Kind, out)
	}
	want := `chore "bugs" not due: nothing to scan`
	if len(out.Reasons) != 1 || out.Reasons[0] != want {
		t.Errorf("Reasons = %v, want [%q]", out.Reasons, want)
	}
	if dispatched {
		t.Error("newBox was called; want no Box dispatched with nothing left to scan")
	}
}

// (i) With no --chore (a multi-chore candidate list), Sweep picks the first
// due candidate in order, skipping one that isn't due yet and leaving its
// Ledger untouched.
func TestSweep_NoChorePicksFirstDueCandidate(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	const head = "headsha"
	tree := fakeTree{head: head, files: []string{"a.go"}}

	doneAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim, err := ledger.Claim(backend, "bugs", ledger.Tip{}, ledger.ClaimedBy{Host: "seed-host", Start: doneAt})
	if err != nil {
		t.Fatalf("seed Claim: %v", err)
	}
	if _, err := ledger.Finish(backend, "bugs", claim, ledger.State{LastSwept: head}, doneAt); err != nil {
		t.Fatalf("seed Finish: %v", err)
	}
	beforeSubjects := gitLogSubjects(t, backend.Repo, ledger.RefPrefix+"bugs")

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9004"
	var dispatchedChore dispatch.Chore
	newBox := func(c dispatch.Chore) dispatch.Dispatcher {
		dispatchedChore = c
		return readyDispatcher()
	}

	now := doneAt.Add(time.Hour)
	r := New(backend, tree, fc.AsIssueFiler(), newBox, testRunPolicy(6*time.Hour, "bugs", "refactor"), func() time.Time { return now })
	out, err := r.Sweep([]string{"bugs", "refactor"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != Swept || out.Chore != "refactor" {
		t.Errorf("Outcome = %+v, want Kind=Swept Chore=refactor (bugs is not due yet)", out)
	}
	if dispatchedChore.Name != "refactor" {
		t.Errorf("dispatched chore = %q, want refactor (bugs is not due yet)", dispatchedChore.Name)
	}

	afterSubjects := gitLogSubjects(t, backend.Repo, ledger.RefPrefix+"bugs")
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

// (j) With no --chore, when nothing is due, Sweep reports NotDue naming
// every candidate's own reason. (An empty chores slice is
// TestSweep_EmptyChoresErrors above.)
func TestSweep_NoChoreNoneDueReportsEachReason(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	const head = "headsha"
	tree := fakeTree{head: head, files: []string{"a.go"}}

	doneAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, choreName := range []string{"bugs", "refactor"} {
		claim, err := ledger.Claim(backend, choreName, ledger.Tip{}, ledger.ClaimedBy{Host: "seed-host", Start: doneAt})
		if err != nil {
			t.Fatalf("seed Claim(%s): %v", choreName, err)
		}
		if _, err := ledger.Finish(backend, choreName, claim, ledger.State{LastSwept: head}, doneAt); err != nil {
			t.Fatalf("seed Finish(%s): %v", choreName, err)
		}
	}

	dispatched := false
	newBox := func(c dispatch.Chore) dispatch.Dispatcher {
		dispatched = true
		return dispatch.NewFake()
	}

	now := doneAt.Add(time.Hour)
	r := New(backend, tree, forge.NewFake().AsIssueFiler(), newBox, testRunPolicy(6*time.Hour, "bugs", "refactor"), func() time.Time { return now })
	out, err := r.Sweep([]string{"bugs", "refactor"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != NotDue {
		t.Fatalf("Kind = %v, want NotDue: %+v", out.Kind, out)
	}
	want := []string{`chore "bugs" not due: interval not elapsed`, `chore "refactor" not due: interval not elapsed`}
	if !reflect.DeepEqual(out.Reasons, want) {
		t.Errorf("Reasons = %v, want %v", out.Reasons, want)
	}
	if dispatched {
		t.Error("newBox was called; want no Box dispatched when nothing is due")
	}
}

// (j2) Budgets are global across every enabled Chore, not per Chore (ADR
// 0056): a sweep already claimed today on one enabled Chore spends the
// shared day's sweep budget, so a distinct due candidate on another enabled
// Chore reports the budget spent rather than running.
func TestSweep_BudgetSpentByAnotherEnabledChore(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	tree := fakeTree{head: "headsha", files: []string{"a.go"}}

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	// A Claimed commit on "refactor" -- a run already under way -- counts
	// toward today's cross-Chore Claims regardless of which Chore it is on.
	if _, err := ledger.Claim(backend, "refactor", ledger.Tip{}, ledger.ClaimedBy{Host: "seed-host", Start: now}); err != nil {
		t.Fatalf("seed Claim: %v", err)
	}

	dispatched := false
	newBox := func(c dispatch.Chore) dispatch.Dispatcher {
		dispatched = true
		return dispatch.NewFake()
	}

	policy := testRunPolicy(noRunEvery, "bugs", "refactor")
	policy.Budgets = chore.Budgets{MaxSweepsPerDay: 1}
	r := New(backend, tree, forge.NewFake().AsIssueFiler(), newBox, policy, func() time.Time { return now })
	out, err := r.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != NotDue {
		t.Fatalf("Kind = %v, want NotDue: %+v", out.Kind, out)
	}
	want := `chore "bugs" not due: daily sweep budget spent`
	if len(out.Reasons) != 1 || out.Reasons[0] != want {
		t.Errorf("Reasons = %v, want [%q]", out.Reasons, want)
	}
	if dispatched {
		t.Error("newBox was called; want no Box dispatched once the shared sweep budget is spent")
	}
}

// (k) The day a budget resets in runs against policy.Zone, not UTC or the
// host's own zone: a Claimed+Done run at 23:30 America/New_York on one date
// is 04:30 UTC the *next* date, so this instant lands on different calendar
// dates depending which zone decides the boundary. Reading at 23:50 the same
// NY evening still finds the budget spent; reading at 00:10 the following NY
// morning finds a fresh day and runs. head1/head2 stand in for the branch
// tip before/after the seeded run, as above (fakeTree, no real git needed).
func TestSweep_DayBoundaryUsesConfiguredZoneNotUTC(t *testing.T) {
	const head1, head2 = "head1", "head2"
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	// The current tree head (head2) differs from the seeded LastSwept
	// (head1), so the only thing left to block a later due check is the
	// budget itself.
	tree := fakeTree{head: head2, files: []string{"a.go", "newfile.go"}}

	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	seedAt := time.Date(2026, 1, 10, 23, 30, 0, 0, loc) // 2026-01-11 04:30 UTC
	claim, err := ledger.Claim(backend, "bugs", ledger.Tip{}, ledger.ClaimedBy{Host: "seed-host", Start: seedAt})
	if err != nil {
		t.Fatalf("seed Claim: %v", err)
	}
	if _, err := ledger.Finish(backend, "bugs", claim, ledger.State{LastSwept: head1}, seedAt); err != nil {
		t.Fatalf("seed Finish: %v", err)
	}

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9099"
	dispatched := false
	newBox := func(c dispatch.Chore) dispatch.Dispatcher {
		dispatched = true
		return readyDispatcher()
	}

	policy := testRunPolicy(noRunEvery, "bugs")
	policy.Budgets = chore.Budgets{MaxSweepsPerDay: 1}
	policy.Zone = loc

	t.Run("same NY day: budget still spent", func(t *testing.T) {
		dispatched = false
		now := time.Date(2026, 1, 10, 23, 50, 0, 0, loc)
		r := New(backend, tree, fc.AsIssueFiler(), newBox, policy, func() time.Time { return now })
		out, err := r.Sweep([]string{"bugs"})
		if err != nil {
			t.Fatalf("Sweep: %v", err)
		}
		if out.Kind != NotDue {
			t.Fatalf("Kind = %v, want NotDue: %+v", out.Kind, out)
		}
		want := `chore "bugs" not due: daily sweep budget spent`
		if len(out.Reasons) != 1 || out.Reasons[0] != want {
			t.Errorf("Reasons = %v, want [%q]", out.Reasons, want)
		}
		if dispatched {
			t.Error("newBox was called; want no Box dispatched while still inside the spent NY day")
		}
	})

	t.Run("next NY day: due again", func(t *testing.T) {
		dispatched = false
		now := time.Date(2026, 1, 11, 0, 10, 0, 0, loc)
		r := New(backend, tree, fc.AsIssueFiler(), newBox, policy, func() time.Time { return now })
		out, err := r.Sweep([]string{"bugs"})
		if err != nil {
			t.Fatalf("Sweep: %v", err)
		}
		if out.Kind != Swept {
			t.Errorf("Kind = %v, want Swept", out.Kind)
		}
		if !dispatched {
			t.Error("newBox was not called; want a Box dispatched once the NY day rolled over and the budget reset")
		}
	})
}

// (l) MaxFindingsPerSweep reaches settle through Sweep's run step: a run
// that relays more findings than the cap gets the overflow dropped, and the
// Ledger done commit records it (Dropped), matching Outcome.Dropped.
func TestSweep_PerSweepCapDropsExcessFindingsInLedger(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	tree := fakeTree{head: "headsha", files: []string{"a.go"}}

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9100"

	d := dispatch.NewFake()
	d.RunResult = dispatch.Succeeded(dispatch.Result{
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "butler-bugs", Status: outcome.StatusReady, Note: "swept"},
		},
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"bug one","body":"repro","dedupTerms":["a.go:One"]}`,
			`{"title":"bug two","body":"repro","dedupTerms":["a.go:Two"]}`,
		},
	})
	newBox := func(c dispatch.Chore) dispatch.Dispatcher { return d }

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	policy := testRunPolicy(noRunEvery, "bugs")
	policy.Budgets = chore.Budgets{MaxFindingsPerSweep: 1}
	r := New(backend, tree, fc.AsIssueFiler(), newBox, policy, func() time.Time { return now })
	out, err := r.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != Swept || out.Filed != 1 || out.Dropped != 1 {
		t.Fatalf("Outcome = %+v, want Kind=Swept Filed=1 Dropped=1", out)
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

// (l2) BUTLER_MAX_FINDINGS_PER_SWEEP=0 (no per-sweep limit configured on
// top of a day cap) still bounds a sweep at the day's remaining finding
// headroom, not an uncapped run (due.go's MaxFindingsPerSweep doc): with
// MaxFindingsPerDay=10 and 4 already filed today, a Box relaying more than
// 6 findings gets only 6 filed, the rest dropped (issue #3994 review
// finding).
func TestSweep_ZeroPerSweepCapStillBoundedByDayHeadroom(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9101"
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Seed today's Filed=4 through a real Sweep against a different Chore --
	// Budgets are global across every enabled Chore (chore.Budgets' own
	// doc), not per Chore, so this counts toward "bugs"'s headroom below.
	seed := dispatch.NewFake()
	seed.RunResult = dispatch.Succeeded(dispatch.Result{
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "butler-seed", Status: outcome.StatusReady, Note: "seeded"},
		},
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"s1","body":"repro","dedupTerms":["a.go:S1"]}`,
			`{"title":"s2","body":"repro","dedupTerms":["a.go:S2"]}`,
			`{"title":"s3","body":"repro","dedupTerms":["a.go:S3"]}`,
			`{"title":"s4","body":"repro","dedupTerms":["a.go:S4"]}`,
		},
	})
	seedPolicy := testRunPolicy(noRunEvery, "seed", "bugs")
	seedRunner := New(backend, fakeTree{head: "headsha", files: []string{"a.go"}}, fc.AsIssueFiler(), func(c dispatch.Chore) dispatch.Dispatcher { return seed }, seedPolicy, func() time.Time { return now })
	seedOut, err := seedRunner.Sweep([]string{"seed"})
	if err != nil {
		t.Fatalf("seed Sweep: %v", err)
	}
	if seedOut.Kind != Swept || seedOut.Filed != 4 {
		t.Fatalf("seed Outcome = %+v, want Kind=Swept Filed=4", seedOut)
	}

	d := dispatch.NewFake()
	d.RunResult = dispatch.Succeeded(dispatch.Result{
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "butler-bugs", Status: outcome.StatusReady, Note: "swept"},
		},
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"bug one","body":"repro","dedupTerms":["a.go:One"]}`,
			`{"title":"bug two","body":"repro","dedupTerms":["a.go:Two"]}`,
			`{"title":"bug three","body":"repro","dedupTerms":["a.go:Three"]}`,
			`{"title":"bug four","body":"repro","dedupTerms":["a.go:Four"]}`,
			`{"title":"bug five","body":"repro","dedupTerms":["a.go:Five"]}`,
			`{"title":"bug six","body":"repro","dedupTerms":["a.go:Six"]}`,
			`{"title":"bug seven","body":"repro","dedupTerms":["a.go:Seven"]}`,
		},
	})
	policy := testRunPolicy(noRunEvery, "seed", "bugs")
	policy.Budgets = chore.Budgets{MaxFindingsPerDay: 10, MaxFindingsPerSweep: 0}
	r := New(backend, fakeTree{head: "headsha", files: []string{"a.go"}}, fc.AsIssueFiler(), func(c dispatch.Chore) dispatch.Dispatcher { return d }, policy, func() time.Time { return now })
	out, err := r.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != Swept || out.Filed != 6 || out.Dropped != 1 {
		t.Fatalf("Outcome = %+v, want Kind=Swept Filed=6 Dropped=1", out)
	}

	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if tip.State.Dropped != 1 {
		t.Errorf("Dropped = %d, want 1", tip.State.Dropped)
	}
	if len(tip.State.Filed) != 6 {
		t.Errorf("len(Filed) = %d, want 6", len(tip.State.Filed))
	}
}

// (m) BUTLER_MAX_PROMOTIONS_PER_DAY (Policy.Budgets.MaxPromotionsPerDay) defaults to
// 0, so wiring the class allow-list alone (Chore.PromotionClasses) never promotes
// anything -- a Consumer has to opt in to promotion itself, not just to a
// Chore (issue #3880).
func TestSweep_PromotionDisabledByDefaultFilesUnlabelled(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	tree := fakeTree{head: "headsha", files: []string{"a.go"}}

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9200"
	var gotChore dispatch.Chore
	newBox := func(c dispatch.Chore) dispatch.Dispatcher {
		gotChore = c
		return promotableDispatcher("error-handling")
	}

	policy := testRunPolicy(noRunEvery, "bugs")
	policy.Chores = withClasses(policy.Chores, "bugs", "error-handling")
	policy.Chores = withClassList(policy.Chores, "bugs", "error-handling", "resource-leak")
	policy.PromotionMaxFiles = 3
	// MaxPromotionsPerDay left at its zero value -- the default -- on purpose.

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := New(backend, tree, fc.AsIssueFiler(), newBox, policy, func() time.Time { return now })
	out, err := r.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != Swept || out.Promoted != 0 {
		t.Fatalf("Outcome = %+v, want Kind=Swept Promoted=0", out)
	}

	if len(fc.PostIssueCalls) != 1 || slices.Contains(fc.PostIssueCalls[0].Labels, "ready-for-agent") {
		t.Fatalf("PostIssueCalls = %+v, want one call with no ready-for-agent label", fc.PostIssueCalls)
	}
	// Promotion off (MaxPromotionsPerDay is 0): the Box must not be told a
	// class list even though the Chore has PromotionClasses, since nothing it could
	// hand back would ever promote (issue #3880).
	if len(gotChore.PromotionClasses) != 0 {
		t.Errorf("dispatch.Chore.PromotionClasses = %v, want none with promotion off", gotChore.PromotionClasses)
	}
	// The closed class list is not a promotion fact: it reaches the Box
	// even with promotion off.
	if want := []string{"error-handling", "resource-leak"}; !slices.Equal(gotChore.ClassList, want) {
		t.Errorf("dispatch.Chore.ClassList = %v, want %v", gotChore.ClassList, want)
	}
	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(tip.State.Promoted) != 0 {
		t.Errorf("Promoted = %v, want none", tip.State.Promoted)
	}
}

// (n) With MaxPromotionsPerDay > 0, an allow-listed finding that clears
// every other gate is filed carrying ready-for-agent, and the Ledger done
// commit records its URL in Promoted (issue #3880).
func TestSweep_PromotionEnabledPromotesAllowedFinding(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	tree := fakeTree{head: "headsha", files: []string{"a.go"}}

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9201"
	var gotChore dispatch.Chore
	newBox := func(c dispatch.Chore) dispatch.Dispatcher {
		gotChore = c
		return promotableDispatcher("error-handling")
	}

	policy := testRunPolicy(noRunEvery, "bugs")
	policy.Chores = withClasses(policy.Chores, "bugs", "error-handling")
	policy.PromotionMaxFiles = 3
	policy.Budgets.MaxPromotionsPerDay = 1

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := New(backend, tree, fc.AsIssueFiler(), newBox, policy, func() time.Time { return now })
	out, err := r.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != Swept || out.Promoted != 1 {
		t.Fatalf("Outcome = %+v, want Kind=Swept Promoted=1", out)
	}

	if len(fc.PostIssueCalls) != 1 || !slices.Contains(fc.PostIssueCalls[0].Labels, "ready-for-agent") {
		t.Fatalf("PostIssueCalls = %+v, want one call carrying ready-for-agent", fc.PostIssueCalls)
	}
	// Promotion on: the Box is told its own Chore's host allow-list, so it
	// knows which findings are worth a reviewer's turn (issue #3880).
	if want := []string{"error-handling"}; !slices.Equal(gotChore.PromotionClasses, want) {
		t.Errorf("dispatch.Chore.PromotionClasses = %v, want %v", gotChore.PromotionClasses, want)
	}
	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !slices.Equal(tip.State.Promoted, []string{fc.PostIssueURL}) {
		t.Errorf("Promoted = %v, want [%s]", tip.State.Promoted, fc.PostIssueURL)
	}
}

// (n2) A Consumer-configured non-default work label (not ready-for-agent) is
// the label a promoted finding actually carries end to end through Sweep's
// run step (issue #3880).
func TestSweep_PromotionUsesConfiguredWorkLabel(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	tree := fakeTree{head: "headsha", files: []string{"a.go"}}

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9204"
	newBox := func(c dispatch.Chore) dispatch.Dispatcher { return promotableDispatcher("error-handling") }

	policy := testRunPolicy(noRunEvery, "bugs")
	policy.Chores = withClasses(policy.Chores, "bugs", "error-handling")
	policy.PromotionMaxFiles = 3
	policy.Budgets.MaxPromotionsPerDay = 1
	policy.PromotionLabel = "agent-go"

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := New(backend, tree, fc.AsIssueFiler(), newBox, policy, func() time.Time { return now })
	if _, err := r.Sweep([]string{"bugs"}); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("PostIssueCalls = %+v, want 1", fc.PostIssueCalls)
	}
	got := fc.PostIssueCalls[0].Labels
	if !slices.Contains(got, "agent-go") || slices.Contains(got, "ready-for-agent") {
		t.Errorf("labels = %v, want agent-go and not the ready-for-agent default", got)
	}
}

// (o) A day whose Ledger already holds MaxPromotionsPerDay promotions leaves
// no room: the next otherwise-eligible finding still files, just unlabelled
// (issue #3880). The earlier promotion is seeded before this Sweep call, so
// Sweep's own single Room walk at its own start (issue #3994) already sees
// it spent -- no second walk at settle time is needed to catch it.
func TestSweep_PromotionBudgetSpentFilesUnlabelled(t *testing.T) {
	const head1, head2 = "head1", "head2"
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	// The current tree head (head2) differs from the seeded LastSwept
	// (head1), so NothingToScan never blocks this run's due check.
	tree := fakeTree{head: head2, files: []string{"a.go", "newfile.go"}}

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	claim, err := ledger.Claim(backend, "bugs", ledger.Tip{}, ledger.ClaimedBy{Host: "seed-host", Start: now})
	if err != nil {
		t.Fatalf("seed Claim: %v", err)
	}
	if _, err := ledger.Finish(backend, "bugs", claim, ledger.State{LastSwept: head1, Promoted: []string{"https://example.com/issues/seed"}}, now); err != nil {
		t.Fatalf("seed Finish: %v", err)
	}

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9202"
	var gotChore dispatch.Chore
	newBox := func(c dispatch.Chore) dispatch.Dispatcher {
		gotChore = c
		return promotableDispatcher("error-handling")
	}

	policy := testRunPolicy(noRunEvery, "bugs")
	policy.Chores = withClasses(policy.Chores, "bugs", "error-handling")
	policy.PromotionMaxFiles = 3
	policy.Budgets.MaxPromotionsPerDay = 1

	r := New(backend, tree, fc.AsIssueFiler(), newBox, policy, func() time.Time { return now })
	out, err := r.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != Swept || out.Promoted != 0 {
		t.Fatalf("Outcome = %+v, want Kind=Swept Promoted=0", out)
	}

	if len(fc.PostIssueCalls) != 1 || slices.Contains(fc.PostIssueCalls[0].Labels, "ready-for-agent") {
		t.Fatalf("PostIssueCalls = %+v, want one call with no ready-for-agent label (budget spent)", fc.PostIssueCalls)
	}
	// Room already spent (today.Promoted >= MaxPromotionsPerDay): the Box
	// must not be told a class list even though the Chore has PromotionClasses and
	// promotion is on, mirroring the promotion-off case above (issue #3994
	// review finding, butler.go's room.Promotions > 0 gate).
	if len(gotChore.PromotionClasses) != 0 {
		t.Errorf("dispatch.Chore.PromotionClasses = %v, want none with today's promotion room spent", gotChore.PromotionClasses)
	}
	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(tip.State.Promoted) != 0 {
		t.Errorf("Promoted = %v, want none (this run promoted nothing)", tip.State.Promoted)
	}
}

// (p) A class allow-listed only for a different Chore never promotes: the
// allow-list is per Chore, so a Box relaying a class that is only on another
// Chore's entry stays unlabelled (issue #3880).
func TestSweep_PromotionClassNotAllowlistedForChoreFilesUnlabelled(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	tree := fakeTree{head: "headsha", files: []string{"a.go"}}

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9203"
	newBox := func(c dispatch.Chore) dispatch.Dispatcher { return promotableDispatcher("error-handling") }

	policy := testRunPolicy(noRunEvery, "bugs", "refactor")
	// error-handling is allow-listed for "refactor", not "bugs" -- the Chore
	// this run actually sweeps.
	policy.Chores = withClasses(policy.Chores, "refactor", "error-handling")
	policy.PromotionMaxFiles = 3
	policy.Budgets.MaxPromotionsPerDay = 1

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := New(backend, tree, fc.AsIssueFiler(), newBox, policy, func() time.Time { return now })
	out, err := r.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != Swept || out.Promoted != 0 {
		t.Fatalf("Outcome = %+v, want Kind=Swept Promoted=0", out)
	}

	if len(fc.PostIssueCalls) != 1 || slices.Contains(fc.PostIssueCalls[0].Labels, "ready-for-agent") {
		t.Fatalf("PostIssueCalls = %+v, want one call with no ready-for-agent label", fc.PostIssueCalls)
	}
	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(tip.State.Promoted) != 0 {
		t.Errorf("Promoted = %v, want none", tip.State.Promoted)
	}
}

// (p) BUTLER_MAX_PATCHES_PER_DAY (Policy.Budgets.MaxPatchesPerDay) defaults
// to 0, the patch rung off: the Box must not be told a patch class list
// even though the Chore has PatchClasses, mirroring
// TestSweep_PromotionDisabledByDefaultFilesUnlabelled for the patch rung
// (issue #4072, ADR 0057).
func TestSweep_PatchRungOffByDefaultOmitsPatchClasses(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	tree := fakeTree{head: "headsha", files: []string{"a.go"}}

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9300"
	var gotChore dispatch.Chore
	newBox := func(c dispatch.Chore) dispatch.Dispatcher {
		gotChore = c
		return promotableDispatcher("docs-drift")
	}

	policy := testRunPolicy(noRunEvery, "bugs")
	policy.Chores = withPatchClasses(policy.Chores, "bugs", "docs-drift")
	// Budgets.MaxPatchesPerDay left at its zero value -- the default -- on purpose.

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := New(backend, tree, fc.AsIssueFiler(), newBox, policy, func() time.Time { return now })
	if _, err := r.Sweep([]string{"bugs"}); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(gotChore.PatchClasses) != 0 {
		t.Errorf("dispatch.Chore.PatchClasses = %v, want none with the patch rung off", gotChore.PatchClasses)
	}
}

// (q) With MaxPatchesPerDay > 0, a configured patchForge, today's patch room
// untouched, and promotion also enabled with room, the Box is told its own
// Chore's host-side patch allow-list, the patch rung's sibling of
// TestSweep_PromotionEnabledPromotesAllowedFinding (issue #4072, ADR 0057).
// This slice only forwards the fact -- landing a patch itself is out of
// scope here. WithPatchForge matters to this test now (issue #4074): a nil
// patchForge must withhold PatchClasses regardless of budget, per
// TestSweep_NoPatchForgeOmitsPatchClassesFromBox.
func TestSweep_PatchRungOnWithRoomForwardsPatchClasses(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	tree := fakeTree{head: "headsha", files: []string{"a.go"}}

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9301"
	var gotChore dispatch.Chore
	newBox := func(c dispatch.Chore) dispatch.Dispatcher {
		gotChore = c
		return promotableDispatcher("docs-drift")
	}

	policy := testRunPolicy(noRunEvery, "bugs")
	policy.Chores = withClasses(policy.Chores, "bugs", "docs-drift")
	policy.Chores = withPatchClasses(policy.Chores, "bugs", "docs-drift")
	policy.PromotionMaxFiles = 3
	policy.Budgets.MaxPromotionsPerDay = 1
	policy.Budgets.MaxPatchesPerDay = 1

	pf := &fakePatchForge{prefix: "agent/issue-", IssueLabeler: fc}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := New(backend, tree, fc.AsIssueFiler(), newBox, policy, func() time.Time { return now }).WithPatchForge(pf, &fakePatchGate{})
	if _, err := r.Sweep([]string{"bugs"}); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if want := []string{"docs-drift"}; !slices.Equal(gotChore.PatchClasses, want) {
		t.Errorf("dispatch.Chore.PatchClasses = %v, want %v", gotChore.PatchClasses, want)
	}
}

// (q2) With MaxPatchesPerDay > 0 but promotion off (the default), the Box
// must not be told a patch class list even though the Chore has
// PatchClasses: the relay fragment only ever honours CHORE_PATCH_CLASSES
// for a class also present on CHORE_CLASSES, and with promotion off that
// list is always empty (review finding on issue #4072, ADR 0057).
func TestSweep_PatchRungOnPromotionOffOmitsPatchClasses(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	tree := fakeTree{head: "headsha", files: []string{"a.go"}}

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9302"
	var gotChore dispatch.Chore
	newBox := func(c dispatch.Chore) dispatch.Dispatcher {
		gotChore = c
		return promotableDispatcher("docs-drift")
	}

	policy := testRunPolicy(noRunEvery, "bugs")
	policy.Chores = withPatchClasses(policy.Chores, "bugs", "docs-drift")
	policy.Budgets.MaxPatchesPerDay = 1
	// MaxPromotionsPerDay left at its zero value -- promotion off -- on purpose.

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := New(backend, tree, fc.AsIssueFiler(), newBox, policy, func() time.Time { return now })
	if _, err := r.Sweep([]string{"bugs"}); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(gotChore.PatchClasses) != 0 {
		t.Errorf("dispatch.Chore.PatchClasses = %v, want none with promotion off", gotChore.PatchClasses)
	}
}

// countingHistoryBackend wraps a ledger.Backend and counts History calls
// whose since is exactly midnight -- the shape only a day's totals walk
// (ledger.DayTotalsAll, via chore.Budgets.Room) makes; Sweep's own due-check
// History call for a candidate's recent claims uses since =
// whenNow.Add(-c.Every) instead, so the two are never confused.
type countingHistoryBackend struct {
	ledger.Backend
	midnight    time.Time
	totalsWalks int
}

func (w *countingHistoryBackend) History(choreName string, since time.Time) ([]ledger.Entry, error) {
	if since.Equal(w.midnight) {
		w.totalsWalks++
	}
	return w.Backend.History(choreName, since)
}

// (q) One full Sweep -- claim, run a Box whose finding is eligible to
// promote, settle -- with promotion enabled walks the day's totals exactly
// once: Sweep's own Room computation up front, never a second walk inside
// settle (issue #3994; before this the promotion Room closure re-walked the
// Ledger fresh at settle time on top of Sweep's own walk, costing two).
func TestSweep_PromotionEnabledWalksDayTotalsOnce(t *testing.T) {
	midnight := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	backend := &countingHistoryBackend{Backend: ledger.Local{Repo: ledgertest.NewRepo(t)}, midnight: midnight}

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9300"
	newBox := func(c dispatch.Chore) dispatch.Dispatcher { return promotableDispatcher("error-handling") }

	policy := testRunPolicy(noRunEvery, "bugs")
	policy.Chores = withClasses(policy.Chores, "bugs", "error-handling")
	policy.PromotionMaxFiles = 3
	policy.Budgets.MaxPromotionsPerDay = 1

	now := midnight.Add(time.Hour)
	r := New(backend, fakeTree{head: "headsha", files: []string{"a.go"}}, fc.AsIssueFiler(), newBox, policy, func() time.Time { return now })
	out, err := r.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != Swept || out.Promoted != 1 {
		t.Fatalf("Outcome = %+v, want Kind=Swept Promoted=1", out)
	}
	if backend.totalsWalks != 1 {
		t.Errorf("totalsWalks = %d, want exactly 1 (Sweep's own Room, no second walk in settle)", backend.totalsWalks)
	}
}

// (q2) With two enabled Chores, the day's totals walk costs one History
// call per Chore (DayTotalsAll folds one DayTotals per name) -- 2, never 4.
// A single-Chore fixture can't tell "one walk, N Chores" apart from "N
// Chores walked twice" since both read back totalsWalks == N in that case;
// this pins the count where the two diverge (issue #3994 review finding).
func TestSweep_PromotionEnabledWalksDayTotalsOncePerChore(t *testing.T) {
	midnight := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	backend := &countingHistoryBackend{Backend: ledger.Local{Repo: ledgertest.NewRepo(t)}, midnight: midnight}

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9301"
	newBox := func(c dispatch.Chore) dispatch.Dispatcher { return promotableDispatcher("error-handling") }

	policy := testRunPolicy(noRunEvery, "bugs", "docs-drift")
	policy.Chores = withClasses(policy.Chores, "bugs", "error-handling")
	policy.PromotionMaxFiles = 3
	policy.Budgets.MaxPromotionsPerDay = 1

	now := midnight.Add(time.Hour)
	r := New(backend, fakeTree{head: "headsha", files: []string{"a.go"}}, fc.AsIssueFiler(), newBox, policy, func() time.Time { return now })
	out, err := r.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != Swept || out.Promoted != 1 {
		t.Fatalf("Outcome = %+v, want Kind=Swept Promoted=1", out)
	}
	if backend.totalsWalks != 2 {
		t.Errorf("totalsWalks = %d, want exactly 2 (one per enabled Chore, one Sweep -- not 4, two walks)", backend.totalsWalks)
	}
}

// TestSweep_RemoteBackendEndToEndAgainstHostedForgeShape drives a full Sweep
// against a Remote Ledger backend built the same way a hosted-forge backend
// row (github, forgejo) wires one -- ledger.NewRemote's scratch repo, then
// FetchTree fetching the same scratch repo forward to the base branch, so
// one checkout serves both the Tree and the Ledger (issue #3876's stand-in
// for "works on github/forgejo"), migrated from cmd/launcher's retired
// TestRunButler_AgainstRemoteLedger (issue #3990): the assertion is about
// ledger.Remote's own push/fetch behavior under a real Sweep, not about
// cmd/launcher's backend-row wiring (remoteLedger itself is a thin
// convenience over these same two calls), so it belongs here rather than at
// the verb level. It asserts the remote's own refs/spindrift/butler/<chore>
// ref -- not just the scratch repo's -- picked up the done commit, and that
// refs/heads/main on the remote never moved. Kept on a real ledgertest.Repo
// and a real FetchTree (unlike every other Sweep test above), since it's the
// one case actually exercising the Ledger/Tree wiring over real git, not
// just Runner's own logic (issue #3995).
func TestSweep_RemoteBackendEndToEndAgainstHostedForgeShape(t *testing.T) {
	remoteRepo := ledgertest.NewRepo(t)
	head := strings.TrimSpace(string(runRepoGitOutput(t, remoteRepo, "rev-parse", "refs/heads/main")))

	scratch := t.TempDir()
	backend, err := ledger.NewRemote(scratch, remoteRepo)
	if err != nil {
		t.Fatalf("NewRemote: %v", err)
	}
	tree, err := FetchTree(scratch, remoteRepo, "main")
	if err != nil {
		t.Fatalf("FetchTree: %v", err)
	}

	newBox := func(c dispatch.Chore) dispatch.Dispatcher { return readyDispatcher() }

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := New(backend, tree, forge.NewFake().AsIssueFiler(), newBox, testRunPolicy(noRunEvery, "bugs"), func() time.Time { return now })
	out, err := r.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != Swept {
		t.Fatalf("Kind = %v, want Swept: %+v", out.Kind, out)
	}

	subjects := gitLogSubjects(t, remoteRepo, ledger.RefPrefix+"bugs")
	if len(subjects) != 2 || subjects[0] != "bugs: done" || subjects[1] != "bugs: claimed" {
		t.Fatalf("remote git log subjects = %v, want [bugs: done, bugs: claimed]", subjects)
	}

	remoteHead := strings.TrimSpace(string(runRepoGitOutput(t, remoteRepo, "rev-parse", "refs/heads/main")))
	if remoteHead != head {
		t.Errorf("remote refs/heads/main moved to %q, want unchanged %q", remoteHead, head)
	}
}

// testPatchDiff is a minimal unified diff that clears signalwire's
// ValidateUnifiedDiff and patchTestPolicy's own patchPaths (docs/a.md, under
// the default BUTLER_PATCH_PATHS) -- what every single-finding patch-rung
// test below embeds as an issue-intent's "patch" field (its own contents are
// never actually applied: fakeTree.CommitPatch answers a scripted
// PatchCommit/error, not a real git apply).
const testPatchDiff = "--- a/docs/a.md\n+++ b/docs/a.md\n@@ -1 +1 @@\n-old\n+new\n"

// patchDiffFor is testPatchDiff's per-path sibling (issue #4075): decide's
// site-key gate requires a patch's diff path to appear among the finding's
// own dedup terms, so patchableDispatcherN can no longer hand every finding
// the same fixed diff -- each needs its own path, matching its own dedup
// term.
func patchDiffFor(path string) string {
	return fmt.Sprintf("--- a/%s\n+++ b/%s\n@@ -1 +1 @@\n-old\n+new\n", path, path)
}

// patchableDispatcher is promotableDispatcher's patch-rung sibling: its one
// relayed finding also carries a Patch, so it clears decide's patch gate
// (class on the patch allow-list, concurrence, patch room) whenever the host
// policy allows it.
func patchableDispatcher(class string) *dispatch.Fake {
	d := dispatch.NewFake()
	d.RunResult = dispatch.Succeeded(dispatch.Result{
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "butler-bugs", Status: outcome.StatusReady, Note: "swept"},
		},
		IssueIntentsFound: true,
		IssueIntents: []string{
			fmt.Sprintf(`{"title":"fix typo","body":"repro","dedupTerms":["docs/a.md:Foo"],"class":%q,"concurrence":"agreed","patch":%q}`, class, testPatchDiff),
		},
	})
	return d
}

// patchableDispatcherN is patchableDispatcher's multi-finding sibling: n
// findings, same class, each with its own title/dedup term/patch (its own
// docs/aN.md path, so decide's site-key gate holds for every one of them) so
// a test can pit them against one sweep's shared patch room (issue #4074).
func patchableDispatcherN(class string, n int) *dispatch.Fake {
	d := dispatch.NewFake()
	intents := make([]string, n)
	for i := range intents {
		path := fmt.Sprintf("docs/a%d.md", i)
		intents[i] = fmt.Sprintf(`{"title":"fix typo %d","body":"repro","dedupTerms":[%q],"class":%q,"concurrence":"agreed","patch":%q}`, i, path+":Foo", class, patchDiffFor(path))
	}
	d.RunResult = dispatch.Succeeded(dispatch.Result{
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "butler-bugs", Status: outcome.StatusReady, Note: "swept"},
		},
		IssueIntentsFound: true,
		IssueIntents:      intents,
	})
	return d
}

// fakePushCall/fakeDraftCall record one fakePatchForge.PushBranch/CreateDraftPR
// invocation.
type fakePushCall struct{ srcDir, localRef, branch, base string }
type fakeDraftCall struct{ title, body, base, head string }

// fakeDeleteCall records one DeleteBranch invocation.
type fakeDeleteCall struct{ branch, base string }

// fakePatchForge is PatchForge's test double: AgentBranch is prefix+num,
// PushBranch/CreateDraftPR each record their call and answer a scripted
// error, or -- on CreateDraftPR -- the scripted draftURL. AddLabels is
// promoted straight through from the embedded forge.IssueLabeler (issue
// #4074), normally the same fc.AsIssueFiler() fake the test's own
// IssueTracker is, so a fallback assertion against fc.AddLabelsCalls sees
// the call whichever path it came through. openPR/openPRFound/openPRErr and
// deleteCalls/deleteErr back landPatch's ambiguous-CreateDraftPR-error
// recovery (issue #4112): a lookup told apart from a real create failure,
// and the branch cleanup that follows only when no PR was found.
type fakePatchForge struct {
	prefix string
	forge.IssueLabeler

	pushCalls []fakePushCall
	pushErr   error

	draftCalls []fakeDraftCall
	draftURL   string
	draftErr   error

	openPRCalls []string
	openPR      forge.PR
	openPRFound bool
	openPRErr   error

	deleteCalls []fakeDeleteCall
	deleteErr   error
}

func (f *fakePatchForge) AgentBranch(num string) string { return f.prefix + num }

func (f *fakePatchForge) PushBranch(srcDir, localRef, branch, base string) error {
	f.pushCalls = append(f.pushCalls, fakePushCall{srcDir, localRef, branch, base})
	return f.pushErr
}

func (f *fakePatchForge) CreateDraftPR(title, body, base, head string) (string, bool, error) {
	f.draftCalls = append(f.draftCalls, fakeDraftCall{title, body, base, head})
	if f.draftErr != nil {
		return "", false, f.draftErr
	}
	return f.draftURL, true, nil
}

func (f *fakePatchForge) OpenPRForBranch(branch string) (forge.PR, bool, error) {
	f.openPRCalls = append(f.openPRCalls, branch)
	if f.openPRErr != nil {
		return forge.PR{}, false, f.openPRErr
	}
	return f.openPR, f.openPRFound, nil
}

func (f *fakePatchForge) DeleteBranch(branch, base string) error {
	f.deleteCalls = append(f.deleteCalls, fakeDeleteCall{branch, base})
	return f.deleteErr
}

// fakePatchGateCall records one PatchGate.SettleAdopted invocation.
type fakePatchGateCall struct {
	d     dispatch.Dispatcher
	num   string
	gen   uint64
	prURL string
}

// fakePatchGate is a no-op recording PatchGate (issue #4076): every existing
// patch-rung Sweep test that doesn't itself exercise the merge gate passes
// one of these so WithPatchForge never sees a nil gate. onCall, when set,
// runs before the call is recorded -- TestSweep_PatchGateCalledAfterDoneCommit
// uses it to read the Ledger from inside the call.
type fakePatchGate struct {
	calls  []fakePatchGateCall
	onCall func(num, prURL string)
}

func (g *fakePatchGate) SettleAdopted(d dispatch.Dispatcher, num string, gen uint64, prURL string) {
	if g.onCall != nil {
		g.onCall(num, prURL)
	}
	g.calls = append(g.calls, fakePatchGateCall{d: d, num: num, gen: gen, prURL: prURL})
}

// patchTestPolicy builds a Policy with both the promotion and patch rungs on
// for the "docs-drift" class on "bugs" -- the shape every patch-rung Sweep
// test below starts from, tuning only what its own case cares about.
func patchTestPolicy() Policy {
	policy := testRunPolicy(noRunEvery, "bugs")
	policy.Chores = withClasses(policy.Chores, "bugs", "docs-drift")
	policy.Chores = withPatchClasses(policy.Chores, "bugs", "docs-drift")
	policy.PromotionMaxFiles = 3
	policy.Budgets.MaxPromotionsPerDay = 1
	policy.Budgets.MaxPatchesPerDay = 1
	policy.PatchPaths = DefaultPatchPaths
	policy.PatchMaxFiles = 3
	policy.PatchMaxLines = 20
	return policy
}

// TestSweep_NoPatchForgeOmitsPatchClassesFromBox pins that room.Patches
// derives from the Consumer's raw BUTLER_MAX_PATCHES_PER_DAY budget alone
// (chore.Room), so it stays positive even with no WithPatchForge -- run()
// must still withhold PatchClasses from the Box, or a non-write-capable
// host would tell the Box to spend its diff budget on patch classes the
// host can never land (issue #4074).
func TestSweep_NoPatchForgeOmitsPatchClassesFromBox(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	tree := fakeTree{head: "headsha", files: []string{"a.go"}}

	var got dispatch.Chore
	newBox := func(c dispatch.Chore) dispatch.Dispatcher {
		got = c
		return readyDispatcher()
	}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// No .WithPatchForge(...): r.patchForge stays nil.
	r := New(backend, tree, forge.NewFake().AsIssueFiler(), newBox, patchTestPolicy(), func() time.Time { return now })
	if _, err := r.Sweep([]string{"bugs"}); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(got.PatchClasses) != 0 {
		t.Errorf("PatchClasses = %v, want none: a nil patchForge must keep the rung off for the Box too", got.PatchClasses)
	}
	if len(got.PromotionClasses) == 0 {
		t.Fatalf("PromotionClasses = %v, want the promotion class list still on", got.PromotionClasses)
	}
}

// (r) A patch-eligible finding lands as a draft PR (ADR 0057, issue #4074):
// filed with agent-butler-finding + agent-butler-patch and no dispatch
// label, pushed to the Consumer's own AgentBranch, and opened as a draft PR
// onto the policy branch whose body closes the finding issue and quotes the
// reviewer's concurrence. The Ledger done commit records the PR URL in
// Patched, not Promoted.
func TestSweep_PatchedFindingLandsDraftPR(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	tree := fakeTree{head: "headsha", files: []string{"a.go"}, commitPatch: PatchCommit{Dir: "/repo", Ref: "refs/butler/patch"}}

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9400"
	fc.SetIssue(forge.Issue{Number: "9400"})

	pf := &fakePatchForge{prefix: "agent/issue-", IssueLabeler: fc, draftURL: "https://example.com/pull/1"}
	newBox := func(c dispatch.Chore) dispatch.Dispatcher { return patchableDispatcher("docs-drift") }

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := New(backend, tree, fc.AsIssueFiler(), newBox, patchTestPolicy(), func() time.Time { return now }).WithPatchForge(pf, &fakePatchGate{})
	out, err := r.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != Swept || out.Filed != 1 || out.Promoted != 0 || out.Patched != 1 {
		t.Fatalf("Outcome = %+v, want Kind=Swept Filed=1 Promoted=0 Patched=1", out)
	}

	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("PostIssueCalls = %+v, want 1", fc.PostIssueCalls)
	}
	labels := fc.PostIssueCalls[0].Labels
	if !slices.Contains(labels, "agent-butler-finding") || !slices.Contains(labels, "agent-butler-patch") {
		t.Fatalf("PostIssue labels = %v, want agent-butler-finding and agent-butler-patch", labels)
	}
	if slices.Contains(labels, "ready-for-agent") {
		t.Errorf("PostIssue labels = %v, want no dispatch label", labels)
	}

	wantBranch := pf.prefix + "9400"
	if len(pf.pushCalls) != 1 || pf.pushCalls[0].branch != wantBranch || pf.pushCalls[0].base != "main" {
		t.Fatalf("pushCalls = %+v, want exactly one push to %q with base %q", pf.pushCalls, wantBranch, "main")
	}
	if len(pf.draftCalls) != 1 {
		t.Fatalf("draftCalls = %+v, want 1", pf.draftCalls)
	}
	dc := pf.draftCalls[0]
	if dc.base != "main" || dc.head != wantBranch {
		t.Errorf("draft base/head = %q/%q, want main/%q", dc.base, dc.head, wantBranch)
	}
	if !strings.Contains(dc.body, "Closes #9400") {
		t.Errorf("draft body = %q, want Closes #9400", dc.body)
	}
	if !strings.Contains(dc.body, "agreed") {
		t.Errorf("draft body = %q, want the reviewer's concurrence quoted", dc.body)
	}
	if !strings.Contains(dc.body, "**Patched**") {
		t.Errorf("draft body = %q, want the patched note", dc.body)
	}
	if !strings.Contains(dc.body, fc.PostIssueURL) {
		t.Errorf("draft body = %q, want the filed finding's URL %q", dc.body, fc.PostIssueURL)
	}

	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !slices.Equal(tip.State.Patched, []string{pf.draftURL}) {
		t.Errorf("Patched = %v, want [%s]", tip.State.Patched, pf.draftURL)
	}
	if len(tip.State.Promoted) != 0 {
		t.Errorf("Promoted = %v, want none", tip.State.Promoted)
	}
}

// (r2) A diff that no longer applies to the current base head (CommitPatch
// fails) falls back to exactly what decide would return with no Patch at
// all: promoted here, since promotion is on and otherwise eligible. No push
// or PR is attempted.
func TestSweep_PatchDiffNoLongerAppliesFallsBackToPromote(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	tree := fakeTree{head: "headsha", files: []string{"a.go"}, commitPatchErr: errors.New("does not apply")}

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9401"

	pf := &fakePatchForge{prefix: "agent/issue-", IssueLabeler: fc, draftURL: "https://example.com/pull/2"}
	newBox := func(c dispatch.Chore) dispatch.Dispatcher { return patchableDispatcher("docs-drift") }

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := New(backend, tree, fc.AsIssueFiler(), newBox, patchTestPolicy(), func() time.Time { return now }).WithPatchForge(pf, &fakePatchGate{})
	var out Outcome
	var err error
	stdout := captureStdout(t, func() {
		out, err = r.Sweep([]string{"bugs"})
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != Swept || out.Patched != 0 || out.Promoted != 1 {
		t.Fatalf("Outcome = %+v, want Kind=Swept Patched=0 Promoted=1", out)
	}
	if len(pf.pushCalls) != 0 || len(pf.draftCalls) != 0 {
		t.Errorf("pushCalls = %v draftCalls = %v, want none: the patch was never committed", pf.pushCalls, pf.draftCalls)
	}

	labels := fc.PostIssueCalls[0].Labels
	if !slices.Contains(labels, "ready-for-agent") || slices.Contains(labels, "agent-butler-patch") {
		t.Fatalf("PostIssue labels = %v, want ready-for-agent and no agent-butler-patch", labels)
	}

	// status=patch-apply-failed (not patch-skipped, which names only the
	// decide-time gate refusal above) so an operator grepping the token can
	// tell a CommitPatch failure from a gate refusal.
	if !strings.Contains(stdout, "status=patch-apply-failed") || !strings.Contains(stdout, "does not apply") {
		t.Errorf("settle log = %q, want a status=patch-apply-failed line naming the CommitPatch error", stdout)
	}
	if strings.Contains(stdout, "status=patch-skipped") {
		t.Errorf("settle log = %q, want no status=patch-skipped line: no gate refused this finding", stdout)
	}

	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(tip.State.Patched) != 0 {
		t.Errorf("Patched = %v, want none", tip.State.Patched)
	}
	if !slices.Equal(tip.State.Promoted, []string{fc.PostIssueURL}) {
		t.Errorf("Promoted = %v, want [%s]", tip.State.Promoted, fc.PostIssueURL)
	}
}

// (r3) The same stale-diff fallback with promotion off: the finding still
// files, just plain -- no dispatch label, no patch label.
func TestSweep_PatchDiffNoLongerAppliesFilesPlainWithPromotionOff(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	tree := fakeTree{head: "headsha", files: []string{"a.go"}, commitPatchErr: errors.New("does not apply")}

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9402"

	pf := &fakePatchForge{prefix: "agent/issue-", IssueLabeler: fc}
	newBox := func(c dispatch.Chore) dispatch.Dispatcher { return patchableDispatcher("docs-drift") }

	policy := patchTestPolicy()
	policy.Budgets.MaxPromotionsPerDay = 0 // promotion off; the patch rung stays on

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := New(backend, tree, fc.AsIssueFiler(), newBox, policy, func() time.Time { return now }).WithPatchForge(pf, &fakePatchGate{})
	out, err := r.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != Swept || out.Patched != 0 || out.Promoted != 0 {
		t.Fatalf("Outcome = %+v, want Kind=Swept Patched=0 Promoted=0", out)
	}
	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("PostIssueCalls = %+v, want 1", fc.PostIssueCalls)
	}
	labels := fc.PostIssueCalls[0].Labels
	if slices.Contains(labels, "ready-for-agent") || slices.Contains(labels, "agent-butler-patch") {
		t.Fatalf("PostIssue labels = %v, want neither ready-for-agent nor agent-butler-patch", labels)
	}

	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(tip.State.Patched) != 0 {
		t.Errorf("Patched = %v, want none", tip.State.Patched)
	}
}

// (r4) A patch that commits cleanly but fails to land -- CreateDraftPR
// errors after the push succeeded, and a lookup finds no PR was created
// anyway -- falls back to promoting the already-filed issue via
// IssueLabeler.AddLabels, since PostIssue already ran and cannot be redone
// with different labels. The finding keeps its agent-butler-patch
// provenance label; ready-for-agent is added on top. The orphaned branch the
// failed push left behind is deleted (issue #4112).
func TestSweep_PatchPRCreateFailsFallsBackToPromoteViaAddLabels(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	tree := fakeTree{head: "headsha", files: []string{"a.go"}, commitPatch: PatchCommit{Dir: "/repo", Ref: "refs/butler/patch"}}

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9403"
	fc.SetIssue(forge.Issue{Number: "9403"})

	pf := &fakePatchForge{prefix: "agent/issue-", IssueLabeler: fc, draftErr: errors.New("boom")}
	newBox := func(c dispatch.Chore) dispatch.Dispatcher { return patchableDispatcher("docs-drift") }

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := New(backend, tree, fc.AsIssueFiler(), newBox, patchTestPolicy(), func() time.Time { return now }).WithPatchForge(pf, &fakePatchGate{})
	out, err := r.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != Swept || out.Patched != 0 || out.Promoted != 1 {
		t.Fatalf("Outcome = %+v, want Kind=Swept Patched=0 Promoted=1", out)
	}

	if len(fc.AddLabelsCalls) != 1 || fc.AddLabelsCalls[0].Num != "9403" || !slices.Contains(fc.AddLabelsCalls[0].Labels, "ready-for-agent") {
		t.Fatalf("AddLabelsCalls = %+v, want one call adding ready-for-agent to 9403", fc.AddLabelsCalls)
	}
	labels := fc.PostIssueCalls[0].Labels
	if !slices.Contains(labels, "agent-butler-patch") {
		t.Errorf("PostIssue labels = %v, want agent-butler-patch (filed as a patch candidate before the PR failed)", labels)
	}

	wantBranch := pf.prefix + "9403"
	if len(pf.openPRCalls) != 1 || pf.openPRCalls[0] != wantBranch {
		t.Fatalf("openPRCalls = %v, want exactly one lookup of %q", pf.openPRCalls, wantBranch)
	}
	if len(pf.deleteCalls) != 1 || pf.deleteCalls[0] != (fakeDeleteCall{branch: wantBranch, base: "main"}) {
		t.Fatalf("deleteCalls = %+v, want exactly one delete of %q with base %q", pf.deleteCalls, wantBranch, "main")
	}

	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(tip.State.Patched) != 0 {
		t.Errorf("Patched = %v, want none", tip.State.Patched)
	}
	if !slices.Equal(tip.State.Promoted, []string{fc.PostIssueURL}) {
		t.Errorf("Promoted = %v, want [%s]", tip.State.Promoted, fc.PostIssueURL)
	}
	if tip.State.Phase != ledger.Done {
		t.Errorf("Phase = %q, want %q (the fallback still writes the done commit)", tip.State.Phase, ledger.Done)
	}
}

// (r5) Per-sweep patch budget (issue #4074): two patch-eligible findings
// compete for one sweep's single patch slot. The first spends it; decide's
// own patch gate then sees zero Patches left for the second and falls
// straight through to decidePromote, so it promotes instead of patching.
func TestSweep_PatchBudgetSpendsOncePerSweep(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	tree := fakeTree{head: "headsha", files: []string{"a.go"}, commitPatch: PatchCommit{Dir: "/repo", Ref: "refs/butler/patch"}}

	fc := forge.NewFake()
	// Distinct per-finding URLs (issue #4074): a shared PostIssueURL could
	// not tell the Ledger's Patched entry apart from its Promoted one, since
	// both findings would then file to the same URL.
	fc.PostIssueURLForTitle = map[string]string{
		"fix typo 0": "https://example.com/issues/9500",
		"fix typo 1": "https://example.com/issues/9501",
	}
	fc.SetIssue(forge.Issue{Number: "9500"})
	fc.SetIssue(forge.Issue{Number: "9501"})

	pf := &fakePatchForge{prefix: "agent/issue-", IssueLabeler: fc, draftURL: "https://example.com/pull/9"}
	newBox := func(c dispatch.Chore) dispatch.Dispatcher { return patchableDispatcherN("docs-drift", 2) }

	policy := patchTestPolicy()
	policy.Budgets.MaxPromotionsPerDay = 2 // headroom for the second finding to promote instead

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := New(backend, tree, fc.AsIssueFiler(), newBox, policy, func() time.Time { return now }).WithPatchForge(pf, &fakePatchGate{})
	out, err := r.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != Swept || out.Patched != 1 || out.Promoted != 1 {
		t.Fatalf("Outcome = %+v, want Kind=Swept Patched=1 Promoted=1", out)
	}
	if len(pf.pushCalls) != 1 || len(pf.draftCalls) != 1 {
		t.Fatalf("pushCalls = %+v draftCalls = %+v, want exactly one of each", pf.pushCalls, pf.draftCalls)
	}

	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !slices.Equal(tip.State.Patched, []string{pf.draftURL}) {
		t.Errorf("Patched = %v, want [%s]", tip.State.Patched, pf.draftURL)
	}
	// The first finding ("fix typo 0") spent the patch slot; the second
	// ("fix typo 1") is the one promoted once patch room ran out.
	if !slices.Equal(tip.State.Promoted, []string{"https://example.com/issues/9501"}) {
		t.Errorf("Promoted = %v, want [https://example.com/issues/9501] (the second finding)", tip.State.Promoted)
	}
}

// (r6) A push failure (the branch never reaches the forge) is landPatch's
// other error path, distinct from a failed CreateDraftPR: no draft PR is
// ever attempted, and the finding falls back to promoting the already-filed
// issue.
func TestSweep_PatchPushFailsFallsBackToPromote(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	tree := fakeTree{head: "headsha", files: []string{"a.go"}, commitPatch: PatchCommit{Dir: "/repo", Ref: "refs/butler/patch"}}

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9501"
	fc.SetIssue(forge.Issue{Number: "9501"})

	pf := &fakePatchForge{prefix: "agent/issue-", IssueLabeler: fc, pushErr: errors.New("push boom")}
	newBox := func(c dispatch.Chore) dispatch.Dispatcher { return patchableDispatcher("docs-drift") }

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := New(backend, tree, fc.AsIssueFiler(), newBox, patchTestPolicy(), func() time.Time { return now }).WithPatchForge(pf, &fakePatchGate{})
	out, err := r.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != Swept || out.Patched != 0 || out.Promoted != 1 {
		t.Fatalf("Outcome = %+v, want Kind=Swept Patched=0 Promoted=1", out)
	}
	if len(pf.draftCalls) != 0 {
		t.Errorf("draftCalls = %+v, want none: a failed push must never reach CreateDraftPR", pf.draftCalls)
	}
	if len(pf.openPRCalls) != 0 {
		t.Errorf("openPRCalls = %v, want none: a failed push must never reach OpenPRForBranch", pf.openPRCalls)
	}
	if len(pf.deleteCalls) != 0 {
		t.Errorf("deleteCalls = %v, want none: PushBranch itself never created a branch to clean up", pf.deleteCalls)
	}

	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(tip.State.Patched) != 0 {
		t.Errorf("Patched = %v, want none", tip.State.Patched)
	}
	if !slices.Equal(tip.State.Promoted, []string{fc.PostIssueURL}) {
		t.Errorf("Promoted = %v, want [%s]", tip.State.Promoted, fc.PostIssueURL)
	}
	if tip.State.Phase != ledger.Done {
		t.Errorf("Phase = %q, want %q", tip.State.Phase, ledger.Done)
	}
}

// (r9, issue #4112) CreateDraftPR errors, but OpenPRForBranch finds the PR
// was created anyway (a timeout/5xx after the server made it): landPatch
// adopts it as if the create had succeeded. No fallback runs -- no delete,
// no AddLabels, no promotion comment -- and the Ledger records the adopted
// URL under Patched, same as an ordinary landed patch, with the merge gate
// handed that URL too.
func TestSweep_PatchPRCreateErrorsButPRWasCreatedAdopts(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	tree := fakeTree{head: "headsha", files: []string{"a.go"}, commitPatch: PatchCommit{Dir: "/repo", Ref: "refs/butler/patch"}}

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9504"
	fc.SetIssue(forge.Issue{Number: "9504"})

	pf := &fakePatchForge{
		prefix: "agent/issue-", IssueLabeler: fc, draftErr: errors.New("timeout"),
		openPR: forge.PR{URL: "https://example.com/pull/9504"}, openPRFound: true,
	}
	newBox := func(c dispatch.Chore) dispatch.Dispatcher { return patchableDispatcher("docs-drift") }

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	gate := &fakePatchGate{}
	r := New(backend, tree, fc.AsIssueFiler(), newBox, patchTestPolicy(), func() time.Time { return now }).WithPatchForge(pf, gate)
	out, err := r.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != Swept || out.Patched != 1 || out.Promoted != 0 {
		t.Fatalf("Outcome = %+v, want Kind=Swept Patched=1 Promoted=0", out)
	}
	if len(pf.deleteCalls) != 0 {
		t.Errorf("deleteCalls = %v, want none: a found PR must never be orphaned by deleting its head branch", pf.deleteCalls)
	}
	if len(fc.AddLabelsCalls) != 0 {
		t.Errorf("AddLabelsCalls = %+v, want none: the adopted PR is not a promotion fallback", fc.AddLabelsCalls)
	}
	if len(fc.CommentCalls) != 0 {
		t.Errorf("CommentCalls = %+v, want none: no promotion note for an adopted patch", fc.CommentCalls)
	}

	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !slices.Equal(tip.State.Patched, []string{pf.openPR.URL}) {
		t.Errorf("Patched = %v, want [%s]", tip.State.Patched, pf.openPR.URL)
	}
	if len(tip.State.Promoted) != 0 {
		t.Errorf("Promoted = %v, want none", tip.State.Promoted)
	}
	if len(gate.calls) != 1 || gate.calls[0].prURL != pf.openPR.URL {
		t.Fatalf("gate.calls = %+v, want one SettleAdopted call with %q", gate.calls, pf.openPR.URL)
	}
}

// (r10, issue #4112) CreateDraftPR errors and OpenPRForBranch itself errors:
// the branch is kept (a PR might be live that the lookup just couldn't see),
// and the create error still falls the finding back to promote.
func TestSweep_PatchPRLookupErrorsFallsBackToPromoteKeepsBranch(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	tree := fakeTree{head: "headsha", files: []string{"a.go"}, commitPatch: PatchCommit{Dir: "/repo", Ref: "refs/butler/patch"}}

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9505"
	fc.SetIssue(forge.Issue{Number: "9505"})

	pf := &fakePatchForge{prefix: "agent/issue-", IssueLabeler: fc, draftErr: errors.New("boom"), openPRErr: errors.New("lookup boom")}
	newBox := func(c dispatch.Chore) dispatch.Dispatcher { return patchableDispatcher("docs-drift") }

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := New(backend, tree, fc.AsIssueFiler(), newBox, patchTestPolicy(), func() time.Time { return now }).WithPatchForge(pf, &fakePatchGate{})
	var out Outcome
	var err error
	stdout := captureStdout(t, func() {
		out, err = r.Sweep([]string{"bugs"})
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != Swept || out.Patched != 0 || out.Promoted != 1 {
		t.Fatalf("Outcome = %+v, want Kind=Swept Patched=0 Promoted=1", out)
	}
	if len(pf.deleteCalls) != 0 {
		t.Errorf("deleteCalls = %v, want none: a failed lookup must never delete a branch a live PR might still need", pf.deleteCalls)
	}
	if !strings.Contains(stdout, "status=patch-pr-lookup-failed") || !strings.Contains(stdout, "lookup boom") {
		t.Errorf("settle log = %q, want a status=patch-pr-lookup-failed line naming the lookup error", stdout)
	}

	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !slices.Equal(tip.State.Promoted, []string{fc.PostIssueURL}) {
		t.Errorf("Promoted = %v, want [%s]", tip.State.Promoted, fc.PostIssueURL)
	}
}

// (r11, issue #4112) CreateDraftPR errors, no PR was created, and the
// cleanup DeleteBranch itself fails: cleanup failure is logged and best-
// effort -- the finding still falls back to promote as usual.
func TestSweep_PatchBranchCleanupFailsStillFallsBackToPromote(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	tree := fakeTree{head: "headsha", files: []string{"a.go"}, commitPatch: PatchCommit{Dir: "/repo", Ref: "refs/butler/patch"}}

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9506"
	fc.SetIssue(forge.Issue{Number: "9506"})

	pf := &fakePatchForge{prefix: "agent/issue-", IssueLabeler: fc, draftErr: errors.New("boom"), deleteErr: errors.New("delete boom")}
	newBox := func(c dispatch.Chore) dispatch.Dispatcher { return patchableDispatcher("docs-drift") }

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := New(backend, tree, fc.AsIssueFiler(), newBox, patchTestPolicy(), func() time.Time { return now }).WithPatchForge(pf, &fakePatchGate{})
	var out Outcome
	var err error
	stdout := captureStdout(t, func() {
		out, err = r.Sweep([]string{"bugs"})
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != Swept || out.Patched != 0 || out.Promoted != 1 {
		t.Fatalf("Outcome = %+v, want Kind=Swept Patched=0 Promoted=1", out)
	}
	if len(pf.deleteCalls) != 1 {
		t.Fatalf("deleteCalls = %+v, want exactly one attempt", pf.deleteCalls)
	}
	if !strings.Contains(stdout, "status=patch-branch-cleanup-failed") || !strings.Contains(stdout, "delete boom") {
		t.Errorf("settle log = %q, want a status=patch-branch-cleanup-failed line naming the delete error", stdout)
	}

	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !slices.Equal(tip.State.Promoted, []string{fc.PostIssueURL}) {
		t.Errorf("Promoted = %v, want [%s]", tip.State.Promoted, fc.PostIssueURL)
	}
}

// (r8) fallBackToPromote where AddLabels itself errors: no promotion is
// counted -- the label never actually landed -- and no promotion comment is
// posted, since that comment only makes sense once the label add succeeded.
func TestSweep_PatchFallbackAddLabelsFailsCountsNoPromotion(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	tree := fakeTree{head: "headsha", files: []string{"a.go"}, commitPatch: PatchCommit{Dir: "/repo", Ref: "refs/butler/patch"}}

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9503"
	fc.SetIssue(forge.Issue{Number: "9503"})
	fc.AddLabelsErr = errors.New("label boom")

	pf := &fakePatchForge{prefix: "agent/issue-", IssueLabeler: fc, draftErr: errors.New("boom")}
	newBox := func(c dispatch.Chore) dispatch.Dispatcher { return patchableDispatcher("docs-drift") }

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := New(backend, tree, fc.AsIssueFiler(), newBox, patchTestPolicy(), func() time.Time { return now }).WithPatchForge(pf, &fakePatchGate{})
	out, err := r.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != Swept || out.Patched != 0 || out.Promoted != 0 {
		t.Fatalf("Outcome = %+v, want Kind=Swept Patched=0 Promoted=0", out)
	}
	if len(fc.AddLabelsCalls) != 1 {
		t.Fatalf("AddLabelsCalls = %+v, want 1", fc.AddLabelsCalls)
	}
	if len(fc.CommentCalls) != 0 {
		t.Errorf("CommentCalls = %+v, want none: a failed AddLabels must never post the promotion note", fc.CommentCalls)
	}

	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(tip.State.Promoted) != 0 {
		t.Errorf("Promoted = %v, want none", tip.State.Promoted)
	}
	if tip.State.Phase != ledger.Done {
		t.Errorf("Phase = %q, want %q", tip.State.Phase, ledger.Done)
	}
}

// (r9) fallBackToPromote where the label add succeeds but the trailing
// Comment errors: the promotion itself still counts -- the label already
// landed -- only the note comment is lost, exactly as promotionNote's other
// best-effort callers behave.
func TestSweep_PatchFallbackCommentFailsStillCountsPromotion(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	tree := fakeTree{head: "headsha", files: []string{"a.go"}, commitPatch: PatchCommit{Dir: "/repo", Ref: "refs/butler/patch"}}

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9504"
	fc.SetIssue(forge.Issue{Number: "9504"})
	fc.CommentErr = errors.New("comment boom")

	pf := &fakePatchForge{prefix: "agent/issue-", IssueLabeler: fc, draftErr: errors.New("boom")}
	newBox := func(c dispatch.Chore) dispatch.Dispatcher { return patchableDispatcher("docs-drift") }

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := New(backend, tree, fc.AsIssueFiler(), newBox, patchTestPolicy(), func() time.Time { return now }).WithPatchForge(pf, &fakePatchGate{})
	out, err := r.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != Swept || out.Patched != 0 || out.Promoted != 1 {
		t.Fatalf("Outcome = %+v, want Kind=Swept Patched=0 Promoted=1", out)
	}
	if len(fc.AddLabelsCalls) != 1 || !slices.Contains(fc.AddLabelsCalls[0].Labels, "ready-for-agent") {
		t.Fatalf("AddLabelsCalls = %+v, want one call adding ready-for-agent", fc.AddLabelsCalls)
	}

	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !slices.Equal(tip.State.Promoted, []string{fc.PostIssueURL}) {
		t.Errorf("Promoted = %v, want [%s]", tip.State.Promoted, fc.PostIssueURL)
	}
	if tip.State.Phase != ledger.Done {
		t.Errorf("Phase = %q, want %q", tip.State.Phase, ledger.Done)
	}
}

// (r10) ISSUE_TRACKER=local paired with a patch-capable CODE_FORGE (issue
// #4074): the local tracker's PostIssue returns "local:"+slug, not a forge
// issue URL, so issueNumberFromURL must reject it before ever reaching
// patchForge -- no push, no draft PR. OnFiled returns as soon as
// issueNumberFromURL errors, before it would otherwise call
// fallBackToPromote, so no promotion happens either; the sweep still reaches
// its done commit. In production butlerPatchForge never hands ISSUE_TRACKER=
// local a PatchForge at all (it has no IssueLabeler), so this pins
// issueNumberFromURL's own guard as defense in depth, not the only thing
// standing between local and a patch attempt.
func TestSweep_PatchLocalTrackerIssueURLLeavesFindingFiled(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	tree := fakeTree{head: "headsha", files: []string{"a.go"}, commitPatch: PatchCommit{Dir: "/repo", Ref: "refs/butler/patch"}}

	fc := forge.NewFake()
	fc.PostIssueURL = "local:fix-typo"

	pf := &fakePatchForge{prefix: "agent/issue-", IssueLabeler: fc}
	newBox := func(c dispatch.Chore) dispatch.Dispatcher { return patchableDispatcher("docs-drift") }

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := New(backend, tree, fc.AsLocalIssueFiler(), newBox, patchTestPolicy(), func() time.Time { return now }).WithPatchForge(pf, &fakePatchGate{})
	out, err := r.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != Swept || out.Patched != 0 || out.Promoted != 0 {
		t.Fatalf("Outcome = %+v, want Kind=Swept Patched=0 Promoted=0: a local-tracker URL can never patch or promote", out)
	}
	if len(pf.pushCalls) != 0 {
		t.Errorf("pushCalls = %+v, want none: a local-tracker URL must never reach PushBranch", pf.pushCalls)
	}
	if len(pf.draftCalls) != 0 {
		t.Errorf("draftCalls = %+v, want none: a local-tracker URL must never reach CreateDraftPR", pf.draftCalls)
	}

	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(tip.State.Patched) != 0 {
		t.Errorf("Patched = %v, want none", tip.State.Patched)
	}
	if len(tip.State.Promoted) != 0 {
		t.Errorf("Promoted = %v, want none", tip.State.Promoted)
	}
	if tip.State.Phase != ledger.Done {
		t.Errorf("Phase = %q, want %q", tip.State.Phase, ledger.Done)
	}
}

// patchGateDispatchLabels mirrors settle_test.go's own testDispatchLabels:
// the DispatchLabels a real *settle.Settle needs to know what "agent-
// complete"/"agent-failed" actually spell, even though an unclaimed patch
// finding's issue never carries agent-in-progress in the first place.
var patchGateDispatchLabels = forge.DispatchLabels{
	Dispatchable: "ready-for-agent",
	InProgress:   "agent-in-progress",
	Complete:     "agent-complete",
	Failed:       "agent-failed",
}

// newPatchGateSettle builds the same shape of *settle.Settle production
// hands a landed butler patch PR to (issue #4076): MaxFixAttempts 0, since
// there is no Dispatcher to run a fix pass against, and Unclaimed true,
// since the finding issue was never claimed. fc backs both the tracker and
// the code/PR forge, so the PR URL fakePatchForge.CreateDraftPR hands back
// is the very URL this gate polls.
func newPatchGateSettle(fc *forge.Fake, mergeMode, guardPaths string) *settle.Settle {
	cfg := settle.Config{
		CompleteLabel:     "agent-complete",
		MergeMode:         mergeMode,
		MergeGuardPaths:   guardPaths,
		MergePollInterval: 1,
		MergePollTimeout:  100,
		MaxFixAttempts:    0,
		Unclaimed:         true,
		Clock:             dispatch.Clock{Now: time.Now, Sleep: func(time.Duration) {}},
		Capabilities:      forge.ResolveCapabilities(fc, fc, bkd.Descriptor{}, bkd.Descriptor{}),
	}
	return settle.New(cfg, fc, fc)
}

// snapshotGate wraps a real PatchGate and records the Ledger's state
// immediately before and after one SettleAdopted call, so a test can pin
// that a failed/no-op gate outcome writes no further Ledger commit on top
// of the Chore's own Done commit (issue #4076).
type snapshotGate struct {
	gate          PatchGate
	backend       ledger.Backend
	chore         string
	before, after ledger.State
}

func (g *snapshotGate) SettleAdopted(d dispatch.Dispatcher, num string, gen uint64, prURL string) {
	if tip, err := g.backend.Read(g.chore); err == nil {
		g.before = tip.State
	}
	g.gate.SettleAdopted(d, num, gen, prURL)
	if tip, err := g.backend.Read(g.chore); err == nil {
		g.after = tip.State
	}
}

// sameLabelSet compares two label sets order-insensitively.
func sameLabelSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			return false
		}
	}
	return true
}

// (a) The gate is handed each landed patch's issue number/PR URL only after
// the Chore's Done Ledger commit has actually landed (issue #4076): the
// recording fake gate reads the Ledger from inside its own call and finds
// the commit -- Phase done, claim released, Patched carrying the PR URL --
// already there.
func TestSweep_PatchGateCalledAfterDoneCommit(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	tree := fakeTree{head: "headsha", files: []string{"a.go"}, commitPatch: PatchCommit{Dir: "/repo", Ref: "refs/butler/patch"}}

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9500"
	fc.SetIssue(forge.Issue{Number: "9500"})

	prURL := "https://example.com/pull/500"
	pf := &fakePatchForge{prefix: "agent/issue-", IssueLabeler: fc, draftURL: prURL}
	newBox := func(c dispatch.Chore) dispatch.Dispatcher { return patchableDispatcher("docs-drift") }

	var sawDoneCommit bool
	gate := &fakePatchGate{}
	gate.onCall = func(num, url string) {
		tip, err := backend.Read("bugs")
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		sawDoneCommit = tip.State.Phase == ledger.Done && tip.State.ClaimedBy == nil && slices.Contains(tip.State.Patched, url)
	}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := New(backend, tree, fc.AsIssueFiler(), newBox, patchTestPolicy(), func() time.Time { return now }).WithPatchForge(pf, gate)
	if _, err := r.Sweep([]string{"bugs"}); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	if !sawDoneCommit {
		t.Errorf("gate ran before the Done commit landed, or the commit did not yet carry the patched PR URL")
	}
	if len(gate.calls) != 1 {
		t.Fatalf("gate calls = %+v, want exactly 1", gate.calls)
	}
	call := gate.calls[0]
	if call.d != nil {
		t.Errorf("gate dispatcher = %v, want nil", call.d)
	}
	if call.num != "9500" {
		t.Errorf("gate num = %q, want %q", call.num, "9500")
	}
	if call.gen != 0 {
		t.Errorf("gate gen = %d, want 0", call.gen)
	}
	if call.prURL != prURL {
		t.Errorf("gate prURL = %q, want %q", call.prURL, prURL)
	}
}

// (b) A red gate on a landed patch PR must never write a second Ledger
// commit on top of the Chore's own Done commit, and must leave the finding
// issue at exactly its two filed labels -- no agent-failed, since there was
// never an InProgress state for the gate to demote from (issue #4076).
func TestSweep_PatchGateRedLeavesLedgerAndLabelsUnchanged(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	tree := fakeTree{head: "headsha", files: []string{"a.go"}, commitPatch: PatchCommit{Dir: "/repo", Ref: "refs/butler/patch"}}

	fc := forge.NewFake(patchGateDispatchLabels)
	fc.PostIssueURL = "https://example.com/issues/9501"
	wantLabels := []string{"agent-butler-finding", "agent-butler-patch"}
	fc.SetIssue(forge.Issue{Number: "9501", Labels: append([]string{}, wantLabels...)})

	prURL := "https://example.com/pull/501"
	fc.SetCheckStates(prURL, []forge.RollupState{forge.StateFailure})
	pf := &fakePatchForge{prefix: "agent/issue-", IssueLabeler: fc, draftURL: prURL}
	newBox := func(c dispatch.Chore) dispatch.Dispatcher { return patchableDispatcher("docs-drift") }

	real := newPatchGateSettle(fc, "immediate", "")
	gate := &snapshotGate{gate: real, backend: backend, chore: "bugs"}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := New(backend, tree, fc.AsIssueFiler(), newBox, patchTestPolicy(), func() time.Time { return now }).WithPatchForge(pf, gate)
	out, err := r.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Patched != 1 {
		t.Errorf("Outcome.Patched = %d, want 1", out.Patched)
	}
	if !reflect.DeepEqual(gate.before, gate.after) {
		t.Errorf("Ledger state changed across the gate call: before=%+v after=%+v", gate.before, gate.after)
	}
	if fc.Merged != "" {
		t.Errorf("expected no merge on red CI; fc.Merged=%q", fc.Merged)
	}
	iss, _ := fc.Issue("9501")
	if !sameLabelSet(iss.Labels, wantLabels) {
		t.Errorf("labels = %v, want exactly %v", iss.Labels, wantLabels)
	}
}

// (c) Under MERGE_MODE=immediate with a green gate, a landed patch PR
// actually merges and its finding issue reaches agent-complete -- the one
// tracker transition an unclaimed issue does commit, since it reflects the
// PR having landed rather than a claim being released (issue #4076).
func TestSweep_PatchGateImmediateGreenMergesAndCompletes(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	tree := fakeTree{head: "headsha", files: []string{"a.go"}, commitPatch: PatchCommit{Dir: "/repo", Ref: "refs/butler/patch"}}

	fc := forge.NewFake(patchGateDispatchLabels)
	fc.PostIssueURL = "https://example.com/issues/9502"
	fc.SetIssue(forge.Issue{Number: "9502", Labels: []string{"agent-butler-finding", "agent-butler-patch"}})

	prURL := "https://example.com/pull/502"
	fc.SetCheckStates(prURL, []forge.RollupState{forge.StatePending, forge.StateSuccess, forge.StateSuccess})
	pf := &fakePatchForge{prefix: "agent/issue-", IssueLabeler: fc, draftURL: prURL}
	newBox := func(c dispatch.Chore) dispatch.Dispatcher { return patchableDispatcher("docs-drift") }

	gate := newPatchGateSettle(fc, "immediate", "")

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := New(backend, tree, fc.AsIssueFiler(), newBox, patchTestPolicy(), func() time.Time { return now }).WithPatchForge(pf, gate)
	out, err := r.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Patched != 1 {
		t.Errorf("Outcome.Patched = %d, want 1", out.Patched)
	}
	if fc.Merged != prURL {
		t.Errorf("fc.Merged = %q, want %q", fc.Merged, prURL)
	}
	iss, _ := fc.Issue("9502")
	if !slices.Contains(iss.Labels, "agent-complete") {
		t.Errorf("issue labels = %v, want agent-complete", iss.Labels)
	}
	if len(fc.CloseMergedIssueCalls) != 1 {
		t.Errorf("CloseMergedIssueCalls = %v, want exactly one merged-close", fc.CloseMergedIssueCalls)
	}
}

// (d) Green but MERGE_MODE=manual/auto never actually lands the PR, so an
// unclaimed finding issue's labels are left completely untouched -- there is
// nothing for the gate to commit (issue #4076).
func TestSweep_PatchGateManualAndAutoLeaveIssueUntouched(t *testing.T) {
	cases := []struct {
		mode          string
		wantAutoMerge bool
	}{
		{mode: "manual"},
		{mode: "auto", wantAutoMerge: true},
	}
	for i, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
			tree := fakeTree{head: "headsha", files: []string{"a.go"}, commitPatch: PatchCommit{Dir: "/repo", Ref: "refs/butler/patch"}}

			fc := forge.NewFake(patchGateDispatchLabels)
			fc.PostIssueURL = fmt.Sprintf("https://example.com/issues/951%d", i)
			num := fmt.Sprintf("951%d", i)
			wantLabels := []string{"agent-butler-finding", "agent-butler-patch"}
			fc.SetIssue(forge.Issue{Number: num, Labels: append([]string{}, wantLabels...)})

			prURL := fmt.Sprintf("https://example.com/pull/951%d", i)
			fc.SetCheckStates(prURL, []forge.RollupState{forge.StatePending, forge.StateSuccess, forge.StateSuccess})
			pf := &fakePatchForge{prefix: "agent/issue-", IssueLabeler: fc, draftURL: prURL}
			newBox := func(c dispatch.Chore) dispatch.Dispatcher { return patchableDispatcher("docs-drift") }

			gate := newPatchGateSettle(fc, tc.mode, "")

			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			r := New(backend, tree, fc.AsIssueFiler(), newBox, patchTestPolicy(), func() time.Time { return now }).WithPatchForge(pf, gate)
			out, err := r.Sweep([]string{"bugs"})
			if err != nil {
				t.Fatalf("Sweep: %v", err)
			}
			if out.Patched != 1 {
				t.Errorf("Outcome.Patched = %d, want 1", out.Patched)
			}
			if fc.Merged != "" {
				t.Errorf("mode=%s: expected no merge; fc.Merged=%q", tc.mode, fc.Merged)
			}
			if len(fc.MarkReadyCalls) != 1 {
				t.Errorf("mode=%s: expected exactly one MarkReady call; got %v", tc.mode, fc.MarkReadyCalls)
			}
			if tc.wantAutoMerge && len(fc.EnqueueAutoMergeCalls) != 1 {
				t.Errorf("mode=%s: expected auto-merge to be enqueued; got %v", tc.mode, fc.EnqueueAutoMergeCalls)
			}
			if len(fc.TransitionStateCalls) != 0 {
				t.Errorf("mode=%s: expected no TransitionState calls; got %+v", tc.mode, fc.TransitionStateCalls)
			}
			iss, _ := fc.Issue(num)
			if !sameLabelSet(iss.Labels, wantLabels) {
				t.Errorf("mode=%s: labels = %v, want exactly %v", tc.mode, iss.Labels, wantLabels)
			}
		})
	}
}

// (e) A merge guard hit on the landed patch's own PR downgrades it to
// manual -- the PR is never merged, the guard posts its own comment, and the
// finding issue's labels are left untouched, the same as an ordinary
// manual-mode green gate (issue #4076).
func TestSweep_PatchGateMergeGuardHitLeavesUnmerged(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	tree := fakeTree{head: "headsha", files: []string{"a.go"}, commitPatch: PatchCommit{Dir: "/repo", Ref: "refs/butler/patch"}}

	fc := forge.NewFake(patchGateDispatchLabels)
	fc.PostIssueURL = "https://example.com/issues/9520"
	wantLabels := []string{"agent-butler-finding", "agent-butler-patch"}
	fc.SetIssue(forge.Issue{Number: "9520", Labels: append([]string{}, wantLabels...)})

	prURL := "https://example.com/pull/520"
	fc.SetCheckStates(prURL, []forge.RollupState{forge.StatePending, forge.StateSuccess, forge.StateSuccess})
	fc.SetPRFiles(prURL, []string{".github/workflows/ci.yml"})
	pf := &fakePatchForge{prefix: "agent/issue-", IssueLabeler: fc, draftURL: prURL}
	newBox := func(c dispatch.Chore) dispatch.Dispatcher { return patchableDispatcher("docs-drift") }

	gate := newPatchGateSettle(fc, "immediate", ".github/**")

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := New(backend, tree, fc.AsIssueFiler(), newBox, patchTestPolicy(), func() time.Time { return now }).WithPatchForge(pf, gate)
	out, err := r.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Patched != 1 {
		t.Errorf("Outcome.Patched = %d, want 1", out.Patched)
	}
	if fc.Merged != "" {
		t.Errorf("merge guard must prevent Merge from being called; fc.Merged=%q", fc.Merged)
	}
	if len(fc.CommentCalls) != 1 {
		t.Fatalf("expected exactly one guard comment, got %d: %+v", len(fc.CommentCalls), fc.CommentCalls)
	}
	if !strings.Contains(fc.CommentCalls[0].Body, "MERGE_GUARD_PATHS") {
		t.Errorf("comment body = %q, want a reference to MERGE_GUARD_PATHS", fc.CommentCalls[0].Body)
	}
	iss, _ := fc.Issue("9520")
	if !sameLabelSet(iss.Labels, wantLabels) {
		t.Errorf("labels = %v, want exactly %v", iss.Labels, wantLabels)
	}
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns
// everything it printed -- settle's status lines are fmt.Printf'd straight
// to os.Stdout, so this is the only way a test can see one (issue #4075).
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close stdout pipe writer: %v", err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read stdout pipe: %v", err)
	}
	return buf.String()
}

// (r11) A diff touching CLAUDE.md -- allow-matched by "*.md" but explicitly
// denied by DefaultPatchPaths' own "!**/CLAUDE.md" entry (ADR 0057, issue
// #4075) -- never reaches the Tree at all: decide's patchPaths gate rejects
// it before CommitPatch is ever called, so the finding falls back to
// exactly what promote would do with no Patch, logging the gate's own
// reason as a patch-skipped status line.
func TestSweep_PatchOutsidePatchPathsNeverReachesTree(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	var commitPatchCalls int
	tree := fakeTree{head: "headsha", files: []string{"a.go"}, commitPatch: PatchCommit{Dir: "/repo", Ref: "refs/butler/patch"}, commitPatchCalls: &commitPatchCalls}

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9600"

	claudeDiff := "--- a/CLAUDE.md\n+++ b/CLAUDE.md\n@@ -1 +1 @@\n-old\n+new\n"
	d := dispatch.NewFake()
	d.RunResult = dispatch.Succeeded(dispatch.Result{
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "butler-bugs", Status: outcome.StatusReady, Note: "swept"},
		},
		IssueIntentsFound: true,
		IssueIntents: []string{
			fmt.Sprintf(`{"title":"fix typo","body":"repro","dedupTerms":["CLAUDE.md:Foo"],"class":"docs-drift","concurrence":"agreed","patch":%q}`, claudeDiff),
		},
	})
	pf := &fakePatchForge{prefix: "agent/issue-", IssueLabeler: fc}
	newBox := func(c dispatch.Chore) dispatch.Dispatcher { return d }

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := New(backend, tree, fc.AsIssueFiler(), newBox, patchTestPolicy(), func() time.Time { return now }).WithPatchForge(pf, &fakePatchGate{})

	var out Outcome
	var err error
	stdout := captureStdout(t, func() {
		out, err = r.Sweep([]string{"bugs"})
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != Swept || out.Filed != 1 || out.Patched != 0 || out.Promoted != 1 {
		t.Fatalf("Outcome = %+v, want Kind=Swept Filed=1 Patched=0 Promoted=1", out)
	}
	if commitPatchCalls != 0 {
		t.Errorf("commitPatchCalls = %d, want 0: patchPaths must reject CLAUDE.md before the Tree is ever touched", commitPatchCalls)
	}
	if len(pf.pushCalls) != 0 || len(pf.draftCalls) != 0 {
		t.Errorf("pushCalls = %v draftCalls = %v, want none", pf.pushCalls, pf.draftCalls)
	}

	labels := fc.PostIssueCalls[0].Labels
	if !slices.Contains(labels, "ready-for-agent") || slices.Contains(labels, "agent-butler-patch") {
		t.Fatalf("PostIssue labels = %v, want ready-for-agent and no agent-butler-patch", labels)
	}

	if !strings.Contains(stdout, "status=patch-skipped") || !strings.Contains(stdout, "outside patch paths") {
		t.Errorf("settle log = %q, want a status=patch-skipped line naming the patchPaths gate", stdout)
	}
}

// patchableTopLevelDispatcher is patchableDispatcher's sibling for a
// top-level file path (issue #4075): the two Sweep-level scanned-gate tests
// below need a real on-disk repo, and git mktree -- ledgertest.NewRepo's own
// fixture builder (see makeDiff's doc) -- only builds flat, single-segment
// trees, so their tracked file can't live under docs/.
func patchableTopLevelDispatcher(path, diff string) *dispatch.Fake {
	d := dispatch.NewFake()
	d.RunResult = dispatch.Succeeded(dispatch.Result{
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "butler-bugs", Status: outcome.StatusReady, Note: "swept"},
		},
		IssueIntentsFound: true,
		IssueIntents: []string{
			fmt.Sprintf(`{"title":"fix typo","body":"repro","dedupTerms":[%q],"class":"docs-drift","concurrence":"agreed","patch":%q}`, path+":Foo", diff),
		},
	})
	return d
}

// TestSweep_PatchAppliesToBaseHeadButNotScannedFallsThrough is the Sweep-
// level acceptance case for ADR 0057's ordered apply-check (issue #4075):
// newBox's closure advances the temp repo's branch only after Sweep has
// already captured the scanned commit (butler.go's r.tree.Head, read before
// newBox runs), so the diff applies to the moved base head but not to what
// was actually scanned -- CommitPatch's scanned-commit gate must still
// reject it, falling through to promote exactly as any other stale-diff
// candidate does, and the settle log names the gate it failed. Runs over a
// real GitTree/CommitPatch round trip, not fakeTree's scripted answer.
func TestSweep_PatchAppliesToBaseHeadButNotScannedFallsThrough(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	bare, _ := newTreeBareRepo(t, "a.md")
	advanceBranch(t, bare, "main", "a.md", "different content\n")
	tree := GitTree{Repo: bare, Name: "Butler Bot", Email: "butler@example.com"}

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9700"

	pf := &fakePatchForge{prefix: "agent/issue-", IssueLabeler: fc}
	diff := patchDiffFor("a.md")
	newBox := func(c dispatch.Chore) dispatch.Dispatcher {
		// Base moves while the Box "works": only now does a.md's content
		// match the diff's own context.
		advanceBranch(t, bare, "main", "a.md", "old\n")
		return patchableTopLevelDispatcher("a.md", diff)
	}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := New(backend, tree, fc.AsIssueFiler(), newBox, patchTestPolicy(), func() time.Time { return now }).WithPatchForge(pf, &fakePatchGate{})

	var out Outcome
	var err error
	logs := captureStdout(t, func() {
		out, err = r.Sweep([]string{"bugs"})
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != Swept || out.Patched != 0 || out.Promoted != 1 {
		t.Fatalf("Outcome = %+v, want Kind=Swept Patched=0 Promoted=1", out)
	}
	if len(pf.pushCalls) != 0 || len(pf.draftCalls) != 0 {
		t.Errorf("pushCalls = %v draftCalls = %v, want none: the scanned gate must reject before any push/PR", pf.pushCalls, pf.draftCalls)
	}
	if !strings.Contains(logs, "scanned commit") {
		t.Errorf("settle log = %q, want it to name the scanned commit gate", logs)
	}
}

// TestSweep_PatchAppliesToScannedAndMovedBaseHeadLands is the Sweep-level
// acceptance case's other half: a diff that applies to both the scanned
// commit and the (moved) base head lands as a real patch commit and draft
// PR, over a real GitTree/CommitPatch round trip rather than fakeTree's
// scripted answer.
func TestSweep_PatchAppliesToScannedAndMovedBaseHeadLands(t *testing.T) {
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	bare, _ := newTreeBareRepo(t, "a.md", "other.md")
	advanceBranch(t, bare, "main", "a.md", "old\n")
	tree := GitTree{Repo: bare, Name: "Butler Bot", Email: "butler@example.com"}

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9701"
	fc.SetIssue(forge.Issue{Number: "9701"})

	pf := &fakePatchForge{prefix: "agent/issue-", IssueLabeler: fc, draftURL: "https://example.com/pull/3"}
	diff := patchDiffFor("a.md")
	var movedHead string
	newBox := func(c dispatch.Chore) dispatch.Dispatcher {
		// Base moves while the Box "works", touching an already-tracked but
		// unrelated file so a.md still matches the diff's own context.
		movedHead = advanceBranch(t, bare, "main", "other.md", "unrelated\n")
		return patchableTopLevelDispatcher("a.md", diff)
	}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := New(backend, tree, fc.AsIssueFiler(), newBox, patchTestPolicy(), func() time.Time { return now }).WithPatchForge(pf, &fakePatchGate{})
	out, err := r.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != Swept || out.Patched != 1 || out.Promoted != 0 {
		t.Fatalf("Outcome = %+v, want Kind=Swept Patched=1 Promoted=0", out)
	}

	if len(pf.pushCalls) != 1 {
		t.Fatalf("pushCalls = %+v, want exactly one", pf.pushCalls)
	}
	push := pf.pushCalls[0]
	if push.srcDir != bare || push.localRef != patchRef || push.base != "main" {
		t.Errorf("push srcDir/localRef/base = %q/%q/%q, want %q/%q/%q", push.srcDir, push.localRef, push.base, bare, patchRef, "main")
	}

	commit := resolveRef(t, bare, patchRef)
	if parent := resolveRef(t, bare, commit+"^"); parent != movedHead {
		t.Errorf("patch commit parent = %s, want moved base head %s", parent, movedHead)
	}
	got, err := exec.Command("git", "-C", bare, "show", commit+":a.md").Output()
	if err != nil {
		t.Fatalf("show patched a.md: %v", err)
	}
	if string(got) != "new\n" {
		t.Errorf("patched a.md = %q, want %q", string(got), "new\n")
	}
}
