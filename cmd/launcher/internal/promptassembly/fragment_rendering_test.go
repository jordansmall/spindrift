package promptassembly

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// promptSection returns the text from the first heading line "from" up to
// (excluding) the next "to" heading (or to the end when "to" is empty), so an assertion on one prompt section
// cannot be satisfied or tripped by the same phrase in a sibling section.
func promptSection(t *testing.T, prompt, from, to string) string {
	t.Helper()
	start := strings.Index(prompt, "\n"+from)
	if start < 0 {
		t.Fatalf("prompt has no %q section:\n%s", from, prompt)
	}
	rest := prompt[start+1:]
	if to == "" {
		return rest
	}
	end := strings.Index(rest, "\n"+to)
	if end < 0 {
		t.Fatalf("prompt has no %q heading after %q:\n%s", to, from, prompt)
	}
	return rest[:end]
}

const (
	fragNonce        = "deadbeefcafe1234"
	fragFilerRoster  = `{"filer":{"description":"filer","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]}}`
	fragWorkerRoster = `{"worker":{"description":"fixture worker description","model":"sonnet","prompt":"","tools":["Read","Bash","Edit","Write","Glob","Grep"]}}`
	fragPromptFiles  = `{"filer":"filer-prompt.md"}`
)

func fragForgejoTracker(e *Env) {
	e.IssueTracker = "forgejo"
	e.TrackerAxisRead = "FORGEJO"
	e.TrackerAxisWrite = "FORGEJO"
	e.TrackerAxisFiler = "FORGEJO"
}

func fragForgejoForge(e *Env) {
	e.CodeForge = "forgejo"
	e.ForgeBackend = "FORGEJO"
}

func fragLocalTracker(e *Env) {
	e.IssueTracker = "local"
	e.TrackerAxisRead = "LOCAL"
	e.TrackerAxisWrite = ""
}

func fragReadOnly(e *Env) { e.BoxWriteEnabled = false }

func fragResearch(e *Env) { e.DispatchKind = "research" }

func fragFiler(e *Env) {
	e.FilerEnabled = true
	e.AgentsJSONTemplate = fragFilerRoster
	e.AgentsPromptFiles = fragPromptFiles
}

// TestAssembleFragmentRendering pins, against the real prompts directory, what
// each gated fragment renders into the work and research prompts and the
// filer's agent prompt, per access/tracker/forge/kind cell. Each case asserts
// both the arm that must render and the sibling arm that must not.
// section, when set, scopes prompt assertions to one "# HEADING" span (from,
// to) because several fragments share phrases across sections.
func TestAssembleFragmentRendering(t *testing.T) {
	reg := loadTestRegistry(t)

	cases := []struct {
		name    string
		mutate  []func(*Env)
		section [2]string
		// prompt assertions (scoped to section when set)
		want, wantNot []string
		// filer agent-prompt assertions
		filerWant, filerWantNot []string
	}{
		{
			name:    "pr-body github keeps Closes",
			want:    []string{"Closes #2349"},
			wantNot: []string{"Local-issue:"},
		},
		{
			name:    "pr-body local default has no reference at all",
			mutate:  []func(*Env){fragLocalTracker},
			wantNot: []string{"Closes #2349", "Local-issue:"},
		},
		{
			name:    "pr-body local opt-in emits a Local-issue breadcrumb, never Closes",
			mutate:  []func(*Env){fragLocalTracker, func(e *Env) { e.LocalIssueReference = true }},
			want:    []string{"Local-issue: 2349"},
			wantNot: []string{"Closes #2349"},
		},
		{
			name:    "issue-read github points at injected text, never fetches it",
			want:    []string{"# ISSUE TEXT section after the template body", "via GitHub"},
			wantNot: []string{"gh issue view", "/issues/2349.md"},
		},
		{
			name:   "issue-read local points at injected text, never gh issue view",
			mutate: []func(*Env){fragLocalTracker},
			want:   []string{"# ISSUE TEXT section after the template body"},
			wantNot: []string{
				"via GitHub", "gh issue view", "read it from the local folder", "/issues",
			},
		},
		{
			name:   "issue-read forgejo selects forgejo guidance, never github",
			mutate: []func(*Env){fragForgejoTracker},
			want:   []string{"# ISSUE TEXT section after the template body", "via Forgejo"},
			wantNot: []string{
				"via GitHub", "fj issue view",
			},
		},
		{
			name:    "issue-read jira selects github guidance, never forgejo or local mount",
			mutate:  []func(*Env){func(e *Env) { e.IssueTracker = "jira" }},
			want:    []string{"via GitHub"},
			wantNot: []string{"via Forgejo", "/issues/2349.md"},
		},
		{
			name:    "research verdict github read-write keeps gh issue comment",
			mutate:  []func(*Env){fragResearch},
			want:    []string{"gh issue comment 2349"},
			wantNot: []string{"SPINDRIFT_COMMENT_BEGIN"},
		},
		{
			name:   "research verdict local relays a nonce-guarded line, never gh issue comment",
			mutate: []func(*Env){fragResearch, fragLocalTracker},
			want:   []string{"SPINDRIFT_COMMENT " + fragNonce},
			// The OUTCOME section names `gh issue comment` to explain
			// github's URL source, so pin the invocation shape.
			wantNot: []string{"SPINDRIFT_COMMENT_BEGIN", "SPINDRIFT_COMMENT_END", "gh issue comment 2349"},
		},
		{
			name:    "research verdict github read-only relays, never gh issue comment",
			mutate:  []func(*Env){fragResearch, fragReadOnly},
			want:    []string{"SPINDRIFT_COMMENT " + fragNonce},
			wantNot: []string{"SPINDRIFT_COMMENT_BEGIN", "SPINDRIFT_COMMENT_END", "gh issue comment 2349"},
		},
		{
			name:   "research verdict forgejo read-write keeps fj issue comment",
			mutate: []func(*Env){fragResearch, fragForgejoTracker},
			want:   []string{"fj issue comment 2349"},
		},
		{
			name:    "research verdict forgejo read-only relays, never fj issue comment",
			mutate:  []func(*Env){fragResearch, fragForgejoTracker, fragReadOnly},
			want:    []string{"SPINDRIFT_COMMENT " + fragNonce},
			wantNot: []string{"fj issue comment 2349"},
		},
		{
			name: "blocked-comment github read-write keeps gh issue comment",
			want: []string{"gh issue comment 2349"},
		},
		{
			name:    "blocked-comment local never runs gh issue comment",
			mutate:  []func(*Env){fragLocalTracker},
			want:    []string{"the launcher posts the SPINDRIFT_OUTCOME"},
			wantNot: []string{"gh issue comment"},
		},
		{
			name:    "blocked-comment github read-only never runs gh issue comment",
			mutate:  []func(*Env){fragReadOnly},
			want:    []string{"the launcher posts it as the issue comment"},
			wantNot: []string{"gh issue comment"},
		},
		{
			name:   "blocked-comment jira read-write keeps gh issue comment",
			mutate: []func(*Env){func(e *Env) { e.IssueTracker = "jira" }},
			want:   []string{"gh issue comment 2349"},
		},
		{
			name:    "blocked-comment jira read-only never runs gh issue comment",
			mutate:  []func(*Env){func(e *Env) { e.IssueTracker = "jira" }, fragReadOnly},
			want:    []string{"the launcher posts it as the issue comment"},
			wantNot: []string{"gh issue comment"},
		},
		{
			name:   "blocked-comment forgejo read-write keeps fj issue comment",
			mutate: []func(*Env){fragForgejoTracker},
			want:   []string{"fj issue comment 2349"},
		},
		{
			name:    "blocked-comment forgejo read-only never runs fj issue comment",
			mutate:  []func(*Env){fragForgejoTracker, fragReadOnly},
			want:    []string{"the launcher posts it as the issue comment"},
			wantNot: []string{"fj issue comment"},
		},
		{
			name:    "open-pr push read-write keeps git push",
			section: [2]string{"# OPEN A PULL REQUEST", "# OUTCOME"},
			want:    []string{"git push --force-with-lease -u origin"},
			wantNot: []string{"seam.bundle"},
		},
		{
			name:    "open-pr push read-only takes no push action, harness lands the branch",
			mutate:  []func(*Env){fragReadOnly},
			section: [2]string{"# OPEN A PULL REQUEST", "# OUTCOME"},
			want:    []string{"harness relays your committed branch out"},
			// The COMMIT section carries the same push string, hence the scope.
			wantNot: []string{"/outbox/seam.bundle", "git push --force-with-lease -u origin"},
		},
		{
			name:    "open-pr create read-write keeps gh pr create",
			want:    []string{"gh pr create --draft"},
			wantNot: []string{"fj pr create", "SPINDRIFT_PR_INTENT_BEGIN"},
		},
		{
			name:    "open-pr create forgejo read-write uses fj pr create, never gh pr create",
			mutate:  []func(*Env){fragForgejoTracker, fragForgejoForge},
			section: [2]string{"# OPEN A PULL REQUEST", "# OUTCOME"},
			// Step 2's own "Do NOT run `gh pr create`" reminder rules out a
			// bare substring check, so anchor on the numbered step line.
			want:    []string{"\n2. `fj pr create"},
			wantNot: []string{"\n2. `gh pr create"},
		},
		{
			name:    "open-pr create forgejo read-only stays forge-agnostic, never fj pr create",
			mutate:  []func(*Env){fragForgejoTracker, fragForgejoForge, fragReadOnly},
			section: [2]string{"# OPEN A PULL REQUEST", "# OUTCOME"},
			want:    []string{"SPINDRIFT_PR_INTENT " + fragNonce},
			wantNot: []string{"fj pr create"},
		},
		{
			name:   "open-pr create read-only emits a nonce-guarded SPINDRIFT_PR_INTENT line",
			mutate: []func(*Env){fragReadOnly},
			want:   []string{"SPINDRIFT_PR_INTENT " + fragNonce},
			// The read-only fragment itself says "do NOT `gh pr create`", so
			// pin the invocation form only the read-write fragment renders.
			wantNot: []string{"SPINDRIFT_PR_INTENT_BEGIN", "SPINDRIFT_PR_INTENT_END", "gh pr create --draft --base"},
		},
		{
			name:    "commit push read-write keeps git push and the retry loop",
			section: [2]string{"# COMMIT", "# REVIEW"},
			want:    []string{"git push --force-with-lease -u origin", "one retry only"},
		},
		{
			name:    "commit push read-only takes no push or retry action",
			mutate:  []func(*Env){fragReadOnly},
			section: [2]string{"# COMMIT", "# REVIEW"},
			want:    []string{"harness relays your committed branch out", "git rebase origin/"},
			wantNot: []string{"git push --force-with-lease -u origin"},
		},
		{
			name:    "if-blocked triage read-write keeps the push-failure triage block",
			section: [2]string{"# IF BLOCKED", ""},
			want:    []string{"Push failure — check the actual cause before reporting it", ".github/workflows/"},
		},
		{
			name:    "if-blocked triage read-only treats a denied push as expected",
			mutate:  []func(*Env){fragReadOnly},
			section: [2]string{"# IF BLOCKED", ""},
			want:    []string{"denied", "`git push` here is expected"},
			wantNot: []string{
				"Push failure — check the actual cause before reporting it",
				"git diff origin/",
				"**Genuine `.github/workflows/` change:**",
			},
		},
		{
			name:    "if-blocked push read-write keeps push-what-you-have",
			section: [2]string{"# IF BLOCKED", ""},
			want:    []string{"Push what you have (or note if even that is impossible)"},
			wantNot: []string{"seam.bundle"},
		},
		{
			name:    "if-blocked push read-only takes no bundle or push action",
			mutate:  []func(*Env){fragReadOnly},
			section: [2]string{"# IF BLOCKED", ""},
			want:    []string{"harness relays your committed branch out"},
			wantNot: []string{"/outbox/seam.bundle", "Push what you have (or note if even that is impossible)"},
		},
		{
			name:    "if-blocked PR read-write keeps gh pr view and create",
			section: [2]string{"# IF BLOCKED", ""},
			want:    []string{"gh pr view --json url"},
			wantNot: []string{"SPINDRIFT_PR_INTENT"},
		},
		{
			name:    "if-blocked PR read-only emits a nonce-guarded SPINDRIFT_PR_INTENT line",
			mutate:  []func(*Env){fragReadOnly},
			section: [2]string{"# IF BLOCKED", ""},
			want:    []string{"SPINDRIFT_PR_INTENT " + fragNonce},
			wantNot: []string{"gh pr view --json url", "gh pr create --draft"},
		},
		{
			name:    "if-blocked outcome line read-write keeps the pr-url placeholder",
			section: [2]string{"# IF BLOCKED", ""},
			want:    []string{"landing=<pr-url> status=blocked"},
		},
		{
			name:    "if-blocked outcome line read-only reports the branch, no pr-url",
			mutate:  []func(*Env){fragReadOnly},
			section: [2]string{"# IF BLOCKED", ""},
			want:    []string{"landing=agent/issue-2349 status=blocked"},
			wantNot: []string{"landing=<pr-url>"},
		},
		{
			name:   "filer write read-write keeps gh issue create",
			mutate: []func(*Env){fragFiler},
			want:   []string{"the filer's returned issue URLs"},
			filerWant: []string{
				"gh issue create", "gh label create",
			},
			filerWantNot: []string{"SPINDRIFT_ISSUE_INTENT " + fragNonce},
		},
		{
			name:    "filer write read-only emits SPINDRIFT_ISSUE_INTENT, never gh issue create",
			mutate:  []func(*Env){fragFiler, fragReadOnly},
			want:    []string{"queued for filing"},
			wantNot: []string{"the filer's returned issue URLs"},
			filerWant: []string{
				"SPINDRIFT_ISSUE_INTENT " + fragNonce,
			},
			filerWantNot: []string{"gh issue create", "gh label create"},
		},
		{
			name:         "filer write forgejo direct speaks fj issue create, never gh",
			mutate:       []func(*Env){fragFiler, fragForgejoTracker},
			filerWant:    []string{"fj issue create"},
			filerWantNot: []string{"gh issue create", "gh label create"},
		},
		{
			name:         "filer write github direct speaks gh issue create, never fj",
			mutate:       []func(*Env){fragFiler},
			filerWant:    []string{"gh issue create"},
			filerWantNot: []string{"fj issue create"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := coveredEnv()
			env.RunNonce = fragNonce
			for _, m := range tc.mutate {
				m(&env)
			}

			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}

			prompt := result.Prompt
			if tc.section[0] != "" {
				prompt = promptSection(t, prompt, tc.section[0], tc.section[1])
			}
			for _, s := range tc.want {
				if !strings.Contains(prompt, s) {
					t.Errorf("prompt missing %q:\n%s", s, prompt)
				}
			}
			for _, s := range tc.wantNot {
				if strings.Contains(prompt, s) {
					t.Errorf("prompt contains %q, want absent:\n%s", s, prompt)
				}
			}
			if len(tc.filerWant)+len(tc.filerWantNot) == 0 {
				return
			}
			filer := agentPromptFromJSON(t, result.AgentsJSON, "filer")
			for _, s := range tc.filerWant {
				if !strings.Contains(filer, s) {
					t.Errorf("filer prompt missing %q:\n%s", s, filer)
				}
			}
			for _, s := range tc.filerWantNot {
				if strings.Contains(filer, s) {
					t.Errorf("filer prompt contains %q, want absent:\n%s", s, filer)
				}
			}
		})
	}
}

// TestAssembleStepRendering covers the knob- and skill-gated steps and the
// seams between adjacent fragments: a step renders only when its gate is on,
// points at its skill instead of explaining it inline, and never glues onto
// the text around it (the failure mode when a fragment's trailing blank line
// is dropped).
func TestAssembleStepRendering(t *testing.T) {
	reg := loadTestRegistry(t)

	cases := []struct {
		name          string
		mutate        []func(*Env)
		section       [2]string
		want, wantNot []string
		// before names two substrings the first of which must precede the second.
		before [2]string
		// review asserts against ReviewPromptText instead of Prompt.
		review bool
	}{
		{
			name:    "FILE ISSUES off",
			wantNot: []string{"# FILE ISSUES"},
		},
		{
			name:   "FILE ISSUES on",
			mutate: []func(*Env){fragFiler},
			want:   []string{"# FILE ISSUES"},
		},
		{
			name:    "AUTO-FORMAT off",
			wantNot: []string{"# AUTO-FORMAT"},
		},
		{
			name:   "AUTO-FORMAT on points at the skill, no inline nix fmt rationale",
			mutate: []func(*Env){func(e *Env) { e.AutoFormat = true }},
			want:   []string{"# AUTO-FORMAT", "`/auto-format`"},
			wantNot: []string{
				"`nix fmt` when the target flake defines a formatter",
				"store-lock permission error",
			},
		},
		{
			name:    "AUTO-LINT off",
			wantNot: []string{"# AUTO-LINT"},
		},
		{
			name:    "AUTO-LINT on points at the skill, no inline linter procedure",
			mutate:  []func(*Env){func(e *Env) { e.AutoLint = true }},
			want:    []string{"# AUTO-LINT", "`/auto-lint`"},
			wantNot: []string{"Apply the linter's safe auto-fix mode"},
		},
		{
			name:    "PR-body step does not glue onto the next paragraph",
			wantNot: []string{"know.The PR opens"},
		},
		{
			name:    "open-pr push step does not glue onto gh pr create, read-write",
			wantNot: []string{"`2. `gh pr create"},
		},
		{
			name:    "open-pr push step does not glue onto gh pr create, read-only",
			mutate:  []func(*Env){fragReadOnly},
			wantNot: []string{"attempt.2. `gh pr create"},
		},
		{
			name:    "AUTO-FORMAT and AUTO-LINT stay separated from each other and COMMIT",
			mutate:  []func(*Env){func(e *Env) { e.AutoFormat, e.AutoLint = true, true }},
			want:    []string{"# AUTO-FORMAT", "# AUTO-LINT", "# COMMIT"},
			wantNot: []string{"changed.# AUTO-LINT", "changed.# COMMIT"},
		},
		{
			name:    "FILE ISSUES stays separated from LAND THE CHANGE",
			mutate:  []func(*Env){fragFiler},
			want:    []string{"# FILE ISSUES", "# LAND THE CHANGE"},
			wantNot: []string{"configured.# LAND THE CHANGE"},
		},
		{
			name:    "CI FAILURE stays separated from CONTEXT on a fix pass",
			mutate:  []func(*Env){func(e *Env) { e.FixPass, e.CIFailureSummary = 1, "build failed" }},
			want:    []string{"build failed", "# CONTEXT"},
			wantNot: []string{"scratch:build failed", "failed# CONTEXT"},
		},
		{
			name:    "CAVEMAN_STEP stays separated from the COMMS body text",
			want:    []string{"# COMMS", "Default to the `/caveman` skill for all narration", "are exempt and stay"},
			wantNot: []string{"message.Your text output"},
		},
		{
			name:    "tdd skill baked renders the anchor alone",
			want:    []string{"Work test-first: run `/tdd` for each slice."},
			wantNot: []string{"RED: write ONE failing test"},
		},
		{
			name:    "tdd skill unbaked renders the inline fallback",
			mutate:  []func(*Env){func(e *Env) { e.TDDSkillBaked = false }},
			want:    []string{"RED: write ONE failing test"},
			wantNot: []string{"Work test-first: run `/tdd` for each slice."},
		},
		{
			name: "commit skill baked renders the commit step",
			want: []string{"Use the `/commit` skill to write every commit message"},
			// commit-unbaked.md's inline format rules are subtracted, not superseded.
			wantNot: []string{"hard-wrapped (subject"},
		},
		{
			name:    "commit skill unbaked renders the inline format rules in place of the commit step",
			mutate:  []func(*Env){func(e *Env) { e.CommitSkillBaked = false }},
			want:    []string{"hard-wrapped (subject"},
			wantNot: []string{"Use the `/commit` skill to write every commit message"},
		},
		{
			name:   "code-review baked reviewer prompt anchors the skill and keeps the hunt dimensions",
			review: true,
			want:   []string{"Run the `/code-review` skill and fold its two-axis", "Hunt every dimension"},
		},
		{
			name:    "code-review absent reviewer prompt carries inline coaching and the VERDICT contract",
			mutate:  []func(*Env){func(e *Env) { e.CodeReviewSkillBaked = false }},
			review:  true,
			want:    []string{"Hunt every dimension", "VERDICT: APPROVE | BLOCK"},
			wantNot: []string{"Run the `/code-review` skill and fold its two-axis"},
		},
		{
			name:    "coordinator delegates slices when a worker is provisioned",
			mutate:  []func(*Env){fragWorker},
			section: [2]string{"# IMPLEMENT", "# CHECK"},
			want:    []string{"coordinator", "delegate each slice"},
			// The coordinator step must not touch the consumer repo's
			// .gitignore, and must not name an unexported $WORK_DIR.
			wantNot: []string{"gitignore", "WORK_DIR"},
		},
		{
			name:    "no worker leaves the single-implementor prompt",
			section: [2]string{"# IMPLEMENT", "# CHECK"},
			want:    []string{"Work test-first"},
			wantNot: []string{"delegate each slice"},
		},
		{
			name:    "coordinator step stays separated from the test-first rule",
			mutate:  []func(*Env){fragWorker, func(e *Env) { e.TDDSkillBaked = false }},
			want:    []string{"coordinator", "Work test-first, one slice"},
			before:  [2]string{"coordinator", "Work test-first, one slice"},
			wantNot: []string{"commits.Work test-first"},
		},
		{
			name:    "REVIEW defers to the code-owned review pass",
			want:    []string{"Review is handled by the orchestrator as a separate"},
			wantNot: []string{"spawn a fresh `reviewer` subagent"},
		},
		{
			name: "scout delegation",
			want: []string{"scout"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := coveredEnv()
			for _, m := range tc.mutate {
				m(&env)
			}
			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			text := result.Prompt
			if tc.review {
				text = result.ReviewPromptText
			}
			if tc.section[0] != "" {
				text = promptSection(t, text, tc.section[0], tc.section[1])
			}
			for _, s := range tc.want {
				if !strings.Contains(text, s) {
					t.Errorf("missing %q:\n%s", s, text)
				}
			}
			if tc.before[0] != "" {
				if a, b := strings.Index(text, tc.before[0]), strings.Index(text, tc.before[1]); a < 0 || b < 0 || a >= b {
					t.Errorf("%q (at %d) must precede %q (at %d)", tc.before[0], a, tc.before[1], b)
				}
			}
			for _, s := range tc.wantNot {
				if strings.Contains(text, s) {
					t.Errorf("contains %q, want absent:\n%s", s, text)
				}
			}
		})
	}
}

func fragWorker(e *Env) {
	e.WorkerProvisioned = true
	e.AgentsJSONTemplate = fragWorkerRoster
	e.AgentsPromptFiles = `{"worker":"worker-prompt.md"}`
}

// A runtime prompt-dir override supplies its own fragment for a knob it
// enables, exactly as it must supply filer-prompt.md when the roster has a
// filer (docs/reference.md).
func TestAssemblePromptDirOverridesGatedFragment(t *testing.T) {
	reg := loadTestRegistry(t)
	cases := []struct {
		name     string
		fragment string
		heading  string
		mutate   func(*Env)
	}{
		{"auto-format", "auto-format.md", "# AUTO-FORMAT", func(e *Env) { e.AutoFormat = true }},
		{"auto-lint", "auto-lint.md", "# AUTO-LINT", func(e *Env) { e.AutoLint = true }},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			dir := promptsDirMissingFragment(t, tc.fragment)
			custom := tc.heading + "\n\nCUSTOM-FRAGMENT-MARKER\n\n"
			if err := os.WriteFile(filepath.Join(dir, "fragments", tc.fragment), []byte(custom), 0o644); err != nil {
				t.Fatal(err)
			}
			env := coveredEnv()
			env.PromptsDir = dir
			tc.mutate(&env)

			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			if !strings.Contains(result.Prompt, "CUSTOM-FRAGMENT-MARKER") {
				t.Errorf("Prompt missing the override's fragment text:\n%s", result.Prompt)
			}
		})
	}
}

// Stub templates, so each substitution is asserted on its own token instead of
// being buried in the real prompts' text.
func TestAssembleSubstitutesAllowlistedTokensInEveryTemplate(t *testing.T) {
	reg := loadTestRegistry(t)

	write := func(dir, name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stubDir := func(t *testing.T) string {
		t.Helper()
		dir, _ := promptsDirExceptSubdir(t, "no-such-entry")
		// promptsDirExceptSubdir symlinks into the real tree; replace the
		// templates under test with files, not through the symlinks.
		for _, name := range []string{"issue-prompt.md", "scout-prompt.md", "review-prompt.md"} {
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}

	t.Run("labels in the issue prompt", func(t *testing.T) {
		dir := stubDir(t)
		write(dir, "issue-prompt.md", "label: ${IN_PROGRESS_LABEL} complete: ${COMPLETE_LABEL}\n")
		write(dir, "scout-prompt.md", "scout stub\n")
		write(dir, "review-prompt.md", "reviewer stub\n\nVERDICT: APPROVE or BLOCK\n")
		env := coveredEnv()
		env.PromptsDir = dir
		env.InProgressLabel, env.CompleteLabel = "wip", "done"

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}
		for _, want := range []string{"label: wip", "complete: done"} {
			if !strings.Contains(result.Prompt, want) {
				t.Errorf("Prompt missing %q:\n%s", want, result.Prompt)
			}
		}
	})

	t.Run("scout and review prompt files", func(t *testing.T) {
		dir := stubDir(t)
		write(dir, "issue-prompt.md", "issue stub\n")
		write(dir, "scout-prompt.md", "scout for issue ${ISSUE_NUMBER}\n")
		write(dir, "review-prompt.md", "review base ${BASE_BRANCH}\n\nVERDICT: APPROVE or BLOCK\n")
		env := coveredEnv()
		env.PromptsDir = dir
		env.AgentsJSONTemplate = `{"reviewer":{"description":"r","model":"opus","prompt":"","tools":["Read"]},"scout":{"description":"s","model":"haiku","prompt":"","tools":["Read"]}}`
		env.AgentsPromptFiles = `{"scout":"scout-prompt.md","reviewer":"review-prompt.md"}`

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}
		if got := agentPromptFromJSON(t, result.AgentsJSON, "scout"); !strings.Contains(got, "scout for issue 2349") {
			t.Errorf("scout prompt = %q, want ${ISSUE_NUMBER} substituted", got)
		}
		if !strings.Contains(result.ReviewPromptText, "review base main") {
			t.Errorf("ReviewPromptText = %q, want ${BASE_BRANCH} substituted", result.ReviewPromptText)
		}
	})
}

// A roster without a filer must not need filer-prompt.md, and the agents JSON
// keeps each agent's read-only tools whitelist.
func TestAssembleAgentsJSONRosterShape(t *testing.T) {
	reg := loadTestRegistry(t)

	t.Run("no filer entry and no filer-prompt.md", func(t *testing.T) {
		dir, _ := promptsDirExceptSubdir(t, "filer-prompt.md")
		env := coveredEnv()
		env.PromptsDir = dir
		env.AgentsJSONTemplate = `{"reviewer":{"description":"r","model":"opus","prompt":"","tools":["Read"]},"scout":{"description":"s","model":"haiku","prompt":"","tools":["Read"]}}`
		env.AgentsPromptFiles = `{"scout":"scout-prompt.md","reviewer":"review-prompt.md"}`

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v, want nil without filer-prompt.md", err)
		}
		var agents map[string]json.RawMessage
		if err := json.Unmarshal([]byte(result.AgentsJSON), &agents); err != nil {
			t.Fatalf("unmarshal AgentsJSON: %v", err)
		}
		if _, ok := agents["filer"]; ok {
			t.Errorf("AgentsJSON has a filer entry, want none: %s", result.AgentsJSON)
		}
	})

	t.Run("scout keeps a read-only tools whitelist", func(t *testing.T) {
		env := coveredEnv()
		env.AgentsJSONTemplate = `{"scout":{"description":"fixture scout description","model":"opus","prompt":"","tools":["Read","Bash","WebFetch","WebSearch","Glob","Grep"]}}`
		env.AgentsPromptFiles = `{"scout":"scout-prompt.md"}`

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}
		var agents map[string]struct {
			Tools []string `json:"tools"`
		}
		if err := json.Unmarshal([]byte(result.AgentsJSON), &agents); err != nil {
			t.Fatalf("unmarshal AgentsJSON: %v", err)
		}
		tools := agents["scout"].Tools
		if len(tools) == 0 {
			t.Fatal("scout.tools is empty, want the template's whitelist")
		}
		for _, banned := range []string{"Edit", "Write"} {
			if slices.Contains(tools, banned) {
				t.Errorf("scout.tools = %v, want no %s", tools, banned)
			}
		}
	})
}
