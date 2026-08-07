package promptassembly

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ErrUnsupportedCell marks an Env combination Assemble does not (yet) cover.
// Assemble currently implements four cells of agent/entrypoint.sh's
// phase_prompt_assembly — see checkCoveredCell — and errors, wrapping this
// sentinel, for anything outside them rather than silently mis-rendering a
// prompt for a combination it hasn't been built (and tested) against.
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
	// ReviewPromptFile and ReviewModel are only ever populated when Invoker
	// is "orchestrator" (entrypoint.sh: 1294, 1303); Assemble's covered
	// cell always resolves to "driver-exec", so both stay empty here.
	ReviewPromptFile string
	ReviewModel      string
}

// checkCoveredCell validates that e sits in one of the four Env cells
// Assemble implements: IssueTracker/CodeForge both (explicitly or by
// default) github, a read-write box, the orchestrator off, and every skill
// baked (SkillsFound non-empty and all four per-skill gates on) are common
// to all four; the dispatch-kind/fix-pass axis then forks into:
//   - dispatch kind "work" (explicit or default), FixPass == 0 -- a fresh
//     work dispatch (issue-prompt.md).
//   - dispatch kind "work" (explicit or default), FixPass > 0 -- a warm fix
//     pass (fix-prompt.md).
//   - dispatch kind "research", SelfContained == false -- a repo-backed
//     research dispatch (research-prompt.md).
//   - dispatch kind "research", SelfContained == true -- a self-contained
//     research dispatch (research-self-contained-prompt.md).
//
// A DispatchKind that is neither "work" nor "research" is out of scope, as
// is any other combination outside these four cells; each returns an error
// wrapping ErrUnsupportedCell.
func checkCoveredCell(e Env) error {
	tracker := e.IssueTracker
	if tracker == "" {
		tracker = defaultIssueTracker
	}
	if tracker != defaultIssueTracker {
		return fmt.Errorf("issue tracker %q: %w", e.IssueTracker, ErrUnsupportedCell)
	}

	forge := e.CodeForge
	if forge == "" {
		forge = defaultCodeForge
	}
	if forge != defaultCodeForge {
		return fmt.Errorf("code forge %q: %w", e.CodeForge, ErrUnsupportedCell)
	}

	if !e.BoxWriteEnabled {
		return fmt.Errorf("box access is read-only: %w", ErrUnsupportedCell)
	}

	kind := e.DispatchKind
	if kind == "" {
		kind = defaultDispatchKind
	}
	if kind != defaultDispatchKind && kind != "research" {
		return fmt.Errorf("dispatch kind %q: %w", e.DispatchKind, ErrUnsupportedCell)
	}

	if e.OrchestratorEnabled {
		return fmt.Errorf("orchestrator enabled: %w", ErrUnsupportedCell)
	}

	if e.SkillsFound == "" || !e.CavemanSkillBaked || !e.TDDSkillBaked || !e.CommitSkillBaked || !e.CodeReviewSkillBaked {
		return fmt.Errorf("skills not fully baked: %w", ErrUnsupportedCell)
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

	// The _subst allowlist (entrypoint.sh: 405-433): the seven fixed names
	// plus the flat _FRAGMENT_SUBST_VARS list -- every registry row's var
	// and extraSubstVars, concatenated once across all rows (identical for
	// every _subst call in this function, never scoped per-fragment).
	allowlist := map[string]string{
		"ISSUE_NUMBER":      e.IssueNumber,
		"ISSUE_TITLE":       e.IssueTitle,
		"BRANCH":            e.Branch,
		"BASE_BRANCH":       e.BaseBranch,
		"IN_PROGRESS_LABEL": e.InProgressLabel,
		"COMPLETE_LABEL":    e.CompleteLabel,
		"RUN_NONCE":         e.RunNonce,
	}

	// extraSubstVars raw sources: as of issue #2349 the registry carries
	// exactly two (SKILLS_FOUND, CI_FAILURE_SUMMARY -- see fragments.nix's
	// header comment and registry_test.go's TestLoadRegistryParsesAllRows).
	// SKILLS_FOUND's raw value is Env.SkillsFound; CI_FAILURE_SUMMARY is a
	// launcher-forwarded env var Env carries no field for yet -- out of
	// scope for this slice's covered cell, whose CI_FAILURE_SUMMARY gate is
	// always off (Env has no field to turn it on), so it defaults to the
	// zero-value empty string like any other unresolved extraSubstVars name.
	extraRaw := map[string]string{
		"SKILLS_FOUND": e.SkillsFound,
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
				return Result{}, fmt.Errorf("read fragment %s: %w", row.Fragment, err)
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

	// Agents JSON (entrypoint.sh: 1077-1116): orchestrator is off for the
	// covered cell, so the inner del(.reviewer)/model-extraction branch
	// (entrypoint.sh: 1088-1104) never applies -- only the generic
	// per-agent injection loop (entrypoint.sh: 1105-1116) does. Empty
	// template means no --agents flag at all: Result.AgentsJSON stays "".
	if e.AgentsJSONTemplate != "" {
		agentsJSON, err := renderAgentsJSON(e, allowlist)
		if err != nil {
			return Result{}, err
		}
		result.AgentsJSON = agentsJSON
	}

	return result, nil
}

// renderAgentsJSON implements entrypoint.sh's generic per-agent injection
// loop (entrypoint.sh: 1105-1116): for every key in AgentsJSONTemplate,
// look up its prompt file via AgentsPromptFiles[name], and when
// PromptsDir/<file> exists, substitute it through the same allowlist and
// set .{name}.prompt to the rendered text. DriverAgentFilesDir's on-disk
// agent-files twin (entrypoint.sh: 1130+) is out of scope for this slice
// (issue #2349) -- it only matters for a non-claude Driver, so a non-empty
// value is silently ignored here, not an error.
func renderAgentsJSON(e Env, allowlist map[string]string) (string, error) {
	var template map[string]json.RawMessage
	if err := json.Unmarshal([]byte(e.AgentsJSONTemplate), &template); err != nil {
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
