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

// A git Code Forge has no PR and no outbox relay (issue #5233).
func fragGitForge(e *Env) {
	e.CodeForge = "git"
	e.OutboxRelayCapable = false
}

// jira rides github's read and write axes but its row has no in-Box client.
func fragJiraTracker(e *Env) { e.IssueTracker = "jira" }

func fragLocalTracker(e *Env) {
	e.IssueTracker = "local"
	e.TrackerAxisRead = "LOCAL"
	e.TrackerAxisWrite = ""
}

func fragReadOnly(e *Env) { e.BoxWriteEnabled = false }

func fragResearch(e *Env) { e.DispatchKind = "research" }

func fragSocket(e *Env) { e.SignalCarrier = "socket" }

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

// assertSelection assembles the rows against each case's environment and
// checks the exact set of rendered markers. The filer's prompt is read from
// the agent roster instead of the kind's prompt when filer is set.
func assertSelection(t *testing.T, rows []gatedRow, filer bool, cases []selectionCase) {
	t.Helper()
	var fragRows []FragmentRow
	files := map[string]string{}
	var tmpl strings.Builder
	for _, r := range rows {
		fragRows = append(fragRows, FragmentRow{Gate: r.gate, Fragment: r.v + ".md", Var: r.v})
		files["fragments/"+r.v+".md"] = r.marker()
		tmpl.WriteString("${" + r.v + "}")
	}
	for _, f := range []string{"issue-prompt.md", "research-prompt.md", "filer-prompt.md"} {
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
			if filer {
				fragFiler(&env)
			}
			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			prompt := result.Prompt
			if filer {
				prompt = agentPromptFromJSON(t, result.AgentsJSON, "filer")
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

func fragJira(e *Env)     { e.IssueTracker = "jira" }
func fragLocalRef(e *Env) { e.LocalIssueReference = true }

// The PR body references its issue per tracker: github closes it, a local
// tracker names no issue unless the Consumer opted in to a breadcrumb.
func TestAssemblePRBodyReferenceSelection(t *testing.T) {
	assertSelection(t, []gatedRow{
		{"PR_BODY_CLOSES", "PRB_CLOSES"},
		{"PR_BODY_LOCAL_REF", "PRB_LOCAL_REF"},
		{"PR_BODY_LOCAL_NOREF", "PRB_LOCAL_NOREF"},
	}, false, []selectionCase{
		{"github closes", nil, []string{"PRB_CLOSES"}},
		{"local default names no issue", []func(*Env){fragLocalTracker}, []string{"PRB_LOCAL_NOREF"}},
		{"local opt-in names the breadcrumb", []func(*Env){fragLocalTracker, fragLocalRef}, []string{"PRB_LOCAL_REF"}},
	})
}

// The issue-read step follows the tracker read axis; a tracker with no
// in-Box client (jira) gets the unreadable arm in work and research alike
// (issue #5233's jira row rides github's axes).
func TestAssembleIssueReadSelection(t *testing.T) {
	rows := []gatedRow{
		{"ISSUE_TRACKER_GITHUB_LINKS_READABLE", "IR_GITHUB"},
		{"ISSUE_TRACKER_LOCAL", "IR_LOCAL"},
		{"ISSUE_TRACKER_FORGEJO", "IR_FORGEJO"},
		{"ISSUE_TRACKER_LINKS_UNREADABLE", "IR_UNREADABLE"},
	}
	for _, kind := range []struct {
		name   string
		mutate []func(*Env)
	}{{"work", nil}, {"research", []func(*Env){fragResearch}}} {
		t.Run(kind.name, func(t *testing.T) {
			assertSelection(t, rows, false, []selectionCase{
				{"github", kind.mutate, []string{"IR_GITHUB"}},
				{"local", append([]func(*Env){fragLocalTracker}, kind.mutate...), []string{"IR_LOCAL"}},
				{"forgejo", append([]func(*Env){fragForgejoTracker}, kind.mutate...), []string{"IR_FORGEJO"}},
				{"jira", append([]func(*Env){fragJira}, kind.mutate...), []string{"IR_UNREADABLE"}},
			})
		})
	}
}

// The research verdict is posted by the Box only on a writable tracker; every
// read-only or local run relays it over the log or socket carrier.
func TestAssembleResearchVerdictSelection(t *testing.T) {
	assertSelection(t, []gatedRow{
		{"ISSUE_TRACKER_GITHUB_READWRITE", "RV_GITHUB"},
		{"ISSUE_TRACKER_FORGEJO_READWRITE", "RV_FORGEJO"},
		{"ISSUE_TRACKER_GITHUB_READONLY_LOG", "RV_GITHUB_RO_LOG"},
		{"ISSUE_TRACKER_GITHUB_READONLY_SOCKET", "RV_GITHUB_RO_SOCKET"},
		{"ISSUE_TRACKER_FORGEJO_READONLY_LOG", "RV_FORGEJO_RO_LOG"},
		{"ISSUE_TRACKER_FORGEJO_READONLY_SOCKET", "RV_FORGEJO_RO_SOCKET"},
		{"ISSUE_TRACKER_LOCAL_LOG", "RV_LOCAL_LOG"},
		{"ISSUE_TRACKER_LOCAL_SOCKET", "RV_LOCAL_SOCKET"},
	}, false, []selectionCase{
		{"github read-write", []func(*Env){fragResearch}, []string{"RV_GITHUB"}},
		{"github read-only", []func(*Env){fragResearch, fragReadOnly}, []string{"RV_GITHUB_RO_LOG"}},
		{"github read-only socket", []func(*Env){fragResearch, fragReadOnly, fragSocket}, []string{"RV_GITHUB_RO_SOCKET"}},
		{"forgejo read-write", []func(*Env){fragResearch, fragForgejoTracker}, []string{"RV_FORGEJO"}},
		{"forgejo read-only", []func(*Env){fragResearch, fragForgejoTracker, fragReadOnly}, []string{"RV_FORGEJO_RO_LOG"}},
		{"forgejo read-only socket", []func(*Env){fragResearch, fragForgejoTracker, fragReadOnly, fragSocket}, []string{"RV_FORGEJO_RO_SOCKET"}},
		{"local", []func(*Env){fragResearch, fragLocalTracker}, []string{"RV_LOCAL_LOG"}},
		{"local socket", []func(*Env){fragResearch, fragLocalTracker, fragSocket}, []string{"RV_LOCAL_SOCKET"}},
	})
}

// The research OUTCOME step reports the verdict's landing as a posted comment
// URL only where the Box posts it itself; a relayed verdict (read-only, local,
// or any run with the Filer) lands as none, and the carrier picks whether the
// relay is acknowledged.
func TestAssembleResearchOutcomeLandingSelection(t *testing.T) {
	rows := []gatedRow{
		{"ISSUE_TRACKER_GITHUB_READWRITE", "OL_GITHUB"},
		{"ISSUE_TRACKER_FORGEJO_READWRITE", "OL_FORGEJO"},
		{"RESEARCH_VERDICT_RELAYED_LOG", "OL_RELAYED_LOG"},
		{"RESEARCH_VERDICT_RELAYED_SOCKET", "OL_RELAYED_SOCKET"},
	}
	assertSelection(t, rows, false, []selectionCase{
		{"github read-write", []func(*Env){fragResearch}, []string{"OL_GITHUB"}},
		{"github read-write with the Filer", []func(*Env){fragResearch, fragFiler}, []string{"OL_RELAYED_LOG"}},
		{"github read-write with the Filer on the socket", []func(*Env){fragResearch, fragFiler, fragSocket}, []string{"OL_RELAYED_SOCKET"}},
		{"github read-only", []func(*Env){fragResearch, fragReadOnly}, []string{"OL_RELAYED_LOG"}},
		{"local", []func(*Env){fragResearch, fragLocalTracker}, []string{"OL_RELAYED_LOG"}},
		{"forgejo read-write", []func(*Env){fragResearch, fragForgejoTracker}, []string{"OL_FORGEJO"}},
		{"forgejo read-only", []func(*Env){fragResearch, fragForgejoTracker, fragReadOnly}, []string{"OL_RELAYED_LOG"}},
	})
}

// The blocked-run issue comment is posted by the Box only on a writable
// github or forgejo tracker; read-only and local runs let the launcher post
// it, and a tracker with no client (jira read-write) says so instead.
func TestAssembleBlockedCommentSelection(t *testing.T) {
	assertSelection(t, []gatedRow{
		{"ISSUE_TRACKER_GITHUB_READWRITE", "BC_GITHUB"},
		{"ISSUE_TRACKER_FORGEJO_READWRITE", "BC_FORGEJO"},
		{"ISSUE_TRACKER_GITHUB_READONLY", "BC_GITHUB_RO"},
		{"ISSUE_TRACKER_FORGEJO_READONLY", "BC_FORGEJO_RO"},
		{"ISSUE_TRACKER_LOCAL", "BC_LOCAL"},
		{"ISSUE_TRACKER_UNWRITABLE_READWRITE", "BC_UNWRITABLE"},
	}, false, []selectionCase{
		{"github read-write", nil, []string{"BC_GITHUB"}},
		{"github read-only", []func(*Env){fragReadOnly}, []string{"BC_GITHUB_RO"}},
		{"local", []func(*Env){fragLocalTracker}, []string{"BC_LOCAL"}},
		{"jira read-write", []func(*Env){fragJira}, []string{"BC_UNWRITABLE"}},
		{"jira read-only", []func(*Env){fragJira, fragReadOnly}, []string{"BC_GITHUB_RO"}},
		{"forgejo read-write", []func(*Env){fragForgejoTracker}, []string{"BC_FORGEJO"}},
		{"forgejo read-only", []func(*Env){fragForgejoTracker, fragReadOnly}, []string{"BC_FORGEJO_RO"}},
	})
}

// Landing the branch forks on Box access, the forge and the carrier: a
// read-write Box pushes and opens the PR itself with its forge's client, a
// read-only one hands the branch to the harness and emits a PR-intent line,
// and a git forge opens no PR at all.
func TestAssembleLandingSelection(t *testing.T) {
	assertSelection(t, []gatedRow{
		{"BOX_ACCESS_READ_WRITE", "OPEN_PUSH_RW"},
		{"BOX_ACCESS_READ_ONLY", "OPEN_PUSH_RO"},
		{"OPEN_PR_CREATE_RW_GH", "OPEN_CREATE_GITHUB"},
		{"OPEN_PR_CREATE_RW_FORGEJO", "OPEN_CREATE_FORGEJO"},
		{"BOX_ACCESS_READ_ONLY_LOG", "OPEN_CREATE_RO_LOG"},
		{"BOX_ACCESS_READ_ONLY_SOCKET", "OPEN_CREATE_RO_SOCKET"},
		{"BOX_PUSH_READ_WRITE", "COMMIT_PUSH_RW"},
		{"OUTBOX_PUSH_READ_ONLY", "COMMIT_PUSH_RO"},
		{"BOX_PUSH_READ_WRITE", "BLOCKED_TRIAGE_RW"},
		{"OUTBOX_PUSH_READ_ONLY", "BLOCKED_TRIAGE_RO"},
		{"BOX_PUSH_READ_WRITE", "BLOCKED_PUSH_RW"},
		{"OUTBOX_PUSH_READ_ONLY", "BLOCKED_PUSH_RO"},
		{"IF_BLOCKED_PR_RW_GH", "BLOCKED_PR_GITHUB"},
		{"IF_BLOCKED_PR_RW_FORGEJO", "BLOCKED_PR_FORGEJO"},
		{"NO_PR_FORGE", "BLOCKED_PR_NONE"},
		{"PR_FORGE_READ_ONLY_LOG", "BLOCKED_PR_RO_LOG"},
		{"PR_FORGE_READ_ONLY_SOCKET", "BLOCKED_PR_RO_SOCKET"},
		{"PR_FORGE_READ_WRITE", "BLOCKED_LANDING_PR"},
		{"NO_PR_FORGE", "BLOCKED_LANDING_BRANCH"},
		{"PR_FORGE_READ_ONLY", "BLOCKED_LANDING_RO"},
	}, false, []selectionCase{
		{"github read-write", nil, []string{
			"OPEN_PUSH_RW", "OPEN_CREATE_GITHUB", "COMMIT_PUSH_RW", "BLOCKED_TRIAGE_RW", "BLOCKED_PUSH_RW",
			"BLOCKED_PR_GITHUB", "BLOCKED_LANDING_PR",
		}},
		{"github read-only", []func(*Env){fragReadOnly}, []string{
			"OPEN_PUSH_RO", "OPEN_CREATE_RO_LOG", "COMMIT_PUSH_RO", "BLOCKED_TRIAGE_RO", "BLOCKED_PUSH_RO",
			"BLOCKED_PR_RO_LOG", "BLOCKED_LANDING_RO",
		}},
		{"github read-only socket", []func(*Env){fragReadOnly, fragSocket}, []string{
			"OPEN_PUSH_RO", "OPEN_CREATE_RO_SOCKET", "COMMIT_PUSH_RO", "BLOCKED_TRIAGE_RO", "BLOCKED_PUSH_RO",
			"BLOCKED_PR_RO_SOCKET", "BLOCKED_LANDING_RO",
		}},
		{"forgejo read-write", []func(*Env){fragForgejoTracker, fragForgejoForge}, []string{
			"OPEN_PUSH_RW", "OPEN_CREATE_FORGEJO", "COMMIT_PUSH_RW", "BLOCKED_TRIAGE_RW", "BLOCKED_PUSH_RW",
			"BLOCKED_PR_FORGEJO", "BLOCKED_LANDING_PR",
		}},
		{"forgejo read-only stays forge-agnostic", []func(*Env){fragForgejoTracker, fragForgejoForge, fragReadOnly}, []string{
			"OPEN_PUSH_RO", "OPEN_CREATE_RO_LOG", "COMMIT_PUSH_RO", "BLOCKED_TRIAGE_RO", "BLOCKED_PUSH_RO",
			"BLOCKED_PR_RO_LOG", "BLOCKED_LANDING_RO",
		}},
	})
}

// A git Code Forge has no PR, so a blocked run opens none and reports the
// branch (issue #5233); the Box still pushes it.
func TestAssembleGitForgeBlockedSelection(t *testing.T) {
	assertSelection(t, []gatedRow{
		{"BOX_PUSH_READ_WRITE", "BLOCKED_PUSH_RW"},
		{"IF_BLOCKED_PR_RW_GH", "BLOCKED_PR_GITHUB"},
		{"NO_PR_FORGE", "BLOCKED_PR_NONE"},
		{"PR_FORGE_READ_WRITE", "BLOCKED_LANDING_PR"},
		{"NO_PR_FORGE", "BLOCKED_LANDING_BRANCH"},
	}, false, []selectionCase{
		{"git forge", []func(*Env){fragGitForge}, []string{"BLOCKED_PUSH_RW", "BLOCKED_PR_NONE", "BLOCKED_LANDING_BRANCH"}},
		{"git forge read-only", []func(*Env){fragGitForge, fragReadOnly}, []string{"BLOCKED_PR_NONE", "BLOCKED_LANDING_BRANCH"}},
	})
}

// The Filer's own prompt files and dedups through the tracker's client when
// the Box may write, and through the host-relayed intent when it may not; a
// tracker with no client runs no search at all (issue #5233).
func TestAssembleFilerPromptSelection(t *testing.T) {
	assertSelection(t, []gatedRow{
		{"FILER_FILE_DIRECT_GH", "FILE_GITHUB"},
		{"FILER_FILE_DIRECT_FORGEJO", "FILE_FORGEJO"},
		{"FILER_FILE_RELAY_LOG", "FILE_RELAY_LOG"},
		{"FILER_FILE_RELAY_SOCKET", "FILE_RELAY_SOCKET"},
		{"ISSUE_TRACKER_GITHUB_WRITABLE", "DEDUP_GITHUB"},
		{"ISSUE_TRACKER_FORGEJO", "DEDUP_FORGEJO"},
		{"ISSUE_TRACKER_LOCAL", "DEDUP_LOCAL"},
		{"ISSUE_TRACKER_UNWRITABLE", "DEDUP_UNWRITABLE"},
	}, true, []selectionCase{
		{"github read-write", nil, []string{"FILE_GITHUB", "DEDUP_GITHUB"}},
		{"github read-only", []func(*Env){fragReadOnly}, []string{"FILE_RELAY_LOG", "DEDUP_GITHUB"}},
		{"github read-only socket", []func(*Env){fragReadOnly, fragSocket}, []string{"FILE_RELAY_SOCKET", "DEDUP_GITHUB"}},
		{"forgejo read-write", []func(*Env){fragForgejoTracker}, []string{"FILE_FORGEJO", "DEDUP_FORGEJO"}},
		{"jira", []func(*Env){fragJira}, []string{"FILE_RELAY_LOG", "DEDUP_UNWRITABLE"}},
	})
}

// The work prompt's FILE ISSUES step forks the same way: a writable Box files
// directly and reads back the URLs, a read-only one queues the findings.
func TestAssembleFileIssuesStepSelection(t *testing.T) {
	assertSelection(t, []gatedRow{
		{"FILER_FILE_DIRECT_ANY", "FILE_ISSUES_DIRECT"},
		{"FILER_FILE_RELAY_LOG", "FILE_ISSUES_RELAY_LOG"},
		{"FILER_FILE_RELAY_SOCKET", "FILE_ISSUES_RELAY_SOCKET"},
	}, false, []selectionCase{
		{"read-write", []func(*Env){fragFiler}, []string{"FILE_ISSUES_DIRECT"}},
		{"read-only", []func(*Env){fragFiler, fragReadOnly}, []string{"FILE_ISSUES_RELAY_LOG"}},
		{"read-only socket", []func(*Env){fragFiler, fragReadOnly, fragSocket}, []string{"FILE_ISSUES_RELAY_SOCKET"}},
		{"filer off", nil, nil},
	})
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
			name: "FILE ISSUES off names no FILE ISSUES step anywhere (issue #5300)",
			// The bare string, not "# FILE ISSUES": the land pass and review
			// loop name the step in prose, and a step named with no section
			// behind it is one the agent cannot find.
			wantNot: []string{"FILE ISSUES", "file, don't fix"},
			want: []string{
				"4. Finish the pass: LAND THE CHANGE, OUTCOME.",
				"The default for gate-discovered work is report, don't fix.",
				"Do not run LAND THE CHANGE, OPEN A",
				"then continue straight into LAND THE CHANGE,",
			},
		},
		{
			name:   "FILE ISSUES on",
			mutate: []func(*Env){fragFiler},
			want: []string{
				"# FILE ISSUES",
				"4. Finish the pass: FILE ISSUES, LAND THE CHANGE, OUTCOME.",
				"The default for gate-discovered work is file, don't fix.",
				"Do not run FILE ISSUES, LAND THE\n  CHANGE,",
				"then continue straight into FILE ISSUES,\n  LAND THE CHANGE, OPEN A PULL REQUEST, and OUTCOME.",
			},
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
			wantNot: []string{"below:build failed", "failed# CONTEXT"},
		},
		{
			name:    "CI FAILURE frames the summary as a pointer, not the failure",
			mutate:  []func(*Env){func(e *Env) { e.FixPass, e.CIFailureSummary = 1, "build failed" }},
			want:    []string{"failure itself"},
			wantNot: []string{"known failure", "skip anything CI FAILURE"},
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

// The scout's issue-read step is rendered into the agents JSON, not the
// prompt: on jira, which has no in-Box client, it says linked issues cannot be
// read instead of pointing at the Code Forge (issue #6344).
func TestAssembleScoutIssueReadUnreadableOnJira(t *testing.T) {
	for _, tc := range []struct {
		tracker, want, wantNot string
	}{
		{"github", "via GitHub", ""},
		{"jira", "cannot be read from this Box", "via GitHub"},
	} {
		env := coveredEnv()
		env.IssueTracker = tc.tracker
		env.AgentsJSONTemplate = `{"scout":{"model":"x"}}`
		env.AgentsPromptFiles = `{"scout":"scout-prompt.md"}`
		res, err := Assemble(env, loadTestRegistry(t))
		if err != nil {
			t.Fatalf("Assemble(%s): %v", tc.tracker, err)
		}
		scout := agentPromptFromJSON(t, res.AgentsJSON, "scout")
		if !strings.Contains(scout, tc.want) {
			t.Errorf("%s scout prompt lacks %q:\n%s", tc.tracker, tc.want, scout)
		}
		if tc.wantNot != "" && strings.Contains(scout, tc.wantNot) {
			t.Errorf("%s scout prompt contains %q, want none:\n%s", tc.tracker, tc.wantNot, scout)
		}
	}
}
