package promptassembly

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/passmachine"
)

const cavemanBody = "caveman body"

// composeRegistry is the synthetic cell the Compose tests share: a gated-on
// and a gated-off fragment plus a template per kind. ${ISSUE_NUMBER} appears
// twice in the work base and once in the review template, so var attribution
// is shared across the two bodies. The ungated rows cover the other attribution
// paths the source-sum reconciliation must hold through: an Inline var spliced
// mid-sentence by a parent fragment, a Shared axis row read from the root file
// (no forge folder is staged), a LegacyFragment fallback (only the legacy file
// is staged), and a body interpolating ${FORGE_DISPLAY_NAME}.
func composeRegistry(t *testing.T) (Registry, Env) {
	t.Helper()
	return syntheticRegistry(t,
		[]FragmentRow{
			{Gate: "CAVEMAN_BAKED", Fragment: "caveman.md", Var: "CAVEMAN_STEP"},
			{Gate: "TDD_UNBAKED", Fragment: "tdd-unbaked.md", Var: "TDD_UNBAKED_STEP"},
			{Fragment: "inline.md", Var: "INLINE_NAME", Inline: true},
			{Fragment: "parent.md", Var: "PARENT_STEP"},
			{Axis: "forge", Fragment: "forge-step.md", Shared: true, Var: "FORGE_STEP", ExtraSubstVars: []string{"FORGE_DISPLAY_NAME"}},
			{Fragment: "new.md", LegacyFragment: "old.md", Var: "LEGACY_STEP"},
		},
		map[string]string{
			"issue-prompt.md":          "issue ${ISSUE_NUMBER} ${CAVEMAN_STEP}${TDD_UNBAKED_STEP}${PARENT_STEP}${FORGE_STEP}${LEGACY_STEP}on ${ISSUE_NUMBER}\n",
			"review-prompt.md":         "review ${ISSUE_NUMBER} ${BASE_BRANCH}\n",
			"fix-prompt.md":            "fix\n",
			"research-prompt.md":       "research\n",
			"fragments/caveman.md":     cavemanBody,
			"fragments/tdd-unbaked.md": "tdd unbaked body",
			"fragments/inline.md":      "the tracker",
			"fragments/parent.md":      "see ${INLINE_NAME} now",
			"fragments/forge-step.md":  "land via ${FORGE_DISPLAY_NAME}",
			"fragments/old.md":         "legacy body",
		})
}

// The four Env shapes are here because Compose derives a different pass set for
// each: a legacy single pass, five orchestrator passes split across two bodies,
// a single research pass, and a single fix-pass-on-warm-box pass.
func TestComposeReconcilesAgainstAssemble(t *testing.T) {
	reg, base := composeRegistry(t)

	orchestratorEnv := base

	researchEnv := base
	researchEnv.DispatchKind = "research"

	fixPassEnv := base
	fixPassEnv.FixPass = 1

	cases := []struct {
		name string
		env  Env
	}{
		{"legacy", base},
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

func TestComposePassKindDerivation(t *testing.T) {
	reg, base := composeRegistry(t)

	t.Run("fresh work: five passes across two templates", func(t *testing.T) {
		env := base

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

	t.Run("fix pass: single legacy pass on fix-prompt.md", func(t *testing.T) {
		env := base
		env.FixPass = 1
		comp, err := Compose(env, reg, nil)
		if err != nil {
			t.Fatalf("Compose: %v", err)
		}
		if len(comp.Passes) != 1 {
			t.Fatalf("len(Passes) = %d, want 1: %+v", len(comp.Passes), comp.Passes)
		}
		if got, want := comp.Passes[0].Pass, passmachine.KindLegacy.ManifestKind(); got != want {
			t.Errorf("Passes[0].Pass = %q, want %q", got, want)
		}
		if got, want := comp.Passes[0].Template, "fix-prompt.md"; got != want {
			t.Errorf("Passes[0].Template = %q, want %q", got, want)
		}
	})

	t.Run("research: single pass on research-prompt.md", func(t *testing.T) {
		env := base
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

func TestComposeAttribution(t *testing.T) {
	reg, base := composeRegistry(t)

	t.Run("gated-on fragment contributes a fragment source, separator included", func(t *testing.T) {
		// base's CavemanSkillBaked (from coveredEnv) bakes CAVEMAN_BAKED on.
		comp, err := Compose(base, reg, nil)
		if err != nil {
			t.Fatalf("Compose: %v", err)
		}
		want := len(cavemanBody) + len("\n\n")
		got := -1
		for _, s := range comp.Passes[0].Sources {
			if s.Kind == SourceFragment && s.Name == "caveman.md" {
				got = s.Bytes
			}
		}
		if got != want {
			t.Errorf("caveman.md fragment source Bytes = %d, want %d", got, want)
		}
	})

	t.Run("gated-off fragment contributes no source", func(t *testing.T) {
		// base's TDDSkillBaked=true (from coveredEnv) leaves TDD_UNBAKED off.
		comp, err := Compose(base, reg, nil)
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
		env := base
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

	t.Run("inline, shared-axis and legacy rows attribute to the file actually read", func(t *testing.T) {
		comp, err := Compose(base, reg, nil)
		if err != nil {
			t.Fatalf("Compose: %v", err)
		}
		got := map[Source]int{}
		for _, s := range comp.Passes[0].Sources {
			got[s.Source] = s.Bytes
		}
		// forge-step.md is read from the root (Shared), so its Source names the
		// root file, not github/forge-step.md; the interpolated display name is a
		// var source. The Inline row omits the "\n\n" separator, and its bytes stay
		// attributed to inline.md even where the parent splices them.
		want := map[Source]int{
			{Kind: SourceFragment, Name: "inline.md"}:     len("the tracker"),
			{Kind: SourceFragment, Name: "parent.md"}:     len("see  now") + len("\n\n"),
			{Kind: SourceFragment, Name: "forge-step.md"}: len("land via ") + len("\n\n"),
			{Kind: SourceVar, Name: "FORGE_DISPLAY_NAME"}: len("GitHub"),
			{Kind: SourceFragment, Name: "old.md"}:        len("legacy body") + len("\n\n"),
		}
		for src, bytes := range want {
			if got[src] != bytes {
				t.Errorf("Source %+v Bytes = %d, want %d (all: %+v)", src, got[src], bytes, got)
			}
		}
	})

	t.Run("substitution variable contributes a var source", func(t *testing.T) {
		env := base // IssueNumber "2349" is non-empty.
		comp, err := Compose(env, reg, nil)
		if err != nil {
			t.Fatalf("Compose: %v", err)
		}
		found := false
		for _, s := range comp.Passes[0].Sources {
			if s.Kind == SourceVar && s.Name == "ISSUE_NUMBER" {
				found = true
				// ${ISSUE_NUMBER} appears twice in the implement pass body, so the
				// total is a multiple of one occurrence's length, not that length.
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

func TestComposeCarriedTextTargeting(t *testing.T) {
	reg, base := composeRegistry(t)
	env := base

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

func TestDiffPasses(t *testing.T) {
	reg, base := composeRegistry(t)
	env := base

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

// The PassComposition values are hand-built rather than taken from Compose, so
// each pairwise byte-count relationship (a heavier, b heavier, only-one-side,
// zero-byte) stays pinned whatever Compose produces for the real templates.
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

// Pins the two collisions the old code let through: a carried block named the
// same as a substituted scalar var (now a different SourceKind by construction,
// issue #3444), and two carried blocks sharing one name, which fold through
// sourceAggregator instead of appending two rows.
func TestComposeCarriedNoDuplicateSources(t *testing.T) {
	reg, base := composeRegistry(t)
	env := base

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

// Pins the non-blocking finding at composition.go:106: a carried block naming a
// pass kind this cell does not render must error and name both the block and
// the pass, rather than silently vanishing from every pass.
func TestComposeUnknownCarriedPassErrors(t *testing.T) {
	reg, base := composeRegistry(t)
	env := base

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

// Extends TestComposeReconcilesAgainstAssemble's reconciliation shape to issue
// #3445: an Env carrying IssueText, so the base and review body's ISSUE_TEXT
// segment reconcile too, with Remainder staying 0.
func TestComposeReconcilesWithIssueTextSection(t *testing.T) {
	reg, base := composeRegistry(t)

	baseEnv := base
	baseEnv.IssueText = "issue body text"

	orchestratorEnv := baseEnv

	cases := []struct {
		name string
		env  Env
	}{
		{"legacy", baseEnv},
		{"orchestrator", orchestratorEnv},
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
				foundIssueText := false
				for _, s := range pass.Sources {
					sum += s.Bytes
					if s.Kind == SourceVar && s.Name == "ISSUE_TEXT" {
						foundIssueText = true
					}
				}
				if !foundIssueText {
					t.Errorf("pass %q Sources missing the ISSUE_TEXT var source", pass.Pass)
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

// Passes must name exactly the passes Compose reports, in the same order.
func TestPassesMatchesCompose(t *testing.T) {
	reg, base := composeRegistry(t)
	legacy := base
	legacy.FixPass = 1
	research := base
	research.DispatchKind = "research"

	for name, env := range map[string]Env{"orchestrator": base, "legacy": legacy, "research": research} {
		t.Run(name, func(t *testing.T) {
			comp, err := Compose(env, reg, nil)
			if err != nil {
				t.Fatalf("Compose: %v", err)
			}
			got, err := Passes(env, reg)
			if err != nil {
				t.Fatalf("Passes: %v", err)
			}
			var want []string
			for _, p := range comp.Passes {
				want = append(want, p.Pass)
			}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("Passes = %v, want %v", got, want)
			}
		})
	}
}
