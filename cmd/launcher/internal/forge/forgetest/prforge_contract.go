package forgetest

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// PRForgeHarness lets RunPRForgeContract drive a PRForge's scripted backend
// without knowing which adapter it is. The push-only git adapter has no
// harness here; its absence of PRForge is pinned by PushOnlyCodeForgeProvider
// below and by the CodeForge contract's PushOnly marker (issue #1545).
type PRForgeHarness interface {
	Forge() forge.PRForge
	// CodeForge returns the same adapter as the statically-typed handle
	// callers hold before discovering PRForge via cf.(forge.PRForge).
	CodeForge() forge.CodeForge
	// SeedOpenPR returns the PR's URL, the ref every PRForge method takes.
	SeedOpenPR(num string) string
	// SeedDraftPR covers issue #2408: OpenPRForBranch must adopt a stranded
	// draft PR exactly as it adopts a non-draft one, on every adapter.
	SeedDraftPR(num string) string
	// SeedCheckStates scripts the RollupState values CheckState returns for
	// url on successive calls, in order.
	SeedCheckStates(url string, states []forge.RollupState)
	SeedFailingCheck(url, name, conclusion, summary string)
	SeedAutoMergeAllowed(allowed bool)
	// SeedNeedsUpdate scripts url's NeedsUpdate result: true when the PR's
	// base branch has commits its head has not incorporated yet.
	SeedNeedsUpdate(url string, needsUpdate bool)
	// AutoMergeEnqueued reports whether EnqueueAutoMerge recorded url as
	// enqueued, which is proof of a side effect rather than a nil error.
	AutoMergeEnqueued(url string) bool
}

// PushOnlyCodeForgeProvider is implemented by harnesses that can also produce
// a push-only CodeForge from the same adapter, so the discovery scenario can
// prove a push-only forge fails the PRForge type assertion. A github CodeForge
// always opens PRs, so its harness leaves this unimplemented and the scenario
// no-ops for it (issue #1545).
type PushOnlyCodeForgeProvider interface {
	PushOnlyCodeForge() forge.CodeForge
}

// RunPRForgeContract runs the shared PRForge conformance suite against h.
func RunPRForgeContract(t *testing.T, h PRForgeHarness) {
	t.Run("OptionalInterfaceDiscovery", func(t *testing.T) { testOptionalInterfaceDiscovery(t, h) })
	t.Run("PRForBranchResolution", func(t *testing.T) { testPRForBranchResolution(t, h) })
	t.Run("OpenPRForBranchAdoptsDraft", func(t *testing.T) { testOpenPRForBranchAdoptsDraft(t, h) })
	t.Run("MarkReadyClearsAdoptedDraftThenMerges", func(t *testing.T) { testMarkReadyClearsAdoptedDraftThenMerges(t, h) })
	t.Run("CheckStateSequence", func(t *testing.T) { testCheckStateSequence(t, h) })
	t.Run("MergeTransitionsPRState", func(t *testing.T) { testMergeTransitionsPRState(t, h) })
	t.Run("AutoMergeEligibility", func(t *testing.T) { testAutoMergeEligibility(t, h) })
	t.Run("MarkDraftIdempotent", func(t *testing.T) { testMarkDraftIdempotent(t, h) })
	t.Run("FailureDetailOnFailingCheck", func(t *testing.T) { testFailureDetailOnFailingCheck(t, h) })
	t.Run("NeedsUpdate", func(t *testing.T) { testNeedsUpdate(t, h) })
}

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

// testOpenPRForBranchAdoptsDraft pins issue #2408: a stranded draft PR is as
// adoptable as a ready one, on every PRForge-capable adapter.
func testOpenPRForBranchAdoptsDraft(t *testing.T, h PRForgeHarness) {
	const num = "202"
	branch := h.CodeForge().AgentBranch(num)
	wantURL := h.SeedDraftPR(num)

	pr, ok, err := h.Forge().OpenPRForBranch(branch)
	if err != nil {
		t.Fatalf("OpenPRForBranch(%q): %v", branch, err)
	}
	if !ok || pr.URL != wantURL {
		t.Fatalf("OpenPRForBranch(%q) = (%+v, %v), want URL %q", branch, pr, ok, wantURL)
	}
}

// testMarkReadyClearsAdoptedDraftThenMerges is the companion half of issue
// #2408: MarkReady must clear whatever draft signal the adapter uses, so an
// adopted draft becomes mergeable and Merge succeeds on it.
func testMarkReadyClearsAdoptedDraftThenMerges(t *testing.T, h PRForgeHarness) {
	const num = "214"
	branch := h.CodeForge().AgentBranch(num)
	wantURL := h.SeedDraftPR(num)

	pr, ok, err := h.Forge().OpenPRForBranch(branch)
	if err != nil {
		t.Fatalf("OpenPRForBranch(%q): %v", branch, err)
	}
	if !ok || pr.URL != wantURL {
		t.Fatalf("OpenPRForBranch(%q) = (%+v, %v), want URL %q", branch, pr, ok, wantURL)
	}

	if err := h.Forge().MarkReady(wantURL); err != nil {
		t.Fatalf("MarkReady(%q): %v", wantURL, err)
	}

	pr, ok, err = h.Forge().OpenPRForBranch(branch)
	if err != nil {
		t.Fatalf("OpenPRForBranch(%q) after MarkReady: %v", branch, err)
	}
	if !ok || pr.URL != wantURL {
		t.Fatalf("OpenPRForBranch(%q) after MarkReady = (%+v, %v), want URL %q", branch, pr, ok, wantURL)
	}

	if err := h.CodeForge().Merge(wantURL); err != nil {
		t.Fatalf("Merge(%q) after MarkReady: %v", wantURL, err)
	}

	if got, err := h.Forge().PRState(wantURL); err != nil || got != forge.PRMerged {
		t.Fatalf("PRState(%q) after Merge = (%q, %v), want (%q, nil)", wantURL, got, err, forge.PRMerged)
	}
}

// testCheckStateSequence covers the three shapes settle's gate-to-green
// polling loop meets, then the exhausted case that must report StateNone.
func testCheckStateSequence(t *testing.T, h PRForgeHarness) {
	cases := []struct {
		name   string
		num    string
		states []forge.RollupState
	}{
		{"green", "210", []forge.RollupState{forge.StateSuccess}},
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

// testMergeTransitionsPRState pins Merge as the one event flipping PRState
// from OPEN to MERGED, which is how settle's merge gate stops polling.
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

// testAutoMergeEligibility pins MarkReady as idempotent: settle's self-heal
// merge gate calls it on every green PR whether or not the driver already
// flipped it.
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

// testMarkDraftIdempotent holds MarkDraft to MarkReady's idempotence rule.
func testMarkDraftIdempotent(t *testing.T, h PRForgeHarness) {
	const num = "209"
	url := h.SeedOpenPR(num)

	if err := h.Forge().MarkDraft(url); err != nil {
		t.Fatalf("MarkDraft(%q) first call: %v", url, err)
	}
	if err := h.Forge().MarkDraft(url); err != nil {
		t.Fatalf("MarkDraft(%q) second call (already draft): %v", url, err)
	}
}

// testFailureDetailOnFailingCheck pins the detail settle renders into
// fix-pass prompts (gateRedRetry).
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

// testNeedsUpdate pins the shared behind-detection semantics every adapter
// must agree on (issue #936, issue #2258): github's native compare API and
// forgejo's swapped-refs compensation must return the same bool for the same
// scripted fact.
func testNeedsUpdate(t *testing.T, h PRForgeHarness) {
	cases := []struct {
		name        string
		num         string
		needsUpdate bool
	}{
		{"stale", "212", true},
		{"upToDate", "213", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url := h.SeedOpenPR(tc.num)
			h.SeedNeedsUpdate(url, tc.needsUpdate)
			got, err := h.Forge().NeedsUpdate(url)
			if err != nil {
				t.Fatalf("NeedsUpdate(%q): %v", url, err)
			}
			if got != tc.needsUpdate {
				t.Fatalf("NeedsUpdate(%q) = %v, want %v", url, got, tc.needsUpdate)
			}
		})
	}
}
