package promptassembly

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/passmachine"
)

// TestComposeReconcilesAgainstAssemble covers the acceptance criterion that
// per-source totals reconcile with the assembled prompt's actual byte
// length, across the four Env shapes Compose derives different pass sets
// for: a legacy single pass, five orchestrator passes split across two
// bodies, a single research pass, and a single fix-pass-on-warm-box pass.
func TestComposeReconcilesAgainstAssemble(t *testing.T) {
	reg := loadTestRegistry(t)

	orchestratorEnv := coveredEnv()
	orchestratorEnv.OrchestratorEnabled = true

	researchEnv := coveredEnv()
	researchEnv.DispatchKind = "research"

	fixPassEnv := coveredEnv()
	fixPassEnv.FixPass = 1

	cases := []struct {
		name string
		env  Env
	}{
		{"legacy", coveredEnv()},
		{"orchestrator", orchestratorEnv},
		{"research", researchEnv},
		{"fix pass", fixPassEnv},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := Assemble(tc.env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			comp, err := Compose(tc.env, reg, nil)
			if err != nil {
				t.Fatalf("Compose: %v", err)
			}
			if len(comp.Passes) == 0 {
				t.Fatalf("Compose returned zero passes")
			}

			for _, pass := range comp.Passes {
				want := len(result.Prompt)
				if pass.Template == "review-prompt.md" {
					want = len(result.ReviewPromptText)
				}
				if pass.Bytes != want {
					t.Errorf("pass %q Bytes = %d, want %d", pass.Pass, pass.Bytes, want)
				}

				sum := 0
				for _, s := range pass.Sources {
					sum += s.Bytes
				}
				if sum != pass.Bytes {
					t.Errorf("pass %q Sources sum = %d, want Bytes %d", pass.Pass, sum, pass.Bytes)
				}
				if pass.Remainder != 0 {
					t.Errorf("pass %q Remainder = %d, want 0", pass.Pass, pass.Remainder)
				}
			}
		})
	}
}

// TestComposePassKindDerivation covers Compose's pass-kind derivation per
// cell shape, and that Template names the file each pass's body actually
// rendered from.
func TestComposePassKindDerivation(t *testing.T) {
	reg := loadTestRegistry(t)

	t.Run("orchestrator on: five passes across two templates", func(t *testing.T) {
		env := coveredEnv()
		env.OrchestratorEnabled = true

		comp, err := Compose(env, reg, nil)
		if err != nil {
			t.Fatalf("Compose: %v", err)
		}

		want := []struct{ pass, template string }{
			{passmachine.KindImplement.ManifestKind(), "issue-prompt.md"},
			{passmachine.KindFix.ManifestKind(), "issue-prompt.md"},
			{passmachine.KindLand.ManifestKind(), "issue-prompt.md"},
			{passmachine.KindReview.ManifestKind(), "review-prompt.md"},
			{passmachine.KindDeltaReview.ManifestKind(), "review-prompt.md"},
		}
		if len(comp.Passes) != len(want) {
			t.Fatalf("len(Passes) = %d, want %d: %+v", len(comp.Passes), len(want), comp.Passes)
		}
		for i, w := range want {
			got := comp.Passes[i]
			if got.Pass != w.pass || got.Template != w.template {
				t.Errorf("Passes[%d] = {%q, %q}, want {%q, %q}", i, got.Pass, got.Template, w.pass, w.template)
			}
		}
	})

	t.Run("legacy: single pass on issue-prompt.md", func(t *testing.T) {
		comp, err := Compose(coveredEnv(), reg, nil)
		if err != nil {
			t.Fatalf("Compose: %v", err)
		}
		if len(comp.Passes) != 1 {
			t.Fatalf("len(Passes) = %d, want 1: %+v", len(comp.Passes), comp.Passes)
		}
		if got, want := comp.Passes[0].Pass, passmachine.KindLegacy.ManifestKind(); got != want {
			t.Errorf("Passes[0].Pass = %q, want %q", got, want)
		}
		if got, want := comp.Passes[0].Template, "issue-prompt.md"; got != want {
			t.Errorf("Passes[0].Template = %q, want %q", got, want)
		}
	})

	t.Run("research: single pass on research-prompt.md", func(t *testing.T) {
		env := coveredEnv()
		env.DispatchKind = "research"

		comp, err := Compose(env, reg, nil)
		if err != nil {
			t.Fatalf("Compose: %v", err)
		}
		if len(comp.Passes) != 1 {
			t.Fatalf("len(Passes) = %d, want 1: %+v", len(comp.Passes), comp.Passes)
		}
		if got, want := comp.Passes[0].Pass, "research"; got != want {
			t.Errorf("Passes[0].Pass = %q, want %q", got, want)
		}
		if got, want := comp.Passes[0].Template, "research-prompt.md"; got != want {
			t.Errorf("Passes[0].Template = %q, want %q", got, want)
		}
	})
}

// TestComposeAttribution covers that the reported breakdown attributes
// bytes to the source that actually produced them: a gate-on fragment, a
// gate-off fragment (absent entirely), an actually-injected contract block,
// and a carried substitution variable.
func TestComposeAttribution(t *testing.T) {
	reg := loadTestRegistry(t)

	t.Run("gated-on fragment contributes a fragment source, separator included", func(t *testing.T) {
		// coveredEnv's CavemanSkillBaked bakes CAVEMAN_BAKED on.
		comp, err := Compose(coveredEnv(), reg, nil)
		if err != nil {
			t.Fatalf("Compose: %v", err)
		}
		want := len(fragmentText(t, "caveman-default.md")) + len("\n\n")
		got := -1
		for _, s := range comp.Passes[0].Sources {
			if s.Kind == SourceFragment && s.Name == "caveman-default.md" {
				got = s.Bytes
			}
		}
		if got != want {
			t.Errorf("caveman-default.md fragment source Bytes = %d, want %d", got, want)
		}
	})

	t.Run("gated-off fragment contributes no source", func(t *testing.T) {
		// coveredEnv's TDDSkillBaked=true leaves TDD_UNBAKED off.
		comp, err := Compose(coveredEnv(), reg, nil)
		if err != nil {
			t.Fatalf("Compose: %v", err)
		}
		for _, s := range comp.Passes[0].Sources {
			if s.Kind == SourceFragment && s.Name == "tdd-unbaked.md" {
				t.Errorf("tdd-unbaked.md present in Sources (gate off), want absent: %+v", s)
			}
		}
	})

	t.Run("actually-injected contract file contributes a contract source", func(t *testing.T) {
		dir := t.TempDir()
		env := coveredEnv()
		env.FixPass = 1 // fix-prompt.md lacks its own "# COMMS" marker, so injection actually appends.
		env.CommsContractFile = writeContractFile(t, dir, "comms-contract.md", "# COMMS\n\ncomms body text\n")

		comp, err := Compose(env, reg, nil)
		if err != nil {
			t.Fatalf("Compose: %v", err)
		}
		found := false
		for _, s := range comp.Passes[0].Sources {
			if s.Kind == SourceContract && s.Name == "comms-contract.md" {
				found = true
				if s.Bytes == 0 {
					t.Errorf("comms-contract.md source Bytes = 0, want > 0")
				}
			}
		}
		if !found {
			t.Errorf("comms-contract.md missing from Sources: %+v", comp.Passes[0].Sources)
		}
	})

	t.Run("substitution variable contributes a var source", func(t *testing.T) {
		env := coveredEnv() // IssueNumber "2349" is non-empty.
		comp, err := Compose(env, reg, nil)
		if err != nil {
			t.Fatalf("Compose: %v", err)
		}
		found := false
		for _, s := range comp.Passes[0].Sources {
			if s.Kind == SourceVar && s.Name == "ISSUE_NUMBER" {
				found = true
				// ${ISSUE_NUMBER} appears more than once across the base
				// template and its gated-on fragments, so the aggregated
				// total is a multiple of one occurrence's length, not one
				// occurrence's length itself.
				if s.Bytes == 0 || s.Bytes%len(env.IssueNumber) != 0 {
					t.Errorf("ISSUE_NUMBER source Bytes = %d, want a positive multiple of %d", s.Bytes, len(env.IssueNumber))
				}
			}
		}
		if !found {
			t.Errorf("ISSUE_NUMBER var source missing: %+v", comp.Passes[0].Sources)
		}
	})
}

// TestComposeCarriedTextTargeting covers that an empty-Pass CarriedText
// lands on every reported pass, a pass-named one lands only there, and both
// still reconcile to a zero Remainder.
func TestComposeCarriedTextTargeting(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.OrchestratorEnabled = true

	const everyText = "shared handoff text"
	const fixText = "reviewer findings text"
	carried := []CarriedText{
		{Pass: "", Name: "run-state", Text: everyText},
		{Pass: passmachine.KindFix.ManifestKind(), Name: "reviewer-findings", Text: fixText},
	}

	comp, err := Compose(env, reg, carried)
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}

	for _, pass := range comp.Passes {
		var haveEvery, haveFix bool
		for _, s := range pass.Sources {
			if s.Kind != SourceCarried {
				continue
			}
			switch s.Name {
			case "run-state":
				haveEvery = true
				if s.Bytes != len(everyText) {
					t.Errorf("pass %q run-state Bytes = %d, want %d", pass.Pass, s.Bytes, len(everyText))
				}
			case "reviewer-findings":
				haveFix = true
			}
		}
		if !haveEvery {
			t.Errorf("pass %q missing the empty-Pass carried entry", pass.Pass)
		}
		wantFix := pass.Pass == passmachine.KindFix.ManifestKind()
		if haveFix != wantFix {
			t.Errorf("pass %q reviewer-findings present = %v, want %v", pass.Pass, haveFix, wantFix)
		}
		if pass.Remainder != 0 {
			t.Errorf("pass %q Remainder = %d, want 0", pass.Pass, pass.Remainder)
		}
	}
}

// TestDiffPasses covers DiffPasses's partition semantics directly, and that
// the partition invariant (SharedBytes + sum(Only*) == that side's Bytes)
// holds for every pair Compose itself reports.
func TestDiffPasses(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.OrchestratorEnabled = true

	comp, err := Compose(env, reg, nil)
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}

	byName := map[string]PassComposition{}
	for _, p := range comp.Passes {
		byName[p.Pass] = p
	}
	implement := byName[passmachine.KindImplement.ManifestKind()]
	fix := byName[passmachine.KindFix.ManifestKind()]
	review := byName[passmachine.KindReview.ManifestKind()]

	t.Run("implement vs fix: identical bodies are wholly shared", func(t *testing.T) {
		diff := DiffPasses(implement, fix)
		if len(diff.OnlyA) != 0 || len(diff.OnlyB) != 0 {
			t.Errorf("OnlyA/OnlyB not empty: %+v / %+v", diff.OnlyA, diff.OnlyB)
		}
		if diff.SharedBytes != implement.Bytes || diff.SharedBytes != fix.Bytes {
			t.Errorf("SharedBytes = %d, want %d (both sides identical)", diff.SharedBytes, implement.Bytes)
		}
	})

	t.Run("implement vs review: shares common sources, splits the rest", func(t *testing.T) {
		diff := DiffPasses(implement, review)
		if diff.SharedBytes == 0 {
			t.Errorf("SharedBytes = 0, want > 0 (ISSUE_NUMBER/BASE_BRANCH are carried into both templates)")
		}
		if len(diff.OnlyA) == 0 {
			t.Errorf("OnlyA empty, want implement-only content")
		}
		if len(diff.OnlyB) == 0 {
			t.Errorf("OnlyB empty, want review-only content")
		}
	})

	t.Run("partition invariant holds for every pair Compose reports", func(t *testing.T) {
		if len(comp.Diffs) != 10 { // C(5,2)
			t.Fatalf("len(Diffs) = %d, want 10", len(comp.Diffs))
		}
		for _, diff := range comp.Diffs {
			a := byName[diff.A]
			b := byName[diff.B]

			sumA := 0
			for _, s := range diff.OnlyA {
				sumA += s.Bytes
			}
			sumB := 0
			for _, s := range diff.OnlyB {
				sumB += s.Bytes
			}

			if diff.SharedBytes+sumA != a.Bytes {
				t.Errorf("%s/%s: SharedBytes(%d)+sum(OnlyA)(%d) = %d, want a.Bytes %d", diff.A, diff.B, diff.SharedBytes, sumA, diff.SharedBytes+sumA, a.Bytes)
			}
			if diff.SharedBytes+sumB != b.Bytes {
				t.Errorf("%s/%s: SharedBytes(%d)+sum(OnlyB)(%d) = %d, want b.Bytes %d", diff.A, diff.B, diff.SharedBytes, sumB, diff.SharedBytes+sumB, b.Bytes)
			}
		}
	})
}

// assertPartitionInvariant checks SharedBytes + sum(OnlyA) == a.Bytes and
// SharedBytes + sum(OnlyB) == b.Bytes for a diff produced from a, b.
func assertPartitionInvariant(t *testing.T, diff PassDiff, a, b PassComposition) {
	t.Helper()
	sumA := 0
	for _, s := range diff.OnlyA {
		sumA += s.Bytes
	}
	sumB := 0
	for _, s := range diff.OnlyB {
		sumB += s.Bytes
	}
	if diff.SharedBytes+sumA != a.Bytes {
		t.Errorf("SharedBytes(%d)+sum(OnlyA)(%d) = %d, want a.Bytes %d", diff.SharedBytes, sumA, diff.SharedBytes+sumA, a.Bytes)
	}
	if diff.SharedBytes+sumB != b.Bytes {
		t.Errorf("SharedBytes(%d)+sum(OnlyB)(%d) = %d, want b.Bytes %d", diff.SharedBytes, sumB, diff.SharedBytes+sumB, b.Bytes)
	}
}

// TestDiffPassesHandConstructed exercises DiffPasses directly from
// hand-built PassComposition values -- not Compose output -- so each
// pairwise byte-count relationship (a heavier, b heavier, only-one-side,
// zero-byte) is pinned independently of whatever Compose happens to
// produce for real templates.
func TestDiffPassesHandConstructed(t *testing.T) {
	srcShared := Source{Kind: SourceFragment, Name: "shared.md"}
	srcOnlyA := Source{Kind: SourceFragment, Name: "only-a.md"}
	srcOnlyB := Source{Kind: SourceFragment, Name: "only-b.md"}
	srcZero := Source{Kind: SourceVar, Name: "EMPTY"}

	t.Run("shared source: a heavier than b spills the remainder into OnlyA", func(t *testing.T) {
		a := PassComposition{Pass: "a", Bytes: 10, Sources: []SourceBytes{{Source: srcShared, Bytes: 10}}}
		b := PassComposition{Pass: "b", Bytes: 4, Sources: []SourceBytes{{Source: srcShared, Bytes: 4}}}

		diff := DiffPasses(a, b)

		if diff.SharedBytes != 4 {
			t.Errorf("SharedBytes = %d, want 4", diff.SharedBytes)
		}
		if want := []SourceBytes{{Source: srcShared, Bytes: 4}}; len(diff.Shared) != 1 || diff.Shared[0] != want[0] {
			t.Errorf("Shared = %+v, want %+v", diff.Shared, want)
		}
		if want := []SourceBytes{{Source: srcShared, Bytes: 6}}; len(diff.OnlyA) != 1 || diff.OnlyA[0] != want[0] {
			t.Errorf("OnlyA = %+v, want %+v", diff.OnlyA, want)
		}
		if len(diff.OnlyB) != 0 {
			t.Errorf("OnlyB = %+v, want empty", diff.OnlyB)
		}
		assertPartitionInvariant(t, diff, a, b)
	})

	t.Run("shared source: b heavier than a spills the remainder into OnlyB", func(t *testing.T) {
		a := PassComposition{Pass: "a", Bytes: 4, Sources: []SourceBytes{{Source: srcShared, Bytes: 4}}}
		b := PassComposition{Pass: "b", Bytes: 10, Sources: []SourceBytes{{Source: srcShared, Bytes: 10}}}

		diff := DiffPasses(a, b)

		if diff.SharedBytes != 4 {
			t.Errorf("SharedBytes = %d, want 4", diff.SharedBytes)
		}
		if len(diff.OnlyA) != 0 {
			t.Errorf("OnlyA = %+v, want empty", diff.OnlyA)
		}
		if want := []SourceBytes{{Source: srcShared, Bytes: 6}}; len(diff.OnlyB) != 1 || diff.OnlyB[0] != want[0] {
			t.Errorf("OnlyB = %+v, want %+v", diff.OnlyB, want)
		}
		assertPartitionInvariant(t, diff, a, b)
	})

	t.Run("source only on a", func(t *testing.T) {
		a := PassComposition{Pass: "a", Bytes: 5, Sources: []SourceBytes{{Source: srcOnlyA, Bytes: 5}}}
		b := PassComposition{Pass: "b", Bytes: 0}

		diff := DiffPasses(a, b)

		if len(diff.Shared) != 0 || diff.SharedBytes != 0 {
			t.Errorf("Shared = %+v SharedBytes = %d, want empty/0", diff.Shared, diff.SharedBytes)
		}
		if want := []SourceBytes{{Source: srcOnlyA, Bytes: 5}}; len(diff.OnlyA) != 1 || diff.OnlyA[0] != want[0] {
			t.Errorf("OnlyA = %+v, want %+v", diff.OnlyA, want)
		}
		if len(diff.OnlyB) != 0 {
			t.Errorf("OnlyB = %+v, want empty", diff.OnlyB)
		}
		assertPartitionInvariant(t, diff, a, b)
	})

	t.Run("source only on b", func(t *testing.T) {
		a := PassComposition{Pass: "a", Bytes: 0}
		b := PassComposition{Pass: "b", Bytes: 7, Sources: []SourceBytes{{Source: srcOnlyB, Bytes: 7}}}

		diff := DiffPasses(a, b)

		if want := []SourceBytes{{Source: srcOnlyB, Bytes: 7}}; len(diff.OnlyB) != 1 || diff.OnlyB[0] != want[0] {
			t.Errorf("OnlyB = %+v, want %+v", diff.OnlyB, want)
		}
		if len(diff.OnlyA) != 0 || diff.SharedBytes != 0 {
			t.Errorf("OnlyA = %+v SharedBytes = %d, want empty/0", diff.OnlyA, diff.SharedBytes)
		}
		assertPartitionInvariant(t, diff, a, b)
	})

	t.Run("zero-byte source on both sides contributes nothing to Shared/Only", func(t *testing.T) {
		a := PassComposition{Pass: "a", Bytes: 0, Sources: []SourceBytes{{Source: srcZero, Bytes: 0}}}
		b := PassComposition{Pass: "b", Bytes: 0, Sources: []SourceBytes{{Source: srcZero, Bytes: 0}}}

		diff := DiffPasses(a, b)

		if len(diff.Shared) != 0 || len(diff.OnlyA) != 0 || len(diff.OnlyB) != 0 || diff.SharedBytes != 0 {
			t.Errorf("diff = %+v, want fully empty", diff)
		}
		assertPartitionInvariant(t, diff, a, b)
	})
}

// TestComposeCarriedNoDuplicateSources covers the two collisions the old
// code let through: a carried block named the same as a substituted scalar
// var (now a different SourceKind by construction, issue #3444), and two
// carried blocks sharing one name (folded through sourceAggregator instead
// of appended as two rows).
func TestComposeCarriedNoDuplicateSources(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.OrchestratorEnabled = true

	carried := []CarriedText{
		{Name: "ISSUE_NUMBER", Text: "carried block text"},
		{Name: "dup", Text: "first"},
		{Name: "dup", Text: "second"},
	}

	comp, err := Compose(env, reg, carried)
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}

	byName := map[string]PassComposition{}
	for _, pass := range comp.Passes {
		byName[pass.Pass] = pass

		seen := map[Source]bool{}
		for _, s := range pass.Sources {
			if seen[s.Source] {
				t.Errorf("pass %q Sources has duplicate Source %+v", pass.Pass, s.Source)
			}
			seen[s.Source] = true
		}
		if pass.Remainder != 0 {
			t.Errorf("pass %q Remainder = %d, want 0", pass.Pass, pass.Remainder)
		}

		for _, s := range pass.Sources {
			if s.Kind == SourceCarried && s.Name == "dup" {
				if want := len("first") + len("second"); s.Bytes != want {
					t.Errorf("pass %q dup carried Bytes = %d, want %d", pass.Pass, s.Bytes, want)
				}
			}
		}
	}

	for _, diff := range comp.Diffs {
		assertPartitionInvariant(t, diff, byName[diff.A], byName[diff.B])
	}
}

// TestComposeUnknownCarriedPassErrors covers the non-blocking finding at
// composition.go:106: a carried block naming a pass kind this cell does not
// render must error, naming both the block and the pass, rather than
// silently vanishing from every pass.
func TestComposeUnknownCarriedPassErrors(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.OrchestratorEnabled = true

	carried := []CarriedText{{Pass: "settel", Name: "typo", Text: "x"}}

	_, err := Compose(env, reg, carried)
	if err == nil {
		t.Fatal("Compose: want error for unknown carried Pass, got nil")
	}
	if !strings.Contains(err.Error(), "settel") {
		t.Errorf("error = %q, want it to mention pass name %q", err.Error(), "settel")
	}
	if !strings.Contains(err.Error(), "typo") {
		t.Errorf("error = %q, want it to mention carried block name %q", err.Error(), "typo")
	}
}
