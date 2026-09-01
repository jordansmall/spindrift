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
// render: an unrecognized DispatchKind value. Every other axis is handled by
// Assemble's own logic regardless of how the others are set, so no
// combination of them is rejected.
var ErrUnsupportedCell = errors.New("promptassembly: env combination not covered by Assemble")

// Result is Assemble's rendered output: prompt text, the (possibly empty)
// --agents JSON, the review-prompt text when rendered, and hand-off facts.
type Result struct {
	Prompt     string
	AgentsJSON string
	// ReviewPromptText is the rendered review-prompt.md body, populated under
	// the same condition as Handoff.ReviewPromptFile. It lives on Result
	// rather than Handoff because it is rendered TEXT, not a path.
	ReviewPromptText string
	Handoff          Handoff
}

// ArgvShape describes how a caller assembles the Driver's argv: which flag
// spells each input, whether the model flag is omitted entirely when Model is
// empty (some Drivers reject an empty --model rather than treating it as "use
// default"), and the flag order the Driver's CLI parser requires. A pure
// passthrough -- Assemble never populates it.
type ArgvShape struct {
	PromptStyle    string
	PromptFlag     string
	ModelFlag      string
	ModelOmitEmpty bool
	AgentsFlag     string
	EffortFlag     string
	Order          []string
}

// Caps carries the per-run resource ceilings an orchestrator invocation
// enforces across the whole run. Like ArgvShape, Assemble never populates it.
type Caps struct {
	MaxSlices       int
	MaxReviewRounds int
	MaxBudgetTokens int
	MaxBudgetUSD    float64
}

// Handoff is the static per-run configuration assemble-prompt hands to a
// driver-exec/orchestrator invocation, written to disk as JSON so a process
// that starts after assemble-prompt exits can consume it without re-deriving
// anything. Assemble itself sets only SessionMode, Invoker, ReviewModel, and
// ReviewEffort; every other field is a pure passthrough the CLI wrapper
// populates from flags/static config, never from Assemble's Env/gate logic.
type Handoff struct {
	// SessionMode is "resume" or "initial".
	SessionMode string
	// Invoker is "orchestrator" or "driver-exec".
	Invoker string
	// PromptFile, AgentsFile, and ReviewPromptFile are the on-disk paths the
	// CLI wrapper writes Result.Prompt, Result.AgentsJSON, and
	// Result.ReviewPromptText to. Assemble renders text, not paths, so it
	// leaves all three at their zero value.
	PromptFile       string
	AgentsFile       string
	ReviewPromptFile string
	// ReviewModel and ReviewEffort are extracted from AgentsJSONTemplate's
	// "reviewer" key whenever Invoker is "orchestrator" -- unconditional on
	// dispatch kind and FixPass. Both stay empty for "driver-exec", or when
	// the template carries no reviewer entry with that field.
	ReviewModel  string
	ReviewEffort string
	// The remaining fields are pure passthrough the CLI wrapper populates
	// directly; Devshell/DevshellName gate whether the Driver runs inside a
	// Nix devShell wrapper, and which one.
	Model        string
	Effort       string
	Driver       string
	DriverBin    string
	DriverFlags  string
	Devshell     bool
	DevshellName string
	Issue        string
	HeartbeatLog string
	ArgvShape    ArgvShape
	Caps         Caps
}

// checkCoveredCell validates that e sits in one of Assemble's covered Env
// cells. Only DispatchKind is checked, against a fixed allowlist of "work"
// (explicit or default) or "research": it is set programmatically at runtime
// by applyDispatchKind (cmd/launcher/main.go) and so has no schema entry
// guarding it upstream. IssueTracker and CodeForge are deliberately not
// re-checked here -- lib/mkHarness.nix eval-asserts their schema `choices` at
// build time, and cmd/launcher/main.go's validate() checks them host-side at
// launcher startup, before the Box that runs Assemble even exists.
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

// substTokenRe matches the braced ${NAME} substitution form; every template
// and fragment under templates/default/prompts spells its substitution
// variables this way, never bare $NAME.
var substTokenRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// substitute replaces every ${NAME} whose NAME is a key of allowlist with its
// value; anything else -- an unlisted ${OTHER} or a bare $ -- passes through
// untouched. A single ReplaceAllStringFunc pass over the original text
// (rather than sequential per-name replacement) guarantees a substituted
// value that itself contains ${NAME}-shaped text is never re-expanded.
func substitute(text string, allowlist map[string]string) string {
	return substTokenRe.ReplaceAllStringFunc(text, func(tok string) string {
		name := tok[2 : len(tok)-1]
		if v, ok := allowlist[name]; ok {
			return v
		}
		return tok
	})
}

// RenderText substitutes every ${NAME} token in text through vars, then trims
// trailing newlines the way renderFile does for a file's contents. Exported
// so a caller that needs this substitution mechanism -- but not the rest of
// Assemble's Env-driven pipeline -- can reuse it rather than hand-rolling a
// bespoke strings.ReplaceAll pass.
func RenderText(text string, vars map[string]string) string {
	return strings.TrimRight(substitute(text, vars), "\n")
}

// renderFile reads path, substitutes it through allowlist, and strips all
// trailing newlines -- every caller depends on that trim, so it lives here
// rather than at each call site.
func renderFile(path string, allowlist map[string]string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(substitute(string(data), allowlist), "\n"), nil
}

// injectSharedBlock appends contractPath's rendered body to prompt, separated
// by a blank line. An empty contractPath is a silent no-op -- a covered cell
// only populates the contract-file Env fields it actually needs. Injection is
// idempotent: the block's first line, the marker each contract file is
// pre-sliced to start with (e.g. "# COMMS"), is checked against prompt, and an
// already-present marker returns prompt unchanged.
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
// hand-off facts (see checkCoveredCell for the covered cells). Any Env
// outside those cells is rejected up front, before any file I/O, with an
// error wrapping ErrUnsupportedCell.
func Assemble(e Env, reg Registry) (Result, error) {
	if err := checkCoveredCell(e); err != nil {
		return Result{}, err
	}

	gates := Gates(e)
	// SKILLS_FOUND is filesystem-derived, so Gates never computes it (I/O is
	// out of its scope -- see env.go's package doc).
	gates["SKILLS_FOUND"] = e.SkillsFound != ""

	// The substitution allowlist: eight fixed names plus every registry row's
	// var and extraSubstVars, concatenated once across all rows rather than
	// scoped per-fragment.
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

	// Raw sources for the registry's extraSubstVars (see fragments.nix).
	// CI_FAILURE_SUMMARY's own presence is also its gate.
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

	// For each row, in registry order: render its fragment when its gate is on
	// and assign the rendered text + "\n\n" to its var, empty when off. The
	// blank-line separator is appended here rather than baked into the
	// fragment file because renderFile strips all trailing newlines.
	for _, row := range reg.Rows {
		if gates[row.Gate] {
			path := filepath.Join(e.PromptsDir, "fragments", row.Fragment)
			rendered, err := renderFile(path, allowlist)
			if err != nil {
				// A missing fragment file resolves to an empty string rather
				// than aborting; any other read error (e.g. permission
				// denied) still hard-fails.
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

	// Base-template selection and session mode, in precedence order: research
	// first (regardless of FixPass), then a warm fix pass, then the default
	// work/issue cell.
	//
	// For the work/issue-prompt.md cell, the shared-block injection below is
	// a no-op: outcome's "# LAND THE CHANGE", comms's "# COMMS", and check's
	// "# CHECK" markers are all sliced FROM issue-prompt.md itself
	// (lib/prompt-contract.nix injectBlocks), so its already-present-marker
	// guard is never true. comms/check exist to backfill fix-prompt.md.
	//
	// Result.Prompt carries no trailing newline: it is written to disk raw,
	// with nothing appended.
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

	// The research branch only ever injects research-verdict; every other
	// covered cell injects comms, code comments, check, then outcome.
	if kind == "research" {
		promptText, err = injectSharedBlock(promptText, e.ResearchOutcomeContractFile, allowlist)
		if err != nil {
			return Result{}, err
		}
	} else {
		for _, contractFile := range []string{e.CommsContractFile, e.CodeCommentsContractFile, e.CheckContractFile, e.OutcomeContractFile} {
			promptText, err = injectSharedBlock(promptText, contractFile, allowlist)
			if err != nil {
				return Result{}, err
			}
		}
	}

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

	// The review prompt is rendered only on the default fresh-work-dispatch
	// path with the orchestrator on: a research dispatch never reviews
	// (ADR 0022), and a warm FIX_PASS box has its own review-less flow.
	if gates["ORCHESTRATOR"] && kind == defaultDispatchKind && e.FixPass == 0 {
		reviewPromptPath := filepath.Join(e.PromptsDir, "review-prompt.md")
		reviewPromptText, err := renderFile(reviewPromptPath, allowlist)
		if err != nil {
			return Result{}, fmt.Errorf("read review-prompt.md: %w", err)
		}
		result.ReviewPromptText = reviewPromptText
	}

	// An empty template means no --agents flag at all: AgentsJSON stays "".
	if e.AgentsJSONTemplate != "" {
		agentsTemplate := e.AgentsJSONTemplate

		if gates["ORCHESTRATOR"] {
			// Extract the reviewer's configured model before dropping the
			// reviewer key entirely -- the code-owned review pass replaces
			// the implementor's inline reviewer subagent, so it is never
			// provisioned into --agents at all, not merely muted (#2277).
			var agentsKeys map[string]json.RawMessage
			if err := json.Unmarshal([]byte(agentsTemplate), &agentsKeys); err != nil {
				return Result{}, fmt.Errorf("parse agents json template: %w", err)
			}
			if reviewerRaw, ok := agentsKeys["reviewer"]; ok {
				var reviewer struct {
					Model  string `json:"model"`
					Effort string `json:"effort"`
				}
				// A malformed or field-less reviewer entry leaves
				// ReviewModel/ReviewEffort empty rather than failing.
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

	// The file-rewrite twin of the --agents JSON injection above, for a Driver
	// whose subagents ride baked agent files instead of the --agents flag. A
	// no-op when DriverAgentFilesDir is unset (claude). Runs after the
	// reviewer-drop above: an existing reviewer.md's frontmatter model
	// overwrites whatever the JSON path already set in ReviewModel; with no
	// reviewer.md the JSON-path value survives unchanged.
	if e.DriverAgentFilesDir != "" {
		if err := rewriteAgentFiles(e, allowlist, gates["ORCHESTRATOR"], &result.Handoff.ReviewModel); err != nil {
			return Result{}, err
		}
	}

	return result, nil
}

// renderAgentsJSON injects per-agent prompts into agentsTemplate: for every
// key, look up its prompt file via AgentsPromptFiles[name] and, when
// PromptsDir/<file> exists, set .{name}.prompt to the rendered text.
// agentsTemplate is an explicit parameter rather than read from
// e.AgentsJSONTemplate so the caller can hand in the reviewer-stripped
// template the orchestrator-on branch produces; with the orchestrator off, a
// reviewer key flows through this loop like any other agent. The on-disk
// agent-files twin is rewriteAgentFiles, called after this function returns.
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
// "---" fence line. A file missing a second fence (never true for a real
// opencode-baked agent file, whose agentFilesTemplate always emits both)
// falls back to the entire file with trailing newlines stripped.
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
// opencode reviewer.md's frontmatter. The baked shape is always a
// double-quoted scalar, e.g. `model: "opus"`, so a prefix cut plus a quote
// trim stands in for a JSON parse. Returns "" when no `model:` line is
// present.
func reviewerModelFrontmatter(frontmatter string) string {
	for _, line := range strings.Split(frontmatter, "\n") {
		if v, ok := strings.CutPrefix(line, "model: "); ok {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

// rewriteAgentFiles is the file-rewrite twin of renderAgentsJSON, for a
// Driver (opencode) whose subagents ride on-disk agent files instead of the
// --agents JSON flag. Callers must only invoke it when
// e.DriverAgentFilesDir != "".
//
// When orchestratorOn, reviewer.md's `model:` frontmatter scalar
// unconditionally overwrites *reviewModel -- deliberately not merged with
// whatever renderAgentsJSON already set -- before the file is removed; a
// missing reviewer.md leaves *reviewModel untouched. When orchestratorOn is
// false, neither extraction nor removal happens.
//
// The rewrite loop iterates e.AgentsPromptFiles in sorted key order for
// determinism; each name's rewrite only ever touches its own file, so order
// never affects the end state. A name is skipped when either its agent file
// (covering both "never baked" and "the reviewer file just removed above") or
// its prompt file is missing; otherwise the agent file's existing frontmatter
// is preserved and the rendered prompt written under it.
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
