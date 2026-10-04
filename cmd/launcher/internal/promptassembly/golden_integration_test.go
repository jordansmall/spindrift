//go:build integration

package promptassembly_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/promptassembly"
	"spindrift.dev/launcher/internal/seamtest"
	"spindrift.dev/launcher/internal/testutil/repopath"
)

// These tests pin every prompt-assembly golden cell in
// tests/testdata/prompt-assembly-golden byte for byte through
// promptassembly.WriteAssembly, the entry point box calls. Each cell mirrors the
// env and skill bake of the bats cell that used to pin it
// (tests/prompt-assembly-parity.bats), as deltas over the suite's default cell.
// UPDATE_GOLDENS=1 (nix run .#regen-goldens) rewrites the goldens instead of
// diffing them.

const (
	// roster is a realistic multi-agent roster. Its reviewer entry stays in the
	// template (the entrypoint drops it from --agents).
	roster = `{"scout":{"description":"fixture scout description","model":"opus","prompt":"","tools":["Read","Bash","WebFetch","WebSearch","Glob","Grep"]},"reviewer":{"description":"fixture reviewer description","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]},"worker":{"description":"fixture worker description","model":"sonnet","prompt":"","tools":["Read","Bash","Edit","Write","Glob","Grep"]}}`
	// rosterNoScout and rosterNoWorker each drop one key, isolating the
	// worker-only and scout-only combinations.
	rosterNoScout  = `{"reviewer":{"description":"fixture reviewer description","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]},"worker":{"description":"fixture worker description","model":"sonnet","prompt":"","tools":["Read","Bash","Edit","Write","Glob","Grep"]}}`
	rosterNoWorker = `{"scout":{"description":"fixture scout description","model":"opus","prompt":"","tools":["Read","Bash","WebFetch","WebSearch","Glob","Grep"]},"reviewer":{"description":"fixture reviewer description","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]}}`
	// rosterWithFiler adds the "filer" entry, flipping the FILER_ENABLED gate
	// that plain roster leaves off.
	rosterWithFiler = `{"scout":{"description":"fixture scout description","model":"opus","prompt":"","tools":["Read","Bash","WebFetch","WebSearch","Glob","Grep"]},"reviewer":{"description":"fixture reviewer description","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]},"worker":{"description":"fixture worker description","model":"sonnet","prompt":"","tools":["Read","Bash","Edit","Write","Glob","Grep"]},"filer":{"description":"fixture filer's description","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]}}`
	// rosterWithReviewEffort gives the reviewer an explicit "effort": "xhigh",
	// distinct from every other effort and model literal here, so a dropped or
	// truncated field fails the diff instead of matching another cell's default.
	rosterWithReviewEffort = `{"scout":{"description":"fixture scout description","model":"opus","prompt":"","tools":["Read","Bash","WebFetch","WebSearch","Glob","Grep"]},"reviewer":{"description":"fixture reviewer description","model":"haiku","effort":"xhigh","prompt":"","tools":["Read","Bash","WebFetch"]},"worker":{"description":"fixture worker description","model":"sonnet","prompt":"","tools":["Read","Bash","Edit","Write","Glob","Grep"]}}`
	// rosterWithReviewAxis adds the "review-axis" fan-out entry, the one roster
	// shape that flips code-review-baked.md's REVIEW_FANOUT_AGENT from the
	// general-purpose fallback to the governed name.
	rosterWithReviewAxis = `{"scout":{"description":"fixture scout description","model":"opus","prompt":"","tools":["Read","Bash","WebFetch","WebSearch","Glob","Grep"]},"reviewer":{"description":"fixture reviewer description","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]},"review-axis":{"description":"fixture review-axis description","model":"haiku","prompt":"","tools":["Read","Bash","Glob","Grep"]},"worker":{"description":"fixture worker description","model":"sonnet","prompt":"","tools":["Read","Bash","Edit","Write","Glob","Grep"]}}`

	agentsPromptFilesWithReviewAxis = `{"scout":"scout-prompt.md","reviewer":"review-prompt.md","filer":"filer-prompt.md","worker":"worker-prompt.md","review-axis":"review-axis-prompt.md"}`
	agentsPromptFilesDefault        = `{"scout":"scout-prompt.md","reviewer":"review-prompt.md","filer":"filer-prompt.md","worker":"worker-prompt.md"}`
)

// issueText carries a literal triple-backtick run, the untrusted shape
// promptfence.Block's dynamic fence widening exists for: untrusted text must not
// close its own fence.
const issueText = "Widgets double-count on retry when the frobnicator restarts mid-batch.\n\nRepro:\n```\nfrobnicate --retry --batch=widgets\n```\n\nExpected: each widget counted once. Actual: counted twice on retry."

// issueTextLocalTracker is a host-rendered forge.IssueText covering both block
// kinds issuetext.go's renderLinkedIssues emits.
const issueTextLocalTracker = "Retry math drifts when two frobnicators race the same batch.\n\n## Linked issues\n\n### widget-lock — Add per-batch locking (blocked-by of retry-math)\n\nstatus: open\n\nGuard the batch counter with a mutex before the retry path lands.\n\n### Unresolved references\n\n- https://github.com/o/r/issues/9 (parent of retry-math): issue not found"

const (
	modeInitial = "initial"
	modeResume  = "resume"
)

// cellInputs is what a cell varies: the process env EnvFromEnviron reads, the
// baked skill directories, and the nix-baked agent-name -> prompt-file map.
type cellInputs struct {
	vars              map[string]string
	skills            []string
	agentsPromptFiles string
}

func (c *cellInputs) export(k, v string) { c.vars[k] = v }
func (c *cellInputs) unset(k string)     { delete(c.vars, k) }

func (c *cellInputs) bakeSkill(name string) {
	if !slices.Contains(c.skills, name) {
		c.skills = append(c.skills, name)
	}
}

func (c *cellInputs) dropSkill(name string) {
	c.skills = slices.DeleteFunc(c.skills, func(s string) bool { return s == name })
}

// dispatch mirrors tests/helper.bash's set_dispatch_kind: the three axes
// dispatch.buildBoxEnv derives from the kind's descriptor, plus the BRANCH
// entrypoint.sh derives from the key. Call it after ISSUE_NUMBER or CHORE_NAME
// is set: the key reads whichever the kind is keyed by.
func (c *cellInputs) dispatch(kind string) {
	c.export("DISPATCH_KIND", kind)
	switch kind {
	case "work":
		c.export("DISPATCH_KEYING", "issue")
		c.export("DISPATCH_ANNOUNCE_VERB", "implementing")
		c.export("DISPATCH_KEY", c.vars["ISSUE_NUMBER"])
	case "research":
		c.export("DISPATCH_KEYING", "issue")
		c.export("DISPATCH_ANNOUNCE_VERB", "researching")
		c.export("DISPATCH_KEY", c.vars["ISSUE_NUMBER"])
	case "butler":
		c.export("DISPATCH_KEYING", "chore")
		c.export("DISPATCH_ANNOUNCE_VERB", "sweeping")
		c.export("DISPATCH_KEY", "butler-"+c.vars["CHORE_NAME"])
	default:
		panic("unknown dispatch kind " + kind)
	}
	c.export("BRANCH", "agent/issue-"+c.vars["DISPATCH_KEY"])
}

// provisionAgents is the bats cells' worker + scout provisioning over a roster.
func (c *cellInputs) provisionAgents(roster string) {
	c.export("AGENTS_JSON_TEMPLATE", roster)
	c.export("BOX_WORKER_PROVISIONED", "1")
	c.export("BOX_SCOUT_PROVISIONED", "1")
}

func (c *cellInputs) filerOn() {
	c.provisionAgents(rosterWithFiler)
	c.export("BOX_FILER_ENABLED", "1")
}

// readOnly mirrors `unset BOX_WRITE_ENABLED`.
func (c *cellInputs) readOnly() { c.unset("BOX_WRITE_ENABLED") }

// forgejoForge mirrors setup_forgejo_forge_env's pipeline-visible half.
func (c *cellInputs) forgejoForge() {
	c.export("CODE_FORGE", "forgejo")
	c.export("BOX_FORGE_BACKEND", "FORGEJO")
}

func (c *cellInputs) forgejoTracker() {
	c.export("ISSUE_TRACKER", "forgejo")
	c.export("BOX_TRACKER_AXIS_READ", "FORGEJO")
	c.export("BOX_TRACKER_AXIS_WRITE", "FORGEJO")
}

func (c *cellInputs) localTracker() {
	c.export("ISSUE_TRACKER", "local")
	c.export("BOX_TRACKER_AXIS_READ", "LOCAL")
	c.unset("BOX_TRACKER_AXIS_WRITE")
}

// butler mirrors setup_butler_env: the butler is keyed by Chore name, never a
// tracker issue.
func (c *cellInputs) butler(chore string) {
	c.unset("ISSUE_NUMBER")
	c.unset("ISSUE_TITLE")
	c.export("CHORE_NAME", chore)
	c.dispatch("butler")
	c.export("CHORE_HEAD", "deadbeef")
	c.export("CHORE_DIFF_RANGE", "cafef00d..deadbeef")
	c.export("CHORE_SLICE", "cmd/launcher/main.go\ncmd/launcher/internal/dispatch/dispatch.go")
	c.export("CHORE_MAX_FINDINGS", "5")
}

// defaultCell is the bats suite's starting point: setup_entrypoint_env plus
// lib/env-schema.nix's boxEnv defaults, a github issue worked by a work
// dispatch, writes enabled, no roster, and setup()'s six baked skills.
func defaultCell() *cellInputs {
	c := &cellInputs{
		vars: map[string]string{
			"ISSUE_TRACKER":            "github",
			"CODE_FORGE":               "github",
			"BOX_FORGE_BACKEND":        "GH",
			"BOX_TRACKER_AXIS_READ":    "GITHUB",
			"BOX_TRACKER_AXIS_WRITE":   "GITHUB",
			"BOX_TRACKER_AXIS_FILER":   "GH",
			"BOX_WRITE_ENABLED":        "1",
			"BOX_OUTBOX_RELAY_CAPABLE": "1",
			"BOX_SIGNAL_CARRIER":       "log",
			"ISSUE_NUMBER":             "7",
			"ISSUE_TITLE":              "Do the thing",
			"BASE_BRANCH":              "main",
			"IN_PROGRESS_LABEL":        "agent-in-progress",
			"COMPLETE_LABEL":           "agent-complete",
			"RUN_NONCE":                "test-run-nonce-0001",
		},
		skills:            []string{"caveman", "tdd", "commit", "code-review", "check-hygiene", "code-comments"},
		agentsPromptFiles: agentsPromptFilesDefault,
	}
	c.dispatch("work")
	return c
}

// goldenCell is one golden: its inputs as deltas over defaultCell, the
// Handoff.SessionMode it must report, and whether it is an orchestrator cell
// that also pins the review prompt and the Handoff's review facts.
type goldenCell struct {
	name   string
	mode   string
	review bool
	setup  func(c *cellInputs)
	// check holds assertions on the produced prompt that guard a cell's reason
	// to exist against an update-mode regeneration silently changing it.
	check func(t *testing.T, prompt string)
}

func goldenCells() []goldenCell {
	return []goldenCell{
		{name: "covered-cell-issue-text", mode: modeInitial, setup: func(c *cellInputs) {
			c.provisionAgents(roster)
			c.export("ISSUE_TEXT", issueText)
		}},
		{name: "covered-cell-review-axis-roster", mode: modeInitial, setup: func(c *cellInputs) {
			// The default map names only the historical four agents, so the
			// fan-out entry needs its own row to reach review-axis-prompt.md.
			c.provisionAgents(rosterWithReviewAxis)
			c.agentsPromptFiles = agentsPromptFilesWithReviewAxis
		}},
		{name: "worker-no-scout", mode: modeInitial, setup: func(c *cellInputs) {
			c.export("AGENTS_JSON_TEMPLATE", rosterNoScout)
			c.export("BOX_WORKER_PROVISIONED", "1")
		}},
		{name: "scout-no-worker", mode: modeInitial, setup: func(c *cellInputs) {
			c.export("AGENTS_JSON_TEMPLATE", rosterNoWorker)
			c.export("BOX_SCOUT_PROVISIONED", "1")
		}},
		{name: "no-roster", mode: modeInitial, setup: func(c *cellInputs) {}},
		{name: "research", mode: modeInitial, setup: func(c *cellInputs) {
			c.dispatch("research")
		}},
		{name: "research-filer-on", mode: modeInitial, setup: func(c *cellInputs) {
			c.dispatch("research")
			c.filerOn()
		}},
		{name: "self-contained-research", mode: modeInitial, setup: func(c *cellInputs) {
			c.dispatch("research")
			c.export("SELF_CONTAINED", "1")
		}},
		{name: "self-contained-research-filer-on", mode: modeInitial, setup: func(c *cellInputs) {
			c.dispatch("research")
			c.export("SELF_CONTAINED", "1")
			c.filerOn()
		}},
		{name: "fix-pass", mode: modeResume, setup: func(c *cellInputs) {
			c.export("FIX_PASS", "1")
		}},
		{name: "github-read-only", mode: modeInitial, setup: func(c *cellInputs) {
			c.readOnly()
		}},
		{name: "forgejo-read-write", mode: modeInitial, setup: func(c *cellInputs) {
			c.forgejoForge()
		}},
		{name: "forgejo-read-only", mode: modeInitial, setup: func(c *cellInputs) {
			c.forgejoForge()
			c.readOnly()
		}},
		{name: "local-tracker-no-issue-ref", mode: modeInitial, setup: func(c *cellInputs) {
			c.localTracker()
		}},
		{name: "local-tracker-issue-text", mode: modeInitial, setup: func(c *cellInputs) {
			c.localTracker()
			// The local tracker's issue-read fragment no longer walks the link
			// chain in-box: it arrives pre-rendered through ISSUE_TEXT.
			c.export("ISSUE_TEXT", issueTextLocalTracker)
		}},
		{name: "local-tracker-issue-ref-on", mode: modeInitial, setup: func(c *cellInputs) {
			c.localTracker()
			c.export("LOCAL_ISSUE_REFERENCE", "1")
		}},
		{name: "forgejo-tracker", mode: modeInitial, setup: func(c *cellInputs) {
			c.forgejoTracker()
			c.export("BOX_TRACKER_AXIS_FILER", "FORGEJO")
		}},
		{name: "jira-tracker", mode: modeInitial, setup: func(c *cellInputs) {
			// jira rides the same prompt-selection arms as github.
			c.export("ISSUE_TRACKER", "jira")
		}},
		{name: "orchestrator-filer-on", mode: modeInitial, review: true, setup: func(c *cellInputs) {
			c.filerOn()
		}},
		{name: "orchestrator-filer-off", mode: modeInitial, review: true, setup: func(c *cellInputs) {
			c.provisionAgents(roster)
		}},
		{name: "orchestrator-skills-absent", mode: modeInitial, review: true, setup: func(c *cellInputs) {
			c.provisionAgents(roster)
			// checkCoveredCell's "SkillsFound empty, every *SkillBaked flag false" branch.
			c.skills = nil
		}},
		{name: "orchestrator-review-effort-set", mode: modeInitial, review: true, setup: func(c *cellInputs) {
			c.provisionAgents(rosterWithReviewEffort)
		}},
		{name: "tdd-skill-absent", mode: modeInitial, setup: func(c *cellInputs) {
			c.provisionAgents(roster)
			// Only tdd is unbaked, the realistic shape: a consumer bakes a subset.
			c.dropSkill("tdd")
		}},
		{name: "commit-skill-absent", mode: modeInitial, setup: func(c *cellInputs) {
			c.provisionAgents(roster)
			c.dropSkill("commit")
		}},
		{name: "code-review-skill-absent", mode: modeInitial, setup: func(c *cellInputs) {
			c.provisionAgents(roster)
			// The gate renders into .agents.json's reviewer.prompt, not the prompt.
			c.dropSkill("code-review")
		}},
		{name: "nix-checks-skill-baked", mode: modeInitial, setup: func(c *cellInputs) {
			c.provisionAgents(roster)
			// Dogfood-only, so no other cell bakes it.
			c.bakeSkill("nix-checks")
		}},

		// Socket-carrier siblings of the log-carrier cells: each differs from its
		// nearest sibling by BOX_SIGNAL_CARRIER=socket plus whatever gate the
		// target fragment pair needs.
		{name: "github-read-only-research-signal-socket", mode: modeInitial, setup: func(c *cellInputs) {
			c.dispatch("research")
			c.readOnly()
			c.unset("BOX_OUTBOX_RELAY_CAPABLE")
			c.export("BOX_SIGNAL_CARRIER", "socket")
		}},
		{name: "local-tracker-research-signal-socket", mode: modeInitial, setup: func(c *cellInputs) {
			c.dispatch("research")
			c.localTracker()
			c.export("BOX_SIGNAL_CARRIER", "socket")
		}},
		{name: "forgejo-tracker-readonly-research-signal-socket", mode: modeInitial, setup: func(c *cellInputs) {
			c.dispatch("research")
			c.forgejoTracker()
			c.readOnly()
			c.unset("BOX_OUTBOX_RELAY_CAPABLE")
			c.export("BOX_SIGNAL_CARRIER", "socket")
		}},
		{name: "github-read-only-signal-socket", mode: modeInitial, setup: func(c *cellInputs) {
			c.readOnly()
			c.unset("BOX_OUTBOX_RELAY_CAPABLE")
			c.export("BOX_SIGNAL_CARRIER", "socket")
		}},
		{name: "orchestrator-filer-on-read-only", mode: modeInitial, review: true, setup: func(c *cellInputs) {
			c.filerOn()
			c.readOnly()
			c.unset("BOX_OUTBOX_RELAY_CAPABLE")
		}, check: func(t *testing.T, prompt string) {
			// Guards the cell's reason to exist against an update-mode
			// regeneration silently flipping it to the socket fragment.
			const logCarrier = "`SPINDRIFT_ISSUE_INTENT` lines instead, and the launcher files"
			if !strings.Contains(prompt, logCarrier) {
				t.Errorf("prompt lacks the log-carrier text %q", logCarrier)
			}
		}},
		{name: "orchestrator-filer-on-signal-socket", mode: modeInitial, review: true, setup: func(c *cellInputs) {
			c.filerOn()
			c.readOnly()
			c.unset("BOX_OUTBOX_RELAY_CAPABLE")
			c.export("BOX_SIGNAL_CARRIER", "socket")
		}},
		{name: "research-filer-on-signal-socket", mode: modeInitial, setup: func(c *cellInputs) {
			c.dispatch("research")
			c.filerOn()
			c.export("BOX_SIGNAL_CARRIER", "socket")
		}},
		{name: "butler-bugs", mode: modeInitial, setup: func(c *cellInputs) {
			c.butler("bugs")
		}},
		{name: "butler-refactor", mode: modeInitial, setup: func(c *cellInputs) {
			c.butler("refactor")
		}},
		{name: "butler-docs-drift", mode: modeInitial, setup: func(c *cellInputs) {
			c.butler("docs-drift")
		}},
		{name: "butler-docs-drift-patch", mode: modeInitial, setup: func(c *cellInputs) {
			// A promotion/patch class pair and a filer roster, so the cell also
			// captures the reviewer's rendered prompt in .agents.json.
			c.butler("docs-drift")
			c.export("CHORE_CLASSES", "docs-drift typo")
			c.export("CHORE_PATCH_CLASSES", "docs-drift")
			c.export("AGENTS_JSON_TEMPLATE", rosterWithFiler)
			c.export("BOX_FILER_ENABLED", "1")
		}},
		{name: "forgejo-orchestrator-filer-on", mode: modeInitial, review: true, setup: func(c *cellInputs) {
			c.forgejoForge()
			c.forgejoTracker()
			c.export("BOX_TRACKER_AXIS_FILER", "FORGEJO")
			c.filerOn()
		}},
		{name: "forgejo-fix-pass", mode: modeResume, setup: func(c *cellInputs) {
			c.export("FIX_PASS", "1")
			c.forgejoForge()
		}},
		{name: "principle-skills-baked", mode: modeInitial, setup: func(c *cellInputs) {
			c.provisionAgents(roster)
			// Dogfood-only, like nix-checks: no stock Consumer image bakes them.
			c.bakeSkill("principle-fix-root-causes")
			c.bakeSkill("principle-laziness-protocol")
			c.bakeSkill("principle-redesign-from-first-principles")
		}},
		{name: "butler-docs-drift-filer-on-signal-socket", mode: modeInitial, setup: func(c *cellInputs) {
			c.butler("docs-drift")
			c.export("AGENTS_JSON_TEMPLATE", rosterWithFiler)
			c.export("BOX_FILER_ENABLED", "1")
			c.unset("BOX_OUTBOX_RELAY_CAPABLE")
			c.export("BOX_SIGNAL_CARRIER", "socket")
		}},
	}
}

// assembleCell runs WriteAssembly over the cell's inputs, with outputs under
// outDir, and returns the Result plus the output paths.
func assembleCell(t *testing.T, c *cellInputs, outDir string) (promptassembly.Result, promptassembly.OutputPaths) {
	t.Helper()

	// Blank every var EnvFromEnviron reads, so the cell sees exactly its own
	// env and none of the ambient Box env a dispatched run carries.
	for _, name := range promptassembly.BoxEnvVarNames {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	for k, v := range c.vars {
		t.Setenv(k, v)
	}

	skillsDir := t.TempDir()
	for _, name := range c.skills {
		if err := os.MkdirAll(filepath.Join(skillsDir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(skillsDir, name, "SKILL.md"), []byte("---\nname: "+name+"\n---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	env := promptassembly.EnvFromEnviron()
	env.ProbeBakedSkills(skillsDir)
	env.SkillsFound = promptassembly.ScanSkillsFound(skillsDir)
	env.PromptsDir = repopath.PromptsDir()
	env.AgentsPromptFiles = c.agentsPromptFiles
	env.CommsContractFile = seamtest.Path(t, "comms-contract.md")
	env.CheckContractFile = seamtest.Path(t, "check-contract.md")
	env.OutcomeContractFile = seamtest.Path(t, "outcome-contract.md")
	env.ResearchOutcomeContractFile = seamtest.Path(t, "research-outcome-contract.md")

	reg, err := promptassembly.LoadRegistryFile(seamtest.Path(t, "fragments-registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	markers, err := promptassembly.LoadValidateMarkersFile(seamtest.Path(t, "prompt-contract-registry.json"))
	if err != nil {
		t.Fatal(err)
	}

	out := promptassembly.OutputPaths{
		Prompt:       filepath.Join(outDir, "prompt.txt"),
		AgentsJSON:   filepath.Join(outDir, "agents.json"),
		Handoff:      filepath.Join(outDir, "handoff.json"),
		ReviewPrompt: filepath.Join(outDir, "review-prompt.txt"),
		Fragments:    filepath.Join(outDir, "fragments.txt"),
	}
	// Passthrough stays zero: the goldens pin only what Assemble itself derives.
	result, err := promptassembly.WriteAssembly(env, reg, markers, promptassembly.Passthrough{}, out, os.Stderr)
	if err != nil {
		t.Fatalf("WriteAssembly: %v", err)
	}
	return result, out
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPromptAssemblyGoldens(t *testing.T) {
	goldenDir := repopath.PromptAssemblyGoldenDir()
	update := updateGoldens()

	for _, cell := range goldenCells() {
		t.Run(cell.name, func(t *testing.T) {
			c := defaultCell()
			cell.setup(c)
			result, out := assembleCell(t, c, t.TempDir())
			golden := func(ext string) string { return filepath.Join(goldenDir, cell.name+"."+ext) }
			report := func(err error) {
				t.Helper()
				if err != nil {
					t.Error(err)
				}
			}

			report(compareOrUpdateText(golden("prompt.txt"), mustRead(t, out.Prompt), update))
			report(compareOrUpdateText(golden("fragments.txt"), mustRead(t, out.Fragments), update))

			if agents := mustRead(t, out.AgentsJSON); len(agents) > 0 {
				report(compareOrUpdateJSON(golden("agents.json"), agents, update))
			} else {
				report(removeGoldenIfUpdate(golden("agents.json"), update))
			}

			if result.Handoff.SessionMode != cell.mode {
				t.Errorf("SessionMode = %q, want %q", result.Handoff.SessionMode, cell.mode)
			}

			if cell.review {
				// Since issue #2975 the Handoff also carries per-run paths no golden
				// can pin byte for byte, so diff only the review facts and require
				// ReviewPromptFile to name a file that was actually written.
				report(compareOrUpdateJSON(golden("handoff.json"), mustRead(t, out.Handoff), update, "ReviewModel", "ReviewEffort"))
				if result.Handoff.ReviewPromptFile == "" {
					t.Fatal("Handoff.ReviewPromptFile is empty, want the written review prompt")
				}
				// Result.Fragments counts fragments reaching the review prompt, so
				// its text is pinned or the fragments sidecar would claim coverage
				// no golden backs.
				report(compareOrUpdateText(golden("review-prompt.txt"), mustRead(t, result.Handoff.ReviewPromptFile), update))
			}

			if cell.check != nil {
				cell.check(t, string(mustRead(t, out.Prompt)))
			}
		})
	}
}

// Every golden's cell prefix must name a cell here, so a deleted cell cannot
// leave goldens that nothing pins.
func TestPromptAssemblyGoldenDirHasNoOrphans(t *testing.T) {
	names := map[string]bool{}
	for _, cell := range goldenCells() {
		names[cell.name] = true
	}
	files, err := os.ReadDir(repopath.PromptAssemblyGoldenDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		cell, _, _ := strings.Cut(f.Name(), ".")
		if !names[cell] {
			t.Errorf("golden %s has no cell in goldenCells()", f.Name())
		}
	}
}

// researchVerdictRow is one row of nix/research-verdicts-parity.nix: a
// RESEARCH_VERDICTS value and the three renderings lib/research-verdicts.nix's
// renderPrompt produces for it.
type researchVerdictRow struct {
	Verdicts string `json:"verdicts"`
	Status   string `json:"status"`
	Enum     string `json:"enum"`
	Bullets  string `json:"bullets"`
}

// Pins forge.VerdictLabels.RenderPrompt's three renderings (the status=<...>
// alternation, the backtick enum, the verdict bullets) against the nix
// renderPrompt (issues #4159, #2630). Row 0 is the default set, row 1 a custom one.
func TestResearchVerdictRenderingMatchesNix(t *testing.T) {
	var rows []researchVerdictRow
	if err := json.Unmarshal(mustRead(t, seamtest.Path(t, "research-verdicts-parity.json")), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) < 2 {
		t.Fatalf("research-verdicts-parity.json has %d rows, want the default and a custom set", len(rows))
	}

	for i, row := range rows {
		t.Run([]string{"default", "custom"}[min(i, 1)], func(t *testing.T) {
			c := defaultCell()
			c.dispatch("research")
			c.export("RESEARCH_VERDICTS", row.Verdicts)
			_, out := assembleCell(t, c, t.TempDir())
			prompt := string(mustRead(t, out.Prompt))

			for field, want := range map[string]string{"status": row.Status, "enum": row.Enum, "bullets": row.Bullets} {
				if !strings.Contains(prompt, want) {
					t.Errorf("assembled prompt lacks the nix-rendered %s: %s", field, want)
				}
			}
			for _, marker := range []string{"RESEARCH_VERDICT_BULLETS", "RESEARCH_VERDICT_ENUM", "${RESEARCH_STATUS_ENUM}"} {
				if strings.Contains(prompt, marker) {
					t.Errorf("assembled prompt still carries the unrendered marker %s", marker)
				}
			}
		})
	}
}
