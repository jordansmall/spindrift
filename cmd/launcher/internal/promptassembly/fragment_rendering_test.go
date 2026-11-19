package promptassembly

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

const (
	fragFilerRoster = `{"filer":{"description":"filer","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]}}`
	fragPromptFiles = `{"filer":"filer-prompt.md"}`
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

// A git Code Forge has no PR and no outbox relay (issue #5233).
func fragGitForge(e *Env) {
	e.CodeForge = "git"
	e.OutboxRelayCapable = false
}

// jira reads its own axis and borrows github's write axis, but has no in-Box client.
func fragJiraTracker(e *Env) { e.IssueTracker = "jira" }

func fragLocalTracker(e *Env) {
	e.IssueTracker = "local"
	e.TrackerAxisRead = "LOCAL"
	e.TrackerAxisWrite = ""
}

func fragResearch(e *Env) { e.DispatchKind = "research" }

func fragSocket(e *Env) { e.SignalCarrier = "socket" }

// fragIssueRead and fragIssueSearch stand for the host forwarding
// BOX_SIGNAL_ISSUE_READ / BOX_SIGNAL_ISSUE_SEARCH (issue #6924).
func fragIssueRead(e *Env)   { e.SignalIssueRead = true }
func fragIssueSearch(e *Env) { e.SignalIssueSearch = true }

func fragWorker(e *Env) { e.WorkerProvisioned = true }

func fragFiler(e *Env) {
	e.FilerEnabled = true
	e.AgentsJSONTemplate = fragFilerRoster
	e.AgentsPromptFiles = fragPromptFiles
}

// gatedRow is a synthetic registry row: the gate under test and the var its
// stub body binds. Gate names mirror lib/fragments.nix; the bodies are
// markers, so the table pins which row each environment selects and nothing
// of what the production fragments say (that is the clause contracts' job).
type gatedRow struct{ gate, v string }

// selectionCase names the vars whose rows must render under the mutations;
// every other row of the family must stay out.
type selectionCase struct {
	name   string
	mutate []func(*Env)
	want   []string
}

func (r gatedRow) marker() string { return "[" + r.v + "]" }

// selectionTarget is the rendered text a selection case inspects.
type selectionTarget int

const (
	targetPrompt selectionTarget = iota // Result.Prompt
	targetFiler                         // the filer agent's prompt in the agents JSON
	targetReview                        // Result.ReviewPromptText
)

// assertSelection assembles the rows against each case's environment and
// checks the exact set of rendered markers in the target's text.
func assertSelection(t *testing.T, rows []gatedRow, target selectionTarget, cases []selectionCase) {
	t.Helper()
	var fragRows []FragmentRow
	files := map[string]string{}
	var tmpl strings.Builder
	for _, r := range rows {
		fragRows = append(fragRows, FragmentRow{Gate: r.gate, Fragment: r.v + ".md", Var: r.v})
		files["fragments/"+r.v+".md"] = r.marker()
		tmpl.WriteString("${" + r.v + "}")
	}
	for _, f := range []string{"issue-prompt.md", "research-prompt.md", "filer-prompt.md", "review-prompt.md"} {
		files[f] = tmpl.String() + "\n"
	}
	reg, env0 := syntheticRegistry(t, fragRows, files)

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := env0
			for _, m := range tc.mutate {
				m(&env)
			}
			if target == targetFiler {
				fragFiler(&env)
			}
			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			prompt := result.Prompt
			switch target {
			case targetFiler:
				prompt = agentPromptFromJSON(t, result.AgentsJSON, "filer")
			case targetReview:
				prompt = result.ReviewPromptText
			}
			for _, r := range rows {
				want := slices.Contains(tc.want, r.v)
				if got := strings.Contains(prompt, r.marker()); got != want {
					t.Errorf("%s (%s) rendered = %v, want %v:\n%s", r.v, r.gate, got, want, prompt)
				}
			}
			for _, w := range tc.want {
				if !slices.ContainsFunc(rows, func(r gatedRow) bool { return r.v == w }) {
					t.Fatalf("case wants %q, which is no row of the family", w)
				}
			}
		})
	}
}

func fragLocalRef(e *Env) { e.LocalIssueReference = true }

// The PR body references its issue per tracker: github closes it, a local
// tracker names no issue unless the Consumer opted in to a breadcrumb.
func TestAssemblePRBodyReferenceSelection(t *testing.T) {
	assertSelection(t, []gatedRow{
		{"PR_BODY_CLOSES", "PRB_CLOSES"},
		{"PR_BODY_LOCAL_REF", "PRB_LOCAL_REF"},
		{"PR_BODY_LOCAL_NOREF", "PRB_LOCAL_NOREF"},
	}, targetPrompt, []selectionCase{
		{"github closes", nil, []string{"PRB_CLOSES"}},
		{"local default names no issue", []func(*Env){fragLocalTracker}, []string{"PRB_LOCAL_NOREF"}},
		{"local opt-in names the breadcrumb", []func(*Env){fragLocalTracker, fragLocalRef}, []string{"PRB_LOCAL_REF"}},
	})
}

// The issue-read step follows the tracker read axis and the carrier: the
// Signal socket's issue verb reads linked issues on github, forgejo and jira
// alike (issue #6894); without a socket every tracker but local renders the
// unreadable arm.
func TestAssembleIssueReadSelection(t *testing.T) {
	rows := []gatedRow{
		{"ISSUE_TRACKER_GITHUB_LINKS_READABLE", "IR_GITHUB"},
		{"ISSUE_TRACKER_GITHUB_LINKS_UNREADABLE", "IR_GITHUB_LOG"},
		{"ISSUE_TRACKER_LOCAL", "IR_LOCAL"},
		{"ISSUE_TRACKER_FORGEJO_LINKS_READABLE", "IR_FORGEJO"},
		{"ISSUE_TRACKER_FORGEJO_LINKS_UNREADABLE", "IR_FORGEJO_LOG"},
		{"ISSUE_TRACKER_LINKS_UNREADABLE", "IR_UNREADABLE"},
	}
	for _, kind := range []struct {
		name   string
		mutate []func(*Env)
	}{{"work", nil}, {"research", []func(*Env){fragResearch}}} {
		t.Run(kind.name, func(t *testing.T) {
			with := func(fs ...func(*Env)) []func(*Env) { return append(fs, kind.mutate...) }
			assertSelection(t, rows, targetPrompt, []selectionCase{
				{"github", with(fragSocket, fragIssueRead), []string{"IR_GITHUB"}},
				{"local", with(fragSocket, fragLocalTracker), []string{"IR_LOCAL"}},
				{"forgejo", with(fragSocket, fragIssueRead, fragForgejoTracker), []string{"IR_FORGEJO"}},
				{"jira", with(fragSocket, fragIssueRead, fragJiraTracker), []string{"IR_GITHUB"}},
				{"github log", with(), []string{"IR_GITHUB_LOG", "IR_UNREADABLE"}},
				{"local log", with(fragLocalTracker), []string{"IR_LOCAL"}},
				{"forgejo log", with(fragForgejoTracker), []string{"IR_FORGEJO_LOG", "IR_UNREADABLE"}},
				{"jira log", with(fragJiraTracker), []string{"IR_GITHUB_LOG", "IR_UNREADABLE"}},
			})
		})
	}
}

// The research verdict is relayed over the log or socket carrier on every
// tracker; the tracker picks only the folder file.
func TestAssembleResearchVerdictSelection(t *testing.T) {
	assertSelection(t, []gatedRow{
		{"ISSUE_TRACKER_GITHUB_READONLY_LOG", "RV_GITHUB_RO_LOG"},
		{"ISSUE_TRACKER_GITHUB_READONLY_SOCKET", "RV_GITHUB_RO_SOCKET"},
		{"ISSUE_TRACKER_FORGEJO_READONLY_LOG", "RV_FORGEJO_RO_LOG"},
		{"ISSUE_TRACKER_FORGEJO_READONLY_SOCKET", "RV_FORGEJO_RO_SOCKET"},
		{"ISSUE_TRACKER_LOCAL_LOG", "RV_LOCAL_LOG"},
		{"ISSUE_TRACKER_LOCAL_SOCKET", "RV_LOCAL_SOCKET"},
	}, targetPrompt, []selectionCase{
		{"github", []func(*Env){fragResearch}, []string{"RV_GITHUB_RO_LOG"}},
		{"github socket", []func(*Env){fragResearch, fragSocket}, []string{"RV_GITHUB_RO_SOCKET"}},
		{"forgejo", []func(*Env){fragResearch, fragForgejoTracker}, []string{"RV_FORGEJO_RO_LOG"}},
		{"forgejo socket", []func(*Env){fragResearch, fragForgejoTracker, fragSocket}, []string{"RV_FORGEJO_RO_SOCKET"}},
		{"local", []func(*Env){fragResearch, fragLocalTracker}, []string{"RV_LOCAL_LOG"}},
		{"local socket", []func(*Env){fragResearch, fragLocalTracker, fragSocket}, []string{"RV_LOCAL_SOCKET"}},
		{"jira", []func(*Env){fragResearch, fragJiraTracker}, []string{"RV_GITHUB_RO_LOG"}},
		{"jira with the Filer", []func(*Env){fragResearch, fragJiraTracker, fragFiler}, []string{"RV_GITHUB_RO_LOG"}},
	})
}

// The research OUTCOME step reports the verdict's landing as none: the Box
// never posts the comment, and the carrier picks whether the relay is
// acknowledged.
func TestAssembleResearchOutcomeLandingSelection(t *testing.T) {
	rows := []gatedRow{
		{"RESEARCH_VERDICT_RELAYED_LOG", "OL_RELAYED_LOG"},
		{"RESEARCH_VERDICT_RELAYED_SOCKET", "OL_RELAYED_SOCKET"},
	}
	assertSelection(t, rows, targetPrompt, []selectionCase{
		{"github", []func(*Env){fragResearch}, []string{"OL_RELAYED_LOG"}},
		{"github with the Filer", []func(*Env){fragResearch, fragFiler}, []string{"OL_RELAYED_LOG"}},
		{"github with the Filer on the socket", []func(*Env){fragResearch, fragFiler, fragSocket}, []string{"OL_RELAYED_SOCKET"}},
		{"local", []func(*Env){fragResearch, fragLocalTracker}, []string{"OL_RELAYED_LOG"}},
		{"jira", []func(*Env){fragResearch, fragJiraTracker}, []string{"OL_RELAYED_LOG"}},
		{"jira with the Filer", []func(*Env){fragResearch, fragJiraTracker, fragFiler}, []string{"OL_RELAYED_LOG"}},
		{"forgejo", []func(*Env){fragResearch, fragForgejoTracker}, []string{"OL_RELAYED_LOG"}},
	})
}

// The blocked-run issue comment is never posted by the Box: the launcher
// posts it from the outcome line's note=, and the tracker picks the wording.
func TestAssembleBlockedCommentSelection(t *testing.T) {
	assertSelection(t, []gatedRow{
		{"ISSUE_TRACKER_GITHUB_READONLY", "BC_GITHUB_RO"},
		{"ISSUE_TRACKER_FORGEJO_READONLY", "BC_FORGEJO_RO"},
		{"ISSUE_TRACKER_LOCAL", "BC_LOCAL"},
	}, targetPrompt, []selectionCase{
		{"github", nil, []string{"BC_GITHUB_RO"}},
		{"local", []func(*Env){fragLocalTracker}, []string{"BC_LOCAL"}},
		{"jira", []func(*Env){fragJiraTracker}, []string{"BC_GITHUB_RO"}},
		{"forgejo", []func(*Env){fragForgejoTracker}, []string{"BC_FORGEJO_RO"}},
	})
}

// Landing the branch forks on the carrier and the push route: the Box hands
// the branch to the harness (host-mediated) or the outbox, emits a PR-intent
// line over the carrier, and reports the branch on a forge with no PRs. It
// never forks on the forge's client.
func TestAssembleLandingSelection(t *testing.T) {
	assertSelection(t, []gatedRow{
		{"BOX_ACCESS_READ_ONLY_LOG", "OPEN_CREATE_RO_LOG"},
		{"BOX_ACCESS_READ_ONLY_SOCKET", "OPEN_CREATE_RO_SOCKET"},
		{"OUTBOX_PUSH_READ_ONLY", "COMMIT_PUSH_RO"},
		{"OUTBOX_PUSH_READ_ONLY", "BLOCKED_TRIAGE_RO"},
		{"OUTBOX_PUSH_READ_ONLY", "BLOCKED_PUSH_RO"},
		{"NO_PR_FORGE", "BLOCKED_PR_NONE"},
		{"PR_FORGE_READ_ONLY_LOG", "BLOCKED_PR_RO_LOG"},
		{"PR_FORGE_READ_ONLY_SOCKET", "BLOCKED_PR_RO_SOCKET"},
		{"NO_PR_FORGE", "BLOCKED_LANDING_BRANCH"},
		{"PR_FORGE_READ_ONLY", "BLOCKED_LANDING_RO"},
	}, targetPrompt, []selectionCase{
		{"github", nil, []string{
			"OPEN_CREATE_RO_LOG", "COMMIT_PUSH_RO", "BLOCKED_TRIAGE_RO", "BLOCKED_PUSH_RO",
			"BLOCKED_PR_RO_LOG", "BLOCKED_LANDING_RO",
		}},
		{"github socket", []func(*Env){fragSocket}, []string{
			"OPEN_CREATE_RO_SOCKET", "COMMIT_PUSH_RO", "BLOCKED_TRIAGE_RO", "BLOCKED_PUSH_RO",
			"BLOCKED_PR_RO_SOCKET", "BLOCKED_LANDING_RO",
		}},
		{"forgejo stays forge-agnostic", []func(*Env){fragForgejoTracker, fragForgejoForge}, []string{
			"OPEN_CREATE_RO_LOG", "COMMIT_PUSH_RO", "BLOCKED_TRIAGE_RO", "BLOCKED_PUSH_RO",
			"BLOCKED_PR_RO_LOG", "BLOCKED_LANDING_RO",
		}},
	})
}

// A git Code Forge has no PR, so a blocked run opens none and reports the
// branch (issue #5233).
func TestAssembleGitForgeBlockedSelection(t *testing.T) {
	assertSelection(t, []gatedRow{
		{"NO_PR_FORGE", "BLOCKED_PR_NONE"},
		{"NO_PR_FORGE", "BLOCKED_LANDING_BRANCH"},
	}, targetPrompt, []selectionCase{
		{"git forge", []func(*Env){fragGitForge}, []string{"BLOCKED_PR_NONE", "BLOCKED_LANDING_BRANCH"}},
	})
}

// The Filer's own prompt files through the host-relayed intent on every
// tracker. Its dedup follows whether a search reaches the Box: the socket's
// issue-search verb on github and forgejo, local's own folder search; jira and
// every tracker under the log carrier run no search at all (issues #5233,
// #6895).
func TestAssembleFilerPromptSelection(t *testing.T) {
	assertSelection(t, []gatedRow{
		{"FILER_FILE_RELAY_LOG", "FILE_RELAY_LOG"},
		{"FILER_FILE_RELAY_SOCKET", "FILE_RELAY_SOCKET"},
		{"ISSUE_TRACKER_GITHUB_SEARCHABLE", "DEDUP_GITHUB"},
		{"ISSUE_TRACKER_FORGEJO_SEARCHABLE", "DEDUP_FORGEJO"},
		{"ISSUE_TRACKER_LOCAL", "DEDUP_LOCAL"},
		{"ISSUE_TRACKER_UNSEARCHABLE", "DEDUP_NONE"},
	}, targetFiler, []selectionCase{
		{"github socket", []func(*Env){fragSocket, fragIssueSearch}, []string{"FILE_RELAY_SOCKET", "DEDUP_GITHUB"}},
		{"github log", nil, []string{"FILE_RELAY_LOG", "DEDUP_NONE"}},
		{"forgejo socket", []func(*Env){fragForgejoTracker, fragSocket, fragIssueSearch}, []string{"FILE_RELAY_SOCKET", "DEDUP_FORGEJO"}},
		{"forgejo log", []func(*Env){fragForgejoTracker}, []string{"FILE_RELAY_LOG", "DEDUP_NONE"}},
		{"jira", []func(*Env){fragJiraTracker}, []string{"FILE_RELAY_LOG", "DEDUP_NONE"}},
		{"jira socket", []func(*Env){fragJiraTracker, fragSocket}, []string{"FILE_RELAY_SOCKET", "DEDUP_NONE"}},
		{"local", []func(*Env){fragLocalTracker}, []string{"FILE_RELAY_LOG", "DEDUP_LOCAL"}},
	})
}

// The relay's stated reason follows the tracker: a tracker with no in-Box
// client never claims a read-only token, while a writable one still does, on
// both signal carriers (issue #6354).
func TestAssembleFilerRelayReasonSelection(t *testing.T) {
	for _, target := range []struct {
		name string
		to   selectionTarget
	}{{"prompt", targetPrompt}, {"filer", targetFiler}} {
		t.Run(target.name, func(t *testing.T) {
			assertSelection(t, []gatedRow{
				{"ISSUE_TRACKER_WRITABLE", "FRR_READONLY"},
				{"ISSUE_TRACKER_UNWRITABLE", "FRR_UNWRITABLE"},
			}, target.to, []selectionCase{
				{"jira log", []func(*Env){fragJiraTracker}, []string{"FRR_UNWRITABLE"}},
				{"jira socket", []func(*Env){fragJiraTracker, fragSocket}, []string{"FRR_UNWRITABLE"}},
				{"github log", nil, []string{"FRR_READONLY"}},
				{"github socket", []func(*Env){fragSocket}, []string{"FRR_READONLY"}},
			})
		})
	}
}

// The work prompt's FILE ISSUES step queues the findings through the relay.
func TestAssembleFileIssuesStepSelection(t *testing.T) {
	assertSelection(t, []gatedRow{
		{"FILER_FILE_RELAY_LOG", "FILE_ISSUES_RELAY_LOG"},
		{"FILER_FILE_RELAY_SOCKET", "FILE_ISSUES_RELAY_SOCKET"},
	}, targetPrompt, []selectionCase{
		{"log", []func(*Env){fragFiler}, []string{"FILE_ISSUES_RELAY_LOG"}},
		{"socket", []func(*Env){fragFiler, fragSocket}, []string{"FILE_ISSUES_RELAY_SOCKET"}},
		{"filer off", nil, nil},
	})
}

// The knob-gated steps (AUTO-FORMAT, AUTO-LINT) render only under their own
// gate, and a fragment the PromptsDir supplies for an enabled knob is the one
// that renders (a Consumer's override replaces the default text).
func TestAssembleKnobStepSelection(t *testing.T) {
	format := func(e *Env) { e.AutoFormat = true }
	lint := func(e *Env) { e.AutoLint = true }
	assertSelection(t, []gatedRow{
		{"AUTO_FORMAT", "AUTO_FORMAT_ROW"},
		{"AUTO_LINT", "AUTO_LINT_ROW"},
	}, targetPrompt, []selectionCase{
		{"both off", nil, nil},
		{"format", []func(*Env){format}, []string{"AUTO_FORMAT_ROW"}},
		{"lint", []func(*Env){lint}, []string{"AUTO_LINT_ROW"}},
		{"both", []func(*Env){format, lint}, []string{"AUTO_FORMAT_ROW", "AUTO_LINT_ROW"}},
	})
}

// Baking a skill subtracts the inline fallback instead of adding to it, so
// each pair renders exactly one arm (the tdd and commit steps in the work
// prompt, the code-review mode in the reviewer prompt).
func TestAssembleSkillBakedPairSelection(t *testing.T) {
	tddOff := func(e *Env) { e.TDDSkillBaked = false }
	commitOff := func(e *Env) { e.CommitSkillBaked = false }
	assertSelection(t, []gatedRow{
		{"TDD_BAKED", "TDD_BAKED_ROW"},
		{"TDD_UNBAKED", "TDD_UNBAKED_ROW"},
		{"COMMIT_BAKED", "COMMIT_BAKED_ROW"},
		{"COMMIT_UNBAKED", "COMMIT_UNBAKED_ROW"},
	}, targetPrompt, []selectionCase{
		{"all baked", nil, []string{"TDD_BAKED_ROW", "COMMIT_BAKED_ROW"}},
		{"tdd unbaked", []func(*Env){tddOff}, []string{"TDD_UNBAKED_ROW", "COMMIT_BAKED_ROW"}},
		{"commit unbaked", []func(*Env){commitOff}, []string{"TDD_BAKED_ROW", "COMMIT_UNBAKED_ROW"}},
		{"both unbaked", []func(*Env){tddOff, commitOff}, []string{"TDD_UNBAKED_ROW", "COMMIT_UNBAKED_ROW"}},
	})

	assertSelection(t, []gatedRow{
		{"CODE_REVIEW_BAKED", "CODE_REVIEW_BAKED_ROW"},
		{"CODE_REVIEW_UNBAKED", "CODE_REVIEW_UNBAKED_ROW"},
	}, targetReview, []selectionCase{
		{"baked", nil, []string{"CODE_REVIEW_BAKED_ROW"}},
		{"unbaked", []func(*Env){func(e *Env) { e.CodeReviewSkillBaked = false }}, []string{"CODE_REVIEW_UNBAKED_ROW"}},
	})
}

// The Filer decides which twin of a step carries the FILE ISSUES mentions, so
// a Filer-less prompt names no step it does not render (issue #5300); a
// provisioned worker alone turns IMPLEMENT into the coordinator step.
func TestAssembleFilerAndWorkerStepSelection(t *testing.T) {
	assertSelection(t, []gatedRow{
		{"FILER_ENABLED", "WITH_FILER"},
		{"FILER_ABSENT", "WITHOUT_FILER"},
		{"WORKER_PROVISIONED", "COORDINATOR"},
	}, targetPrompt, []selectionCase{
		{"neither", nil, []string{"WITHOUT_FILER"}},
		{"filer", []func(*Env){fragFiler}, []string{"WITH_FILER"}},
		{"worker", []func(*Env){fragWorker}, []string{"WITHOUT_FILER", "COORDINATOR"}},
	})
}

// Each step var is followed by a blank line however its fragment file ends,
// and an off step leaves nothing behind, so adjacent steps and the text around
// them never glue together (the failure mode when a fragment's trailing blank
// line is dropped). The CI failure step takes the fix-pass template and a
// substituted summary, which must stay on its own lines.
func TestAssembleStepsStaySeparated(t *testing.T) {
	reg, env0 := syntheticRegistry(t, []FragmentRow{
		{Gate: "AUTO_FORMAT", Fragment: "a.md", Var: "STEP_A"},
		{Gate: "AUTO_LINT", Fragment: "b.md", Var: "STEP_B"},
		{Gate: "CI_FAILURE_SUMMARY", Fragment: "c.md", Var: "STEP_C", ExtraSubstVars: []string{"CI_FAILURE_SUMMARY"}},
	}, map[string]string{
		"issue-prompt.md": "head.${STEP_A}${STEP_B}# TAIL\n",
		"fix-prompt.md":   "${STEP_C}# TAIL\n",
		"fragments/a.md":  "step a.\n",
		"fragments/b.md":  "step b.\n\n\n",
		"fragments/c.md":  "below:\n\n${CI_FAILURE_SUMMARY}\n",
	})

	cases := []struct {
		name   string
		mutate func(*Env)
		want   string
	}{
		{"two steps", func(e *Env) { e.AutoFormat, e.AutoLint = true, true }, "head.step a.\n\nstep b.\n\n# TAIL"},
		{"first step only", func(e *Env) { e.AutoFormat = true }, "head.step a.\n\n# TAIL"},
		{"second step only", func(e *Env) { e.AutoLint = true }, "head.step b.\n\n# TAIL"},
		{"no steps", func(e *Env) {}, "head.# TAIL"},
		{"substituted summary", func(e *Env) { e.FixPass, e.CIFailureSummary = 1, "build failed" }, "below:\n\nbuild failed\n\n# TAIL"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := env0
			tc.mutate(&env)
			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			if !strings.Contains(result.Prompt, tc.want) {
				t.Errorf("prompt lacks %q:\n%s", tc.want, result.Prompt)
			}
		})
	}
}

// Stub templates, so each substitution is asserted on its own token instead of
// being buried in the real prompts' text.
func TestAssembleSubstitutesAllowlistedTokensInEveryTemplate(t *testing.T) {
	t.Run("labels in the issue prompt", func(t *testing.T) {
		reg, env := syntheticRegistry(t, nil, map[string]string{
			"issue-prompt.md": "label: ${IN_PROGRESS_LABEL} complete: ${COMPLETE_LABEL}\n",
		})
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
		reg, env := syntheticRegistry(t, nil, map[string]string{
			"scout-prompt.md":  "scout for issue ${ISSUE_NUMBER}\n",
			"review-prompt.md": "review base ${BASE_BRANCH}\n\nVERDICT: APPROVE or BLOCK\n",
		})
		env.AgentsJSONTemplate = rosterReviewerAndScout
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

const rosterReviewerAndScout = `{"reviewer":{"description":"r","model":"opus","prompt":"","tools":["Read"]},"scout":{"description":"s","model":"haiku","prompt":"","tools":["Read"]}}`

// A roster without a filer must not need filer-prompt.md, and the agents JSON
// keeps each agent's read-only tools whitelist.
func TestAssembleAgentsJSONRosterShape(t *testing.T) {
	reg, env0 := syntheticRegistry(t, nil, map[string]string{"scout-prompt.md": "scout stub\n"})

	t.Run("no filer entry and no filer-prompt.md", func(t *testing.T) {
		env := env0
		env.AgentsJSONTemplate = rosterReviewerAndScout
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
		env := env0
		env.AgentsJSONTemplate = rosterScoutOnly
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

// The scout's issue-read step is rendered into the agents JSON, not the
// prompt: under the log carrier, which has no socket for the issue verb, the
// unreadable arm renders instead of the verb arm (issues #6344, #6894).
func TestAssembleScoutIssueReadFollowsCarrier(t *testing.T) {
	reg, env0 := syntheticRegistry(t, []FragmentRow{
		{Gate: "ISSUE_TRACKER_LINKS_REACHABLE", Fragment: "reachable.md", Var: "SCOUT_READ_STEP"},
		{Gate: "ISSUE_TRACKER_LINKS_UNREADABLE", Fragment: "unreadable.md", Var: "SCOUT_READ_STEP"},
	}, map[string]string{
		"scout-prompt.md":         "${SCOUT_READ_STEP}",
		"fragments/reachable.md":  "[REACHABLE]",
		"fragments/unreadable.md": "[UNREADABLE]",
	})
	for _, tc := range []struct {
		tracker, carrier, want, wantNot string
		read                            bool // the host forwarded BOX_SIGNAL_ISSUE_READ
	}{
		{"github", "socket", "[REACHABLE]", "[UNREADABLE]", true},
		{"jira", "socket", "[REACHABLE]", "[UNREADABLE]", true},
		{"github", "socket", "[UNREADABLE]", "[REACHABLE]", false},
		{"local", "socket", "", "[REACHABLE]", false},
		{"github", "log", "[UNREADABLE]", "[REACHABLE]", false},
		{"jira", "log", "[UNREADABLE]", "[REACHABLE]", false},
	} {
		env := env0
		env.IssueTracker = tc.tracker
		env.SignalCarrier = tc.carrier
		env.SignalIssueRead = tc.read
		env.AgentsJSONTemplate = rosterScoutOnly
		env.AgentsPromptFiles = `{"scout":"scout-prompt.md"}`
		res, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble(%s/%s): %v", tc.tracker, tc.carrier, err)
		}
		scout := agentPromptFromJSON(t, res.AgentsJSON, "scout")
		if !strings.Contains(scout, tc.want) || strings.Contains(scout, tc.wantNot) || (tc.want == "" && strings.Contains(scout, "[UNREADABLE]")) {
			t.Errorf("%s/%s scout prompt = %q, want %q and not %s", tc.tracker, tc.carrier, scout, tc.want, tc.wantNot)
		}
	}
}

// Jira reads its own tracker folder and the dedup row is not Shared, so a jira
// Filer with a search verb forwarded needs jira/filer-dedup.md to get a search
// step rather than an empty FILER_DEDUP_STEP (issue #6924). The jira fragment
// is the shipped file, so a missing or search-less one fails here.
func TestAssembleFilerDedupJiraSearchable(t *testing.T) {
	shipped, err := os.ReadFile("../../../../templates/default/prompts/fragments/jira/filer-dedup.md")
	if err != nil {
		t.Fatalf("shipped jira filer-dedup fragment: %v", err)
	}
	jira := func(e *Env) {
		e.IssueTracker, e.TrackerAxisRead, e.TrackerAxisWrite, e.TrackerAxisFiler = "jira", "JIRA", "GITHUB", "GH"
	}
	for _, tc := range []struct {
		name   string
		mutate []func(*Env)
		want   string
	}{
		{"github with search", []func(*Env){fragSocket, fragIssueSearch}, "GH-DEDUP"},
		{"jira with search", []func(*Env){jira, fragSocket, fragIssueSearch}, "driver-exec signal issue-search -state open"},
		{"jira without search", []func(*Env){jira, fragSocket}, "NO-SEARCH"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, env := syntheticRegistry(t,
				[]FragmentRow{
					{Axis: "tracker", Gate: "ISSUE_TRACKER_SEARCHABLE", Fragment: "filer-dedup.md", Var: "DEDUP_STEP"},
					{Gate: "ISSUE_TRACKER_UNSEARCHABLE", Fragment: "filer-dedup-unwritable.md", Var: "DEDUP_STEP"},
				},
				map[string]string{
					"issue-prompt.md":                     "dedup:${DEDUP_STEP}|\n",
					"fragments/github/filer-dedup.md":     "GH-DEDUP",
					"fragments/jira/filer-dedup.md":       string(shipped),
					"fragments/filer-dedup-unwritable.md": "NO-SEARCH",
				})
			for _, m := range tc.mutate {
				m(&env)
			}
			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			if !strings.Contains(result.Prompt, tc.want) {
				t.Errorf("Prompt = %q, want it to contain %q", result.Prompt, tc.want)
			}
			if tc.name == "jira without search" && strings.Contains(result.Prompt, "issue-search") {
				t.Errorf("Prompt = %q, want no search step without the verb", result.Prompt)
			}
		})
	}
}
