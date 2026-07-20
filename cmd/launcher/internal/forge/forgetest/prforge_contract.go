package forgetest

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// PRForgeHarness lets RunPRForgeContract drive a PRForge's scripted backend
// without knowing which adapter it is. Only adapters that open PRs and watch
// CI implement PRForge (github, the Fake); the push-only git adapter has no
// harness here at all — its absence of PRForge is pinned from the other
// side, by PushOnlyCodeForgeProvider below and by the sibling CodeForge
// contract's PushOnly marker (issue #1545).
type PRForgeHarness interface {
	// Forge returns the PRForge under test.
	Forge() forge.PRForge
	// CodeForge returns the same underlying adapter as forge.CodeForge — the
	// statically-typed handle callers actually hold before discovering
	// PRForge via `cf.(forge.PRForge)`, and the value Merge lands ref
	// through for the merge/PRState transition scenario.
	CodeForge() forge.CodeForge
	// SeedOpenPR opens a non-draft PR for issue num's agent branch and
	// returns its URL — the ref Merge and every other PRForge method below
	// expect.
	SeedOpenPR(num string) string
	// SeedCheckStates scripts the sequence of RollupState values CheckState
	// returns for url on successive calls, in order.
	SeedCheckStates(url string, states []forge.RollupState)
	// SeedFailingCheck scripts url's head commit to have one failing check
	// named name with the given conclusion and summary — the failure-detail
	// surface settle renders into fix-pass prompts.
	SeedFailingCheck(url, name, conclusion, summary string)
	// SeedAutoMergeAllowed scripts CanAutoMerge's result for the repo under
	// test.
	SeedAutoMergeAllowed(allowed bool)
	// AutoMergeEnqueued reports whether EnqueueAutoMerge actually recorded
	// url as enqueued — proof of a side effect, not just a nil error.
	AutoMergeEnqueued(url string) bool
}

// PushOnlyCodeForgeProvider is implemented by harnesses that can also
// produce a push-only CodeForge value from the same underlying adapter — the
// Fake, wrapped with AsPushOnly() — letting the discovery scenario prove the
// negative half of `cf.(forge.PRForge)` (a push-only forge doesn't satisfy
// it) alongside the positive half every PRForgeHarness proves by
// definition. github has no push-only shape of its own (a github CodeForge
// always opens PRs), so its harness leaves this unimplemented and the
// scenario no-ops for it, the same way CodeForge contract's PushOnly marker
// no-ops for github (issue #1545).
type PushOnlyCodeForgeProvider interface {
	PushOnlyCodeForge() forge.CodeForge
}

// RunPRForgeContract runs the shared PRForge conformance suite against h.
// Every PRForge-capable adapter package calls this from its own test file,
// backed by its own scripted-backend harness.
func RunPRForgeContract(t *testing.T, h PRForgeHarness) {
	t.Run("OptionalInterfaceDiscovery", func(t *testing.T) { testOptionalInterfaceDiscovery(t, h) })
	t.Run("PRForBranchResolution", func(t *testing.T) { testPRForBranchResolution(t, h) })
	t.Run("CheckStateSequence", func(t *testing.T) { testCheckStateSequence(t, h) })
	t.Run("MergeTransitionsPRState", func(t *testing.T) { testMergeTransitionsPRState(t, h) })
	t.Run("AutoMergeEligibility", func(t *testing.T) { testAutoMergeEligibility(t, h) })
	t.Run("FailureDetailOnFailingCheck", func(t *testing.T) { testFailureDetailOnFailingCheck(t, h) })
}

// testOptionalInterfaceDiscovery verifies that the standard Go
// optional-interface pattern callers use to find PRForge —
// `pr, ok := cf.(forge.PRForge)` — reports true for a PR-capable forge and
// false for a push-only one, for both halves on the same underlying
// adapter where the harness can produce them.
func testOptionalInterfaceDiscovery(t *testing.T, h PRForgeHarness) {
	if _, ok := h.CodeForge().(forge.PRForge); !ok {
		t.Fatal("PR-capable harness's CodeForge does not satisfy forge.PRForge")
	}
	p, ok := h.(PushOnlyCodeForgeProvider)
	if !ok {
		return
	}
	if _, ok := p.PushOnlyCodeForge().(forge.PRForge); ok {
		t.Fatal("push-only CodeForge satisfies forge.PRForge, want it hidden")
	}
}

// testPRForBranchResolution verifies OpenPRForBranch and PRForBranch both
// resolve a seeded PR's branch to its URL, and both report absence for a
// branch with no PR at all — the settle package's landing-PR discovery path.
func testPRForBranchResolution(t *testing.T, h PRForgeHarness) {
	const num = "201"
	branch := h.CodeForge().AgentBranch(num)
	wantURL := h.SeedOpenPR(num)

	pr, ok, err := h.Forge().OpenPRForBranch(branch)
	if err != nil {
		t.Fatalf("OpenPRForBranch(%q): %v", branch, err)
	}
	if !ok || pr.URL != wantURL {
		t.Fatalf("OpenPRForBranch(%q) = (%+v, %v), want URL %q", branch, pr, ok, wantURL)
	}

	url, ok, err := h.Forge().PRForBranch(branch)
	if err != nil {
		t.Fatalf("PRForBranch(%q): %v", branch, err)
	}
	if !ok || url != wantURL {
		t.Fatalf("PRForBranch(%q) = (%q, %v), want %q", branch, url, ok, wantURL)
	}

	const unknownBranch = "agent/issue-no-such-pr"
	if _, ok, err := h.Forge().OpenPRForBranch(unknownBranch); ok || err != nil {
		t.Fatalf("OpenPRForBranch(%q) = (_, %v, %v), want (_, false, nil)", unknownBranch, ok, err)
	}
	if _, ok, err := h.Forge().PRForBranch(unknownBranch); ok || err != nil {
		t.Fatalf("PRForBranch(%q) = (_, %v, %v), want (_, false, nil)", unknownBranch, ok, err)
	}
}

// testCheckStateSequence verifies CheckState pops a scripted rollup sequence
// in order for three shapes settle's gate-to-green polling loop actually
// meets: green (immediate SUCCESS), red (immediate FAILURE), and
// blocked-then-green (PENDING polls before a SUCCESS lands) — then reports
// StateNone once the sequence is exhausted.
func testCheckStateSequence(t *testing.T, h PRForgeHarness) {
	cases := []struct {
		name   string
		num    string
		states []forge.RollupState
	}{
		{"green", "202", []forge.RollupState{forge.StateSuccess}},
		{"red", "203", []forge.RollupState{forge.StateFailure}},
		{"blocked-then-green", "204", []forge.RollupState{forge.StatePending, forge.StatePending, forge.StateSuccess}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url := h.SeedOpenPR(tc.num)
			h.SeedCheckStates(url, tc.states)

			for i, want := range tc.states {
				got, err := h.Forge().CheckState(url)
				if err != nil {
					t.Fatalf("CheckState poll %d: %v", i, err)
				}
				if got != want {
					t.Fatalf("CheckState poll %d = %q, want %q", i, got, want)
				}
			}
			got, err := h.Forge().CheckState(url)
			if err != nil {
				t.Fatalf("CheckState poll (exhausted): %v", err)
			}
			if got != forge.StateNone {
				t.Fatalf("CheckState poll (exhausted) = %q, want %q", got, forge.StateNone)
			}
		})
	}
}

// testMergeTransitionsPRState verifies that CodeForge.Merge landing a
// seeded PR is the one event that flips PRForge.PRState from OPEN to
// MERGED — the "PR state transitions on merge" semantics settle's merge
// gate depends on to stop polling once a PR has actually landed.
func testMergeTransitionsPRState(t *testing.T, h PRForgeHarness) {
	const num = "205"
	url := h.SeedOpenPR(num)

	if got, err := h.Forge().PRState(url); err != nil || got != forge.PROpen {
		t.Fatalf("PRState before Merge = (%q, %v), want (%q, nil)", got, err, forge.PROpen)
	}

	if err := h.CodeForge().Merge(url); err != nil {
		t.Fatalf("Merge(%q): %v", url, err)
	}

	got, err := h.Forge().PRState(url)
	if err != nil {
		t.Fatalf("PRState after Merge: %v", err)
	}
	if got != forge.PRMerged {
		t.Fatalf("PRState after Merge = %q, want %q", got, forge.PRMerged)
	}
}

// testAutoMergeEligibility verifies CanAutoMerge reports the repo's
// scripted eligibility, EnqueueAutoMerge succeeds for a seeded PR, and
// MarkReady is idempotent — settle's self-heal merge gate calls it
// unconditionally on every green PR whether or not the driver already
// flipped it (exec_pr.go's MarkReady doc).
func testAutoMergeEligibility(t *testing.T, h PRForgeHarness) {
	h.SeedAutoMergeAllowed(true)
	if allowed, err := h.Forge().CanAutoMerge(); err != nil || !allowed {
		t.Fatalf("CanAutoMerge() = (%v, %v), want (true, nil)", allowed, err)
	}

	h.SeedAutoMergeAllowed(false)
	if allowed, err := h.Forge().CanAutoMerge(); err != nil || allowed {
		t.Fatalf("CanAutoMerge() = (%v, %v), want (false, nil)", allowed, err)
	}

	const num = "206"
	url := h.SeedOpenPR(num)

	if err := h.Forge().EnqueueAutoMerge(url); err != nil {
		t.Fatalf("EnqueueAutoMerge(%q): %v", url, err)
	}
	if !h.AutoMergeEnqueued(url) {
		t.Fatalf("AutoMergeEnqueued(%q) = false after a successful EnqueueAutoMerge call", url)
	}

	if err := h.Forge().MarkReady(url); err != nil {
		t.Fatalf("MarkReady(%q) first call: %v", url, err)
	}
	if err := h.Forge().MarkReady(url); err != nil {
		t.Fatalf("MarkReady(%q) second call (already ready): %v", url, err)
	}
}

// testFailureDetailOnFailingCheck verifies FailureDetail is empty for a PR
// with nothing scripted as failing, and surfaces the failing check's name
// and conclusion once one is scripted — the failure-detail surface settle
// renders into fix-pass prompts (gateRedRetry, settle/ready.go).
func testFailureDetailOnFailingCheck(t *testing.T, h PRForgeHarness) {
	const cleanNum = "207"
	cleanURL := h.SeedOpenPR(cleanNum)
	if detail, err := h.Forge().FailureDetail(cleanURL); err != nil || detail != "" {
		t.Fatalf("FailureDetail(%q) with nothing failing = (%q, %v), want (\"\", nil)", cleanURL, detail, err)
	}

	const failingNum = "208"
	failingURL := h.SeedOpenPR(failingNum)
	h.SeedFailingCheck(failingURL, "build", "FAILURE", "exit status 1")

	detail, err := h.Forge().FailureDetail(failingURL)
	if err != nil {
		t.Fatalf("FailureDetail(%q): %v", failingURL, err)
	}
	if !strings.Contains(detail, "build") || !strings.Contains(detail, "FAILURE") {
		t.Fatalf("FailureDetail(%q) = %q, want it to mention the failing check's name and conclusion", failingURL, detail)
	}
}
