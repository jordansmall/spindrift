package promptassembly

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// varyDispatchFacts changes every per-Dispatch fact an Env carries and leaves
// the harness setup alone.
func varyDispatchFacts(e Env) Env {
	e.IssueNumber = "9001"
	e.IssueTitle = "A wholly different issue"
	e.IssueText = "different body text"
	e.Branch = "agent/issue-9001"
	e.DispatchKey = "work-9001"
	e.RunNonce = "other-nonce"
	if e.CIFailureSummary != "" {
		e.CIFailureSummary = "another failure summary"
	}
	return e
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func mustTemplateHashes(t *testing.T, e Env, reg Registry) map[string]string {
	t.Helper()
	h, err := TemplateHashes(e, reg)
	if err != nil {
		t.Fatalf("TemplateHashes: %v", err)
	}
	return h
}

// hashRegistry is the synthetic cell the template-hash tests share: one
// template per kind referencing the per-Dispatch and setup vars the tests
// move, plus a gated fragment on the issue, fix and butler templates so a gate
// flip has something to render.
// The Chores differ in body so the chosen Chore reaches the hash.
func hashRegistry(t *testing.T) (Registry, Env) {
	t.Helper()
	return syntheticRegistry(t,
		[]FragmentRow{
			{Gate: "FILER_ENABLED", Fragment: "filer.md", Var: "FILER_STEP"},
			{Gate: "CHORE_PATCH_CLASSES", Fragment: "patch.md", Var: "PATCH_STEP"},
			{Gate: "CI_FAILURE_SUMMARY", Fragment: "ci-failure.md", Var: "CI_FAILURE_STEP", ExtraSubstVars: []string{"CI_FAILURE_SUMMARY"}},
		},
		map[string]string{
			"issue-prompt.md":         "issue ${ISSUE_NUMBER} ${ISSUE_TITLE} on ${BRANCH} key ${DISPATCH_KEY} nonce ${RUN_NONCE}\n${FILER_STEP}",
			"fix-prompt.md":           "fix ${ISSUE_NUMBER} on ${BRANCH}\n${CI_FAILURE_STEP}",
			"review-prompt.md":        "review ${ISSUE_NUMBER} ${ISSUE_TITLE}\n",
			"butler-prompt.md":        "butler ${CHORE_NAME} at ${CHORE_HEAD} range ${CHORE_DIFF_RANGE} slice ${CHORE_SLICE} classes ${CHORE_CLASSES} of ${CHORE_CLASS_LIST} max ${CHORE_MAX_FINDINGS} key ${DISPATCH_KEY} nonce ${RUN_NONCE}\n${CHORE_PROMPT}${CHORE_INPUT}\n${PATCH_STEP}",
			"chores/bugs.md":          "bugs chore body\n",
			"chores/refactor.md":      "refactor chore body\n",
			"chores/tuning.md":        "tuning chore body\n",
			"fragments/ci-failure.md": "ci failed: ${CI_FAILURE_SUMMARY}",
			"fragments/filer.md":      "filer step body",
			"fragments/patch.md":      "patch step for ${CHORE_PATCH_CLASSES}",
		})
}

// hashButlerEnv is butlerEnv pointed at base's synthetic PromptsDir.
func hashButlerEnv(base Env) Env {
	e := butlerEnv()
	e.PromptsDir = base.PromptsDir
	return e
}

func TestTemplateHashesIgnorePerDispatchFacts(t *testing.T) {
	reg, base := hashRegistry(t)

	fresh := base
	fresh.IssueText = "original body text"

	fixPass := base
	fixPass.FixPass = 1
	fixPass.CIFailureSummary = "original failure summary"

	cases := []struct {
		name  string
		env   Env
		roles []string
	}{
		{"fresh work", fresh, []string{"delta-review", "fix", "implement", "land", "review"}},
		{"fresh work without issue text", base, []string{"delta-review", "fix", "implement", "land", "review"}},
		{"fix pass", fixPass, []string{"legacy"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := mustTemplateHashes(t, tc.env, reg)
			b := mustTemplateHashes(t, varyDispatchFacts(tc.env), reg)
			if got := sortedKeys(a); !reflect.DeepEqual(got, tc.roles) {
				t.Errorf("roles = %v, want %v", got, tc.roles)
			}
			if !reflect.DeepEqual(a, b) {
				t.Errorf("hashes differ across Dispatches:\n a=%v\n b=%v", a, b)
			}
		})
	}

	// Without this the equal hashes above could be vacuous: the real prompt
	// does carry the per-Dispatch facts.
	r1, err := Assemble(fresh, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	r2, err := Assemble(varyDispatchFacts(fresh), reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if !strings.Contains(r1.Prompt, fresh.IssueTitle) {
		t.Errorf("assembled prompt lacks the issue title %q", fresh.IssueTitle)
	}
	if r1.Prompt == r2.Prompt {
		t.Error("assembled prompts equal across Dispatches; per-Dispatch facts not exercised")
	}
}

func TestTemplateHashesButlerIgnoreChoreFacts(t *testing.T) {
	reg, base := hashRegistry(t)
	a := hashButlerEnv(base)
	a.ChorePatchClasses = "dead-code" // presence gates the patch section, so keep it set on both sides
	b := a
	b.ChoreHead = "0123abcd"
	b.ChoreDiffRange = "aaaa..bbbb"
	b.ChoreSlice = "other/file.go"
	b.ChoreClasses = "typo"
	b.ChorePatchClasses = "typo"
	b.ChoreMaxFindings = "9"
	b.DispatchKey = "butler-other"
	b.RunNonce = "other-nonce"

	ha := mustTemplateHashes(t, a, reg)
	if got, want := sortedKeys(ha), []string{"butler"}; !reflect.DeepEqual(got, want) {
		t.Errorf("roles = %v, want %v", got, want)
	}
	if hb := mustTemplateHashes(t, b, reg); !reflect.DeepEqual(ha, hb) {
		t.Errorf("hashes differ across chore Dispatches:\n a=%v\n b=%v", ha, hb)
	}

	// CHORE_NAME selects the chore prompt: it is setup, so it must move the hash.
	c := a
	c.ChoreName = "refactor"
	if hc := mustTemplateHashes(t, c, reg); reflect.DeepEqual(ha, hc) {
		t.Error("hash unchanged across different chores")
	}
}

func TestTemplateHashesTrackSetup(t *testing.T) {
	reg, off := hashRegistry(t)
	on := off
	on.FilerEnabled = true

	ha := mustTemplateHashes(t, off, reg)
	hb := mustTemplateHashes(t, on, reg)
	if ha["implement"] == hb["implement"] {
		t.Error("implement hash unchanged after enabling the Filer section")
	}

	// Without this the moved hash could be vacuous: the gated step must
	// actually render into the prompt the hash covers.
	for _, tc := range []struct {
		env  Env
		want bool
	}{{off, false}, {on, true}} {
		r, err := Assemble(tc.env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}
		if got := strings.Contains(r.Prompt, "filer step body"); got != tc.want {
			t.Errorf("FilerEnabled=%v: prompt carries the Filer step = %v, want %v", tc.env.FilerEnabled, got, tc.want)
		}
	}
}

// classificationProblems reports every way perDispatch and setup fail to
// partition defined, the substitution var names (fragment Vars excluded).
func classificationProblems(defined map[string]bool, perDispatch, setup []string) []string {
	inList := map[string]int{}
	for _, name := range perDispatch {
		inList[name]++
	}
	for _, name := range setup {
		inList[name]++
	}

	var problems []string
	for name := range defined {
		if inList[name] == 0 {
			problems = append(problems, name+" is in neither perDispatchVars nor setupVars (assemble.go): classify it as a Dispatch fact or harness setup")
		}
	}
	for name, n := range inList {
		if n > 1 {
			problems = append(problems, name+" is listed more than once in perDispatchVars and setupVars combined")
		}
		if !defined[name] {
			problems = append(problems, name+" is listed in perDispatchVars or setupVars but is not a substitution var assemblePromptBodies defines")
		}
	}
	sort.Strings(problems)
	return problems
}

func TestClassificationProblems(t *testing.T) {
	cases := []struct {
		name        string
		defined     []string
		perDispatch []string
		setup       []string
		want        string // var a problem must name; empty means no problems
	}{
		{"clean", []string{"A", "B"}, []string{"A"}, []string{"B"}, ""},
		{"unclassified", []string{"A", "B"}, []string{"A"}, nil, "B"},
		{"in both lists", []string{"A"}, []string{"A"}, []string{"A"}, "A"},
		{"repeated within one list", []string{"A"}, []string{"A", "A"}, nil, "A"},
		{"stale listed name", []string{"A"}, []string{"A"}, []string{"GONE"}, "GONE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defined := map[string]bool{}
			for _, n := range tc.defined {
				defined[n] = true
			}
			problems := classificationProblems(defined, tc.perDispatch, tc.setup)
			if tc.want == "" {
				if len(problems) != 0 {
					t.Fatalf("problems = %v, want none", problems)
				}
				return
			}
			for _, p := range problems {
				if strings.HasPrefix(p, tc.want+" ") {
					return
				}
			}
			t.Fatalf("problems = %v, want one naming %q", problems, tc.want)
		})
	}
}

func TestSubstitutionVarsAreClassified(t *testing.T) {
	reg, base := hashRegistry(t)
	e := hashButlerEnv(base)
	e.CIFailureSummary = "x"
	bodies, err := assemblePromptBodies(e, reg)
	if err != nil {
		t.Fatalf("assemblePromptBodies: %v", err)
	}

	// A registry row's Var is a rendered fragment, not a substitution input.
	fragments := map[string]bool{}
	for _, row := range reg.Rows {
		fragments[row.Var] = true
	}
	defined := map[string]bool{}
	for name := range bodies.vars {
		if !fragments[name] {
			defined[name] = true
		}
	}
	// extraSubstRaw keys count even when no row names them in extraSubstVars.
	for name := range extraSubstRaw(e) {
		defined[name] = true
	}

	for _, p := range classificationProblems(defined, perDispatchVars, setupVars) {
		t.Error(p)
	}
}

// The Chore input section's fixed prose is hashed; the digest it carries is a
// per-Dispatch fact and must not move the hash. A code Chore (no input) hashes
// without the section at all.
func TestTemplateHashesTuningIgnoreChoreInput(t *testing.T) {
	reg, base := hashRegistry(t)
	a := hashButlerEnv(base)
	a.ChoreName = "tuning"
	a.ChoreInput = "# Tuning digest\n\n| Anchor | n |\n|---|---|\n| a1 | 20 |"
	b := a
	b.ChoreInput = "# Tuning digest\n\n| Anchor | n |\n|---|---|\n| z9 | 31 |\n```\nfence break attempt"

	ha := mustTemplateHashes(t, a, reg)
	if hb := mustTemplateHashes(t, b, reg); !reflect.DeepEqual(ha, hb) {
		t.Errorf("hashes differ across digests:\n a=%v\n b=%v", ha, hb)
	}

	bare := a
	bare.ChoreInput = ""
	if hn := mustTemplateHashes(t, bare, reg); reflect.DeepEqual(ha, hn) {
		t.Error("hash unchanged when the Chore input section is absent; its fixed prose is not covered")
	}
}

func TestChoreInputSection(t *testing.T) {
	if got := choreInputSection(Env{}); got != "" {
		t.Errorf("empty ChoreInput rendered %q, want empty", got)
	}
	got := choreInputSection(Env{ChoreInput: "row one\nrow two"})
	for _, want := range []string{"# CHORE INPUT", "row one\nrow two"} {
		if !strings.Contains(got, want) {
			t.Errorf("section lacks %q:\n%s", want, got)
		}
	}
}
