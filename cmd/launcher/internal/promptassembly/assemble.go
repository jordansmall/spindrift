package promptassembly

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ErrUnsupportedCell marks an Env combination Assemble does not know how to
// render: an unrecognized DispatchKind value — see checkCoveredCell for the
// exact set it is checked against. IssueTracker and CodeForge are covered
// upstream (see checkCoveredCell's doc comment) and no longer re-validated
// here. Every other axis (orchestrator on/off, dispatch-kind/fix-pass,
// per-skill baked flags, access/forge) is handled by Assemble's own logic
// regardless of how the others are set, so no combination of them is
// rejected here.
var ErrUnsupportedCell = errors.New("promptassembly: env combination not covered by Assemble")

// Result is Assemble's rendered output: the final prompt text, the
// (possibly empty) completed --agents JSON, and the driver hand-off facts
// run_driver_in_env (entrypoint.sh: 1282-1310) derives from the same phase.
type Result struct {
	Prompt     string
	AgentsJSON string
	Handoff    Handoff
}

// Handoff mirrors the subset of run_driver_in_env's invoker/flag derivation
// (entrypoint.sh: 1282-1310) that phase_prompt_assembly itself determines or
// hands off a value for.
type Handoff struct {
	// SessionMode is "resume" or "initial" (entrypoint.sh: 1037-1052).
	SessionMode string
	// Invoker is "orchestrator" or "driver-exec" (entrypoint.sh: 1282-1286).
	Invoker string
	// ReviewPromptFile is only ever populated when Invoker is "orchestrator"
	// (entrypoint.sh: 1294) and the cell is the default fresh-work dispatch
	// (kind "work", FixPass == 0) -- research and fix-pass cells leave it
	// empty even with the orchestrator on, per entrypoint.sh's if/elif/else
	// (entrypoint.sh: 1029-1062): review_prompt_rendered is only ever
	// assigned inside that chain's final "else" branch.
	//
	// ReviewModel, by contrast, is extracted from AgentsJSONTemplate's own
	// "reviewer" key (entrypoint.sh: 1096) whenever Invoker is
	// "orchestrator", regardless of dispatch kind or FixPass -- that
	// extraction is a separate, unconditional step inside the --agents JSON
	// block (entrypoint.sh: 1086-1101), not gated by the dispatch-kind/
	// fix-pass if/elif/else chain ReviewPromptFile is. It stays empty when
	// Invoker is "driver-exec", or when AgentsJSONTemplate carries no
	// "reviewer" key (or a reviewer entry with no "model" field), mirroring
	// jq's `.reviewer.model // empty` (entrypoint.sh: 1096).
	//
	// ReviewEffort mirrors ReviewModel exactly, extracted from the same
	// "reviewer" key's "effort" field under the same condition (Invoker
	// "orchestrator", unconditional on dispatch kind or FixPass). It stays
	// empty when Invoker is "driver-exec", or when AgentsJSONTemplate
	// carries no "reviewer" key (or a reviewer entry with no "effort"
	// field), mirroring jq's `.reviewer.effort // empty`.
	ReviewPromptFile string
	ReviewModel      string
	ReviewEffort     string

	// WorkerPromptFile is the Go orchestrator's own driver-exec-spawned
	// parallel worker prompt (issue #2059, #2058) -- the base prompt a
	// coordinator's manifest-emission dispatches into one worktree per
	// slice. It is populated under the exact same condition as
	// ReviewPromptFile above (Invoker "orchestrator", kind "work",
	// FixPass == 0): only the default fresh-work dispatch cell ever
	// fans out slices this way -- research and fix-pass cells leave it
	// empty even with the orchestrator on, for the same reasons
	// ReviewPromptFile does.
	WorkerPromptFile string
}

// checkCoveredCell validates that e sits in one of Assemble's covered Env
// cells. Only DispatchKind is checked here, against a fixed allowlist of
// "work" (explicit or default) or "research" -- an unrecognized value is a
// real "Assemble doesn't know how to render this" case, and DispatchKind
// has no schema entry to guard it upstream: it is set programmatically at
// runtime by applyDispatchKind (cmd/launcher/main.go), never eval-asserted.
//
// IssueTracker and CodeForge used to be re-validated here too, but that
// duplicated two guarantees that already hold before Assemble ever runs, so
// their arms were deleted (issue #2540):
//
//   - lib/mkHarness.nix's `assert choicesCheckOk;` eval-time assert
//     (backed by the choices-guard block) validates both fields' schema
//     `choices` (lib/env-schema.nix) at build time.
//   - cmd/launcher/main.go's validate() re-checks both at launcher startup
//     via trackerRow.ValidAsTracker and codeForgeRow.ValidAsCodeForge.
//
// Every other axis -- the orchestrator flag, FixPass, the four per-skill
// baked flags, BoxWriteEnabled -- is handled by Assemble's own rendering
// logic regardless of how the others are set, so no combination of them is
// rejected here (issue #2354): a partial skill-baked combination (any
// subset of the four per-skill gates, matching lib/image.nix's per-skill
// baking) renders exactly the fragments whose gate is on, and
// OrchestratorEnabled combined with FixPass > 0 or DispatchKind ==
// "research" renders the same fix-prompt.md/research-prompt.md any other
// cell on that dispatch-kind/fix-pass axis would, with
// Handoff.ReviewPromptFile only ever populated on the default
// fresh-work-dispatch path regardless of the orchestrator flag (see
// Handoff's doc comment).
func checkCoveredCell(e Env) error {
	kind := e.DispatchKind
	if kind == "" {
		kind = defaultDispatchKind
	}
	if kind != defaultDispatchKind && kind != "research" {
		return fmt.Errorf("dispatch kind %q: %w", e.DispatchKind, ErrUnsupportedCell)
	}

	return nil
}

// substTokenRe matches the braced ${NAME} substitution form _subst's
// envsubst call recognizes (entrypoint.sh: 405-433); every template and
// fragment file under templates/default/prompts references its
// substitution variables this way, never bare $NAME (verified against the
// tree, issue #2349).
var substTokenRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// substitute reproduces _subst's allowlisted envsubst call (entrypoint.sh:
// 405-433) in a single pass over text: every ${NAME} whose NAME is a key of
// allowlist is replaced by its value; anything else -- an unlisted ${OTHER}
// or a literal bare $ -- passes through untouched. A single
// ReplaceAllStringFunc pass over the original text (rather than sequential
// per-name replacement) guarantees a substituted value that itself contains
// ${NAME}-shaped text is never re-expanded.
func substitute(text string, allowlist map[string]string) string {
	return substTokenRe.ReplaceAllStringFunc(text, func(tok string) string {
		name := tok[2 : len(tok)-1]
		if v, ok := allowlist[name]; ok {
			return v
		}
		return tok
	})
}

// renderFile reads path, substitutes it through allowlist, and trims the
// trailing newlines a $(...) command substitution would strip -- the same
// three-step sequence entrypoint.sh's _subst call performs at every one of
// its call sites (fragment rows, the base template, and per-agent prompt
// files), centralized here so that "command-sub strips trailing newlines"
// invariant lives in one place.
func renderFile(path string, allowlist map[string]string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(substitute(string(data), allowlist), "\n"), nil
}

// injectSharedBlock mirrors _inject_shared_block (entrypoint.sh: 632-643),
// called once per contract file from Assemble's injection step
// (entrypoint.sh: 1064-1074). An empty contractPath is a silent no-op --
// Assemble's covered cells only ever populate the contract-file Env fields
// a given cell actually needs. Otherwise contractPath is rendered through
// renderFile (the same allowlist substitution and trailing-newline trim as
// every other file Assemble reads), and its first line -- the marker each
// contract file is pre-sliced to start with, e.g. "# COMMS" -- is checked
// against prompt: already present, prompt is returned unchanged (the
// idempotent skip); otherwise the rendered block is appended, separated by
// a blank line.
func injectSharedBlock(prompt, contractPath string, allowlist map[string]string) (string, error) {
	if contractPath == "" {
		return prompt, nil
	}

	block, err := renderFile(contractPath, allowlist)
	if err != nil {
		return "", fmt.Errorf("read contract file %s: %w", contractPath, err)
	}

	marker := block
	if idx := strings.IndexByte(block, '\n'); idx != -1 {
		marker = block[:idx]
	}

	if strings.Contains(prompt, marker) {
		return prompt, nil
	}

	return prompt + "\n\n" + block, nil
}

// Assemble renders the covered Env cell's prompt, --agents JSON, and driver
// hand-off facts, mirroring agent/entrypoint.sh's phase_prompt_assembly (see
// checkCoveredCell for the exact covered cells). Any Env outside those
// cells is rejected up front, before any file I/O, with an error wrapping
// ErrUnsupportedCell.
func Assemble(e Env, reg Registry) (Result, error) {
	if err := checkCoveredCell(e); err != nil {
		return Result{}, err
	}

	gates := Gates(e)
	// SKILLS_FOUND is a filesystem-derived presence gate Gates itself never
	// computes (I/O is out of its scope, see env.go's package doc) -- it's
	// Assemble's own concern, resolved directly from the pre-resolved
	// Env.SkillsFound field.
	gates["SKILLS_FOUND"] = e.SkillsFound != ""

	// The _subst allowlist (entrypoint.sh: 405-433): the eight fixed names
	// (the original seven, plus RESEARCH_STATUS_ENUM, issue #2504) plus the
	// flat _FRAGMENT_SUBST_VARS list -- every registry row's var and
	// extraSubstVars, concatenated once across all rows (identical for
	// every _subst call in this function, never scoped per-fragment).
	allowlist := map[string]string{
		"ISSUE_NUMBER":         e.IssueNumber,
		"ISSUE_TITLE":          e.IssueTitle,
		"BRANCH":               e.Branch,
		"BASE_BRANCH":          e.BaseBranch,
		"IN_PROGRESS_LABEL":    e.InProgressLabel,
		"COMPLETE_LABEL":       e.CompleteLabel,
		"RUN_NONCE":            e.RunNonce,
		"RESEARCH_STATUS_ENUM": e.ResearchStatusEnum,
	}

	// extraSubstVars raw sources: as of issue #2349 the registry carries
	// exactly two (SKILLS_FOUND, CI_FAILURE_SUMMARY -- see fragments.nix's
	// header comment and registry_test.go's TestLoadRegistryParsesAllRows).
	// SKILLS_FOUND's raw value is Env.SkillsFound; CI_FAILURE_SUMMARY's raw
	// value is Env.CIFailureSummary (issue #2354) -- the same field that
	// also drives the CI_FAILURE_SUMMARY gate above (Gates), since its own
	// presence is the gate.
	extraRaw := map[string]string{
		"SKILLS_FOUND":       e.SkillsFound,
		"CI_FAILURE_SUMMARY": e.CIFailureSummary,
	}
	seenExtra := map[string]bool{}
	for _, row := range reg.Rows {
		for _, extra := range row.ExtraSubstVars {
			if seenExtra[extra] {
				continue
			}
			seenExtra[extra] = true
			allowlist[extra] = extraRaw[extra]
		}
	}

	// The fragment loop (entrypoint.sh: 1001-1009): for each row, in
	// registry order, render its fragment when its gate is on and assign
	// renderedText + "\n\n" to its var; assign empty when the gate is off.
	// The "\n\n" is appended outside _subst -- entrypoint.sh: 694-710
	// explains why: command substitution strips trailing newlines, so the
	// blank-line separator can't be baked into the fragment file or the
	// substitution result, only appended at the assignment site.
	for _, row := range reg.Rows {
		if gates[row.Gate] {
			path := filepath.Join(e.PromptsDir, "fragments", row.Fragment)
			// renderFile reproduces "$(_subst "$f")"'s command-substitution
			// newline stripping (bash strips ALL trailing newlines from
			// $(...) output, not just one) before the "\n\n" separator --
			// itself never part of the fragment file or the substitution
			// result -- is appended at this assignment site, per the
			// comment above.
			rendered, err := renderFile(path, allowlist)
			if err != nil {
				// entrypoint.sh's own equivalent of this call,
				// `printf -v "$_fvar" '%s' "$(_subst "${PROMPTS_DIR}/fragments/${_ffile}")"`
				// (entrypoint.sh: 1001-1009), sits as a printf argument
				// rather than a bare assignment -- a failed command
				// substitution there never trips `set -e` (bash only
				// checks the exit status of the printf itself, which
				// still runs and succeeds), so a missing/unreadable
				// fragment file silently resolves to an empty string
				// instead of aborting the script. A missing file
				// reproduces that exact swallow; any other read error
				// (e.g. permission denied) is not something old bash's
				// quirk would have swallowed either, so it still
				// hard-fails here.
				if !errors.Is(err, os.ErrNotExist) {
					return Result{}, fmt.Errorf("read fragment %s: %w", row.Fragment, err)
				}
				allowlist[row.Var] = ""
				continue
			}
			allowlist[row.Var] = rendered + "\n\n"
		} else {
			allowlist[row.Var] = ""
		}
	}

	// Base template selection (entrypoint.sh: 1029-1063) and session mode
	// (entrypoint.sh: 1037-1052): mirrors the if/elif/else precedence
	// exactly -- research first (regardless of FixPass), then a warm fix
	// pass, then the default work/issue cell. Shared-block injection
	// (_inject_shared_block, entrypoint.sh: 1064-1074) follows base-template
	// selection below.
	//
	// For the work/issue-prompt.md cell specifically, injection is a no-op:
	// outcome's marker "# LAND THE CHANGE",
	// comms's "# COMMS", and check's "# CHECK" are all sliced FROM
	// issue-prompt.md itself (lib/prompt-contract.nix injectBlocks), so
	// issue-prompt.md always already contains its own marker -- injection's
	// "if prompt does NOT already contain marker" guard (entrypoint.sh:
	// 632-644) is never true for it. comms/check also list only "fix" in
	// their kinds (never "issue"), confirming they exist to backfill
	// fix-prompt.md, not issue-prompt.md.
	//
	// Every branch's entrypoint.sh equivalent, e.g.
	// `prompt="$(_subst "${PROMPTS_DIR}/issue-prompt.md")"`, is itself
	// inside a $(...) command substitution, so the fully substituted
	// prompt has every trailing newline stripped too -- and nothing
	// re-adds one later: write_prompt_and_run's `printf '%s' "$prompt" >
	// "$_prompt_file"` (entrypoint.sh: 1244) writes $prompt raw, with no
	// appended newline, so Result.Prompt must match that exact on-disk
	// form.
	kind := e.DispatchKind
	if kind == "" {
		kind = defaultDispatchKind
	}

	var baseName, sessionMode string
	switch {
	case kind == "research":
		if e.SelfContained {
			baseName = "research-self-contained-prompt.md"
		} else {
			baseName = "research-prompt.md"
		}
		sessionMode = "initial"
	case e.FixPass > 0:
		baseName = "fix-prompt.md"
		sessionMode = "resume"
	default:
		baseName = "issue-prompt.md"
		sessionMode = "initial"
		if e.ResumeAfterHold {
			sessionMode = "resume"
		}
	}

	basePath := filepath.Join(e.PromptsDir, baseName)
	promptText, err := renderFile(basePath, allowlist)
	if err != nil {
		return Result{}, fmt.Errorf("read %s: %w", baseName, err)
	}

	// Shared-block injection (entrypoint.sh: 1064-1074): the research branch
	// only ever injects research-verdict; every other covered cell injects
	// comms, then check, then outcome, in that order.
	if kind == "research" {
		promptText, err = injectSharedBlock(promptText, e.ResearchOutcomeContractFile, allowlist)
		if err != nil {
			return Result{}, err
		}
	} else {
		for _, contractFile := range []string{e.CommsContractFile, e.CheckContractFile, e.OutcomeContractFile} {
			promptText, err = injectSharedBlock(promptText, contractFile, allowlist)
			if err != nil {
				return Result{}, err
			}
		}
	}

	// Invoker (entrypoint.sh: 1282-1286).
	invoker := "driver-exec"
	if gates["ORCHESTRATOR"] {
		invoker = "orchestrator"
	}

	result := Result{
		Prompt: promptText,
		Handoff: Handoff{
			SessionMode: sessionMode,
			Invoker:     invoker,
		},
	}

	// review_prompt_rendered (entrypoint.sh: 1029-1062): only ever populated
	// on the default fresh-work-dispatch path (kind == "work", FixPass ==
	// 0) and only when the orchestrator is on -- a research dispatch never
	// reviews (ADR 0022), and a warm FIX_PASS box has its own review-less
	// warm-fix flow. review-prompt.md is rendered through the same
	// allowlist as every other file this function reads.
	if gates["ORCHESTRATOR"] && kind == defaultDispatchKind && e.FixPass == 0 {
		reviewPromptPath := filepath.Join(e.PromptsDir, "review-prompt.md")
		reviewPromptText, err := renderFile(reviewPromptPath, allowlist)
		if err != nil {
			return Result{}, fmt.Errorf("read review-prompt.md: %w", err)
		}
		result.Handoff.ReviewPromptFile = reviewPromptText

		// worker_prompt_rendered (issue #2059, #2058): the Go
		// orchestrator's own driver-exec worker prompt, gated and
		// rendered identically to review-prompt.md above.
		workerPromptPath := filepath.Join(e.PromptsDir, "worker-prompt.md")
		workerPromptText, err := renderFile(workerPromptPath, allowlist)
		if err != nil {
			return Result{}, fmt.Errorf("read worker-prompt.md: %w", err)
		}
		result.Handoff.WorkerPromptFile = workerPromptText
	}

	// Agents JSON (entrypoint.sh: 1077-1116). Empty template means no
	// --agents flag at all: Result.AgentsJSON stays "".
	if e.AgentsJSONTemplate != "" {
		agentsTemplate := e.AgentsJSONTemplate

		if gates["ORCHESTRATOR"] {
			// Issue #2277 (entrypoint.sh: 1086-1101): extract the
			// reviewer's own configured model into Handoff.ReviewModel
			// before dropping the reviewer key from the template entirely
			// -- the code-owned review pass replaces the implementor's own
			// inline reviewer subagent, so it's never provisioned into
			// --agents at all, not merely muted.
			var agentsKeys map[string]json.RawMessage
			if err := json.Unmarshal([]byte(agentsTemplate), &agentsKeys); err != nil {
				return Result{}, fmt.Errorf("parse agents json template: %w", err)
			}
			if reviewerRaw, ok := agentsKeys["reviewer"]; ok {
				var reviewer struct {
					Model  string `json:"model"`
					Effort string `json:"effort"`
				}
				// A malformed reviewer entry (not an object, or one with no
				// model/effort field) mirrors jq's `.reviewer.model // empty`
				// and `.reviewer.effort // empty`: Unmarshal error or a
				// zero-value Model/Effort both leave ReviewModel/ReviewEffort
				// at their empty default rather than failing.
				_ = json.Unmarshal(reviewerRaw, &reviewer)
				result.Handoff.ReviewModel = reviewer.Model
				result.Handoff.ReviewEffort = reviewer.Effort
			}
			delete(agentsKeys, "reviewer")
			strippedJSON, err := json.Marshal(agentsKeys)
			if err != nil {
				return Result{}, fmt.Errorf("marshal reviewer-stripped agents json: %w", err)
			}
			agentsTemplate = string(strippedJSON)
		}

		agentsJSON, err := renderAgentsJSON(e, agentsTemplate, allowlist)
		if err != nil {
			return Result{}, err
		}
		result.AgentsJSON = agentsJSON
	}

	// On-disk opencode agent-file rewrite (entrypoint.sh: 1128-1187) -- the
	// file-rewrite twin of the --agents JSON injection loop above, for a
	// Driver whose subagents ride baked agent files instead of the --agents
	// flag. A no-op when DriverAgentFilesDir is unset (claude). Runs after
	// the JSON-path reviewer-drop above: when reviewer.md exists, its
	// frontmatter model overwrites whatever the JSON path already set in
	// result.Handoff.ReviewModel (entrypoint.sh: 1152-1153 runs after 1096
	// and unconditionally overwrites); when it doesn't exist, the JSON-path
	// value (if any) survives unchanged.
	if e.DriverAgentFilesDir != "" {
		if err := rewriteAgentFiles(e, allowlist, gates["ORCHESTRATOR"], &result.Handoff.ReviewModel); err != nil {
			return Result{}, err
		}
	}

	return result, nil
}

// renderAgentsJSON implements entrypoint.sh's generic per-agent injection
// loop (entrypoint.sh: 1105-1116): for every key in agentsTemplate, look up
// its prompt file via AgentsPromptFiles[name], and when PromptsDir/<file>
// exists, substitute it through the same allowlist and set .{name}.prompt
// to the rendered text. agentsTemplate is an explicit parameter, rather
// than e.AgentsJSONTemplate read internally, so the caller can hand this
// function the reviewer-stripped template the orchestrator-on
// del(.reviewer)/model-extraction branch (entrypoint.sh: 1086-1101)
// produces (issue #2353) -- when the orchestrator is off, the caller passes
// e.AgentsJSONTemplate through unmodified, and a reviewer key (if any)
// flows through this loop like any other agent, unchanged from issue
// #2349's original behavior. DriverAgentFilesDir's on-disk agent-files twin
// (entrypoint.sh: 1130+) is a separate loop entirely -- see
// rewriteAgentFiles, called from Assemble after this function returns.
func renderAgentsJSON(e Env, agentsTemplate string, allowlist map[string]string) (string, error) {
	var template map[string]json.RawMessage
	if err := json.Unmarshal([]byte(agentsTemplate), &template); err != nil {
		return "", fmt.Errorf("parse agents json template: %w", err)
	}

	var promptFiles map[string]string
	if e.AgentsPromptFiles != "" {
		if err := json.Unmarshal([]byte(e.AgentsPromptFiles), &promptFiles); err != nil {
			return "", fmt.Errorf("parse agents prompt files: %w", err)
		}
	}

	for name := range template {
		promptFile := promptFiles[name]
		if promptFile == "" {
			continue
		}
		path := filepath.Join(e.PromptsDir, promptFile)
		// entrypoint.sh's equivalent, `_p="$(_subst "${PROMPTS_DIR}/${_pf}")"`
		// (entrypoint.sh: 1121), is itself inside a $(...) command
		// substitution -- trim to match, same as the fragment loop and the
		// base-template substitution above.
		rendered, err := renderFile(path, allowlist)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("read agent prompt file %s: %w", promptFile, err)
		}

		var entry map[string]json.RawMessage
		if err := json.Unmarshal(template[name], &entry); err != nil {
			return "", fmt.Errorf("parse agents json entry %q: %w", name, err)
		}
		if entry == nil {
			entry = map[string]json.RawMessage{}
		}
		renderedJSON, err := json.Marshal(rendered)
		if err != nil {
			return "", fmt.Errorf("marshal rendered prompt for %q: %w", name, err)
		}
		entry["prompt"] = renderedJSON

		entryJSON, err := json.Marshal(entry)
		if err != nil {
			return "", fmt.Errorf("marshal agents json entry %q: %w", name, err)
		}
		template[name] = entryJSON
	}

	out, err := json.Marshal(template)
	if err != nil {
		return "", fmt.Errorf("marshal agents json: %w", err)
	}
	return string(out), nil
}

// frontmatterOf returns every line of data up to and including the second
// "---" fence line -- the same slice awk '{ print } /^---$/ { if (++_c ==
// 2) exit }' produces (entrypoint.sh: 1170, 1177). A file missing a second
// fence (never true for a real opencode-baked agent file, whose
// agentFilesTemplate always emits both fences) falls through to returning
// the entire file with its trailing newline(s) stripped, matching bash's
// own behavior in that case: the awk output there is captured via
// $(...) command substitution, which strips all trailing newlines.
func frontmatterOf(data []byte) string {
	lines := strings.Split(string(data), "\n")
	fences := 0
	for i, line := range lines {
		if line == "---" {
			fences++
			if fences == 2 {
				return strings.Join(lines[:i+1], "\n")
			}
		}
	}
	return strings.TrimRight(string(data), "\n")
}

// reviewerModelFrontmatter extracts the `model:` YAML scalar from a baked
// opencode reviewer.md's frontmatter (entrypoint.sh: 1152-1153: `awk ... |
// sed -n 's/^model: //p' | jq -r '.'`). The baked shape is always a
// double-quoted scalar, e.g. `model: "opus"` -- jq -r unwraps the JSON
// string; TrimPrefix plus a bare quote trim reproduces that here without a
// JSON parse. Returns "" if no `model:` line is present in the frontmatter,
// mirroring sed -n finding no match.
func reviewerModelFrontmatter(frontmatter string) string {
	for _, line := range strings.Split(frontmatter, "\n") {
		if v, ok := strings.CutPrefix(line, "model: "); ok {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

// rewriteAgentFiles implements entrypoint.sh's DRIVER_AGENT_FILES_DIR-gated
// block (entrypoint.sh: 1128-1187), the file-rewrite twin of
// renderAgentsJSON's --agents JSON injection loop for a Driver (opencode)
// whose subagents ride on-disk agent files instead of the --agents JSON
// flag. Callers must only invoke this when e.DriverAgentFilesDir != "" (the
// zero-value early-return this function's caller in Assemble already
// applies).
//
// When orchestratorOn, reviewer.md's `model:` frontmatter scalar overwrites
// *reviewModel (entrypoint.sh: 1152-1153) -- deliberately unconditional, not
// merged with whatever renderAgentsJSON's JSON-path reviewer-drop already
// set, matching bash's sequential assignment -- before the file is removed
// (entrypoint.sh: 1156); a missing reviewer.md leaves *reviewModel
// untouched, mirroring the `[ -f ... ] &&` guard. When orchestratorOn is
// false, neither extraction nor removal happens, matching the bash off-row.
//
// Regardless of orchestratorOn, the generic per-agent rewrite loop
// (entrypoint.sh: 1165-1186) then iterates e.AgentsPromptFiles in sorted key
// order (bash iterates AGENTS_PROMPT_FILES's own key order via jq; sorting
// here trades exact bash parity for Go-map-iteration determinism, since each
// name's rewrite only ever touches its own independent file, so order never
// affects the end state -- see the slice's task description). For each
// name -> promptFile: skip if DriverAgentFilesDir/<name>.md doesn't exist
// (covers both "opencode never baked this file" and "the reviewer file just
// removed above"); skip if PromptsDir/<promptFile> doesn't exist; otherwise
// preserve the agent file's existing frontmatter and overwrite it with
// frontmatter + "\n" + the rendered prompt + "\n" (entrypoint.sh: 1186's
// `printf '%s\n%s\n' "$_af_frontmatter" "$_af_prompt" >"$_af_file"`).
func rewriteAgentFiles(e Env, allowlist map[string]string, orchestratorOn bool, reviewModel *string) error {
	if orchestratorOn {
		reviewerPath := filepath.Join(e.DriverAgentFilesDir, "reviewer.md")
		if data, err := os.ReadFile(reviewerPath); err == nil {
			*reviewModel = reviewerModelFrontmatter(frontmatterOf(data))
			if err := os.Remove(reviewerPath); err != nil {
				return fmt.Errorf("remove %s: %w", reviewerPath, err)
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("read %s: %w", reviewerPath, err)
		}
	}

	var promptFiles map[string]string
	if e.AgentsPromptFiles != "" {
		if err := json.Unmarshal([]byte(e.AgentsPromptFiles), &promptFiles); err != nil {
			return fmt.Errorf("parse agents prompt files: %w", err)
		}
	}

	names := make([]string, 0, len(promptFiles))
	for name := range promptFiles {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		agentFilePath := filepath.Join(e.DriverAgentFilesDir, name+".md")
		agentFileData, err := os.ReadFile(agentFilePath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("read agent file %s: %w", agentFilePath, err)
		}

		promptPath := filepath.Join(e.PromptsDir, promptFiles[name])
		rendered, err := renderFile(promptPath, allowlist)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("read agent prompt file %s: %w", promptFiles[name], err)
		}

		frontmatter := frontmatterOf(agentFileData)
		out := frontmatter + "\n" + rendered + "\n"
		if err := os.WriteFile(agentFilePath, []byte(out), 0o644); err != nil {
			return fmt.Errorf("write agent file %s: %w", agentFilePath, err)
		}
	}

	return nil
}
