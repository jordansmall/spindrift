package butler

import (
	"fmt"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/ledger/ledgertest"
	"spindrift.dev/launcher/internal/outcome"
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
// Chores built from chores, each with the same Every and no Classes --
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
			chores[i].Classes = classes
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
// line at all, the shape a killed or crashed Box leaves.
func crashedDispatcher() *dispatch.Fake {
	d := dispatch.NewFake()
	d.RunResult = dispatch.Result{Success: false}
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
	d.RunResult = dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "butler-bugs", Status: outcome.StatusReady, Note: "swept"},
		},
		IssueIntentsFound: true,
		IssueIntents: []string{
			fmt.Sprintf(`{"title":"bug found","body":"repro","dedupTerms":["a.go:Foo"],"class":%q,"concurrence":"agreed"}`, class),
		},
	}
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
	seed.RunResult = dispatch.Result{
		Success: true,
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
	}
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
			`{"title":"bug three","body":"repro","dedupTerms":["a.go:Three"]}`,
			`{"title":"bug four","body":"repro","dedupTerms":["a.go:Four"]}`,
			`{"title":"bug five","body":"repro","dedupTerms":["a.go:Five"]}`,
			`{"title":"bug six","body":"repro","dedupTerms":["a.go:Six"]}`,
			`{"title":"bug seven","body":"repro","dedupTerms":["a.go:Seven"]}`,
		},
	}
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
// 0, so wiring the class allow-list alone (Chore.Classes) never promotes
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
	// class list even though the Chore has Classes, since nothing it could
	// hand back would ever promote (issue #3880).
	if len(gotChore.Classes) != 0 {
		t.Errorf("dispatch.Chore.Classes = %v, want none with promotion off", gotChore.Classes)
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
	if want := []string{"error-handling"}; !slices.Equal(gotChore.Classes, want) {
		t.Errorf("dispatch.Chore.Classes = %v, want %v", gotChore.Classes, want)
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
	// must not be told a class list even though the Chore has Classes and
	// promotion is on, mirroring the promotion-off case above (issue #3994
	// review finding, butler.go's room.Promotions > 0 gate).
	if len(gotChore.Classes) != 0 {
		t.Errorf("dispatch.Chore.Classes = %v, want none with today's promotion room spent", gotChore.Classes)
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
