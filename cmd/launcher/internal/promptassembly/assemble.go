package promptassembly

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/forge"
)

// ErrUnsupportedCell marks an Env combination Assemble cannot render: an
// unrecognized DispatchKind value. Every other axis is handled regardless of
// how the others are set, so no combination of them is rejected.
var ErrUnsupportedCell = errors.New("promptassembly: env combination not covered by Assemble")

// Result is Assemble's rendered output: the final prompt text, the completed
// --agents JSON, the review-prompt text when rendered, and the driver
// hand-off facts.
type Result struct {
	Prompt     string
	AgentsJSON string
	// ReviewPromptText is the rendered review-prompt.md body, set only on a
	// fresh-work, non-AdviseOnly dispatch (FixPass == 0). It lives on Result,
	// not Handoff, because Handoff serializes paths, not text (issue #2975).
	ReviewPromptText string
	Handoff          Handoff
	// Fragments is the sorted, de-duplicated set of lib/fragments.nix
	// fragment names whose bytes reach Prompt, ReviewPromptText, or an agent
	// prompt in AgentsJSON. It is the per-cell coverage record the
	// prompt-assembly golden suite pins (issue #3838).
	Fragments []string

	// fragmentRenders maps each Fragments name to its own rendered prose in
	// every body it reached: its literal text and the vars substituted directly
	// into it, with nested fragments and intervening text left out.
	// Tests read it through FragmentRenders to check a clause against one
	// fragment's own prose rather than the whole prompt.
	fragmentRenders map[string][]string

	// removedVarErr is the first removed fragment variable Assemble found in
	// an operator-authored segment of a rendered prompt; Validate reports it.
	// Deferred rather than returned so it surfaces as a *ValidateError via
	// WriteAssembly (printed bare as operator prose); skipping Validate drops it.
	removedVarErr error

	// deprecatedVarWarnings holds one message per deprecated fragment variable
	// an operator-authored segment still references; Validate returns them.
	deprecatedVarWarnings []string
}

// ArgvShape describes how the CLI wrapper assembles the Driver's argv: which
// flag spells each input, whether the model flag is omitted when Model is
// empty (some Drivers reject an empty --model rather than defaulting), and
// the flag order the Driver's parser requires. Assemble never populates this;
// the wrapper fills it from per-Driver static configuration (issue #2975).
type ArgvShape struct {
	PromptStyle    string   // "flag" or "positional"
	PromptFlag     string   // meaningful only when PromptStyle == "flag"
	ModelFlag      string   // spells the model slot
	ModelOmitEmpty bool     // when true, omit the model slot entirely if Handoff.Model == ""
	AgentsFlag     string   // "" means this Driver has no --agents equivalent
	EffortFlag     string   // the effort slot is always omitted when Handoff.Effort == ""
	Order          []string // permutation of {"prompt","model","agents","session","driverFlags","effort"}
}

// Caps carries the per-run resource ceilings an orchestrator invocation
// enforces across the whole run. Assemble never populates this; the CLI
// wrapper does (issue #2975).
type Caps struct {
	MaxSlices       int
	MaxReviewRounds int
	MaxBudgetTokens int
	MaxBudgetUSD    float64
}

// Handoff is the static per-run configuration assemble-prompt hands to a
// driver-exec/orchestrator invocation, written to disk as JSON so a process
// starting after assemble-prompt exits re-derives nothing. Assemble sets only
// SessionMode, ReviewModel, ReviewEffort, and AdvisoryReviewer; the
// CLI wrapper populates every other field from flags and static config
// (issue #2975).
type Handoff struct {
	// SessionMode is "resume" or "initial", chosen by
	// assemblePromptBodiesMasked's base-template switch.
	SessionMode string
	// PromptFile is the path the CLI wrapper writes Result.Prompt to. Assemble
	// never sets it: it renders the text, the wrapper picks the path.
	PromptFile string
	// AgentsFile is the path the CLI wrapper writes Result.AgentsJSON to.
	AgentsFile string
	// ReviewPromptFile is the path the CLI wrapper writes
	// Result.ReviewPromptText to (issue #2975). Assemble leaves it zero in
	// every cell; the wrapper sets it only when ReviewPromptText is non-empty.
	ReviewPromptFile string
	// ReviewModel is extracted from AgentsJSONTemplate's "reviewer" key
	// regardless of dispatch kind or FixPass. It stays empty when the template
	// has no reviewer model, mirroring jq's `.reviewer.model // empty`. Under a
	// Driver with on-disk agent files (DRIVER_AGENT_FILES_DIR) the baked
	// reviewer.md `model:` overwrites it (issue #5350). An explicit
	// dispatch-time REVIEW_MODEL (issue #3171) binds over both last.
	ReviewModel string
	// ReviewEffort mirrors ReviewModel for the reviewer key's "effort" field,
	// with reviewer.md's `reasoningEffort:` and REVIEW_EFFORT binding over it
	// in the same order.
	ReviewEffort string
	// AdvisoryReviewer is true when the kind supplies its own reviewer prompt
	// (butler, ADR 0056): its inline reviewer only advises, so its verdict
	// must not steer the pass loop (issue #3925).
	AdvisoryReviewer bool
	// Model, Effort, Driver, DriverBin, and DriverFlags are the Driver
	// invocation's static configuration, never derived from Env/gate logic.
	Model       string
	Effort      string
	Driver      string
	DriverBin   string
	DriverFlags string
	// DriverRowsFile is the baked Driver-rows JSON the in-box binaries apply
	// before resolving Driver; empty when the Harness has no outside Driver.
	DriverRowsFile string
	// Devshell and DevshellName gate whether the Driver runs inside a Nix
	// devShell wrapper, and which one.
	Devshell     bool
	DevshellName string
	Issue        string
	HeartbeatLog string
	ArgvShape    ArgvShape
	Caps         Caps
}

// checkCoveredCell validates DispatchKind, which has no schema entry to guard
// it, plus SelfContained against the kind's Prompts. IssueTracker and
// CodeForge are validated upstream by mkHarness's choicesCheckOk and main.go's
// validate() (issue #2540); no other axis combination is rejected (#2354).
func checkCoveredCell(e Env) error {
	d, ok := dispatchkind.ByName(e.kind())
	if !ok {
		return fmt.Errorf("dispatch kind %q: %w", e.DispatchKind, ErrUnsupportedCell)
	}

	// A kind with its own base prompt but no SelfContainedBase has nothing to
	// render under SelfContained=true; main.go's validate() rejects this
	// combination on the CLI path, but the box env path calls Assemble
	// directly, so it needs its own gate here too.
	if e.SelfContained && d.Prompts.Base != "" && d.Prompts.SelfContainedBase == "" {
		return fmt.Errorf("dispatch kind %q: self-contained not supported: %w", d.Name, ErrUnsupportedCell)
	}

	return nil
}

const identPattern = `[A-Za-z_][A-Za-z0-9_]*`

// substTokenRe matches the braced ${NAME} form envsubst recognizes. Every
// template and fragment under templates/default/prompts references its
// variables this way, never bare $NAME (verified against the tree, #2349).
var substTokenRe = regexp.MustCompile(`\$\{(` + identPattern + `)\}`)

// bareTokenRe matches the braced form or a bare $NAME, which takes the whole
// identifier: $BRANCHX is the name BRANCHX, not BRANCH followed by X.
var bareTokenRe = regexp.MustCompile(`\$(?:\{(` + identPattern + `)\}|(` + identPattern + `))`)

// substituteWith replaces every token re matches whose name is a key of vars;
// anything else passes through untouched. The single ReplaceAllStringFunc
// pass, rather than sequential per-name replacement, keeps a substituted value
// that itself contains $NAME-shaped text from being re-expanded.
func substituteWith(re *regexp.Regexp, text string, vars map[string]string) string {
	return re.ReplaceAllStringFunc(text, func(tok string) string {
		if v, ok := vars[tokenName(tok)]; ok {
			return v
		}
		return tok
	})
}

// tokenName returns the NAME in a $NAME or ${NAME} token.
func tokenName(tok string) string {
	return strings.TrimSuffix(strings.TrimPrefix(tok[1:], "{"), "}")
}

// substitute replaces every braced ${NAME} that is a key of vars.
func substitute(text string, vars map[string]string) string {
	return substituteWith(substTokenRe, text, vars)
}

// RenderText substitutes the ${NAME} tokens in text through vars and trims
// trailing newlines, as renderFileSegments does for a file. bare also
// substitutes $NAME (GNU envsubst parity), only for Consumer PROMPTS_DIR
// overrides of conflict-resolve-prompt.md that use a bare $BASE_BRANCH; the
// main path stays braces-only because the default prompts carry literal shell
// text such as `echo $CODE_FORGE`.
func RenderText(text string, vars map[string]string, bare bool) string {
	re := substTokenRe
	if bare {
		re = bareTokenRe
	}
	return strings.TrimRight(substituteWith(re, text, vars), "\n")
}

// injectSharedBlockSegments appends the rendered contract file to prompt,
// separated by a blank line. An empty contractPath is a silent no-op: a cell
// only populates the contract-file Env fields it needs. The block's first
// line is the marker each contract file is pre-sliced to start with, e.g.
// "# COMMS"; if prompt already contains it, injection is skipped.
func injectSharedBlockSegments(prompt body, contractPath string, vars map[string]body) (body, error) {
	if contractPath == "" {
		return prompt, nil
	}

	contractSource := Source{Kind: SourceContract, Name: filepath.Base(contractPath)}
	block, err := renderFileSegments(contractPath, contractSource, vars)
	if err != nil {
		return nil, fmt.Errorf("read contract file %s: %w", contractPath, err)
	}

	blockText := block.text()
	marker := blockText
	if idx := strings.IndexByte(blockText, '\n'); idx != -1 {
		marker = blockText[:idx]
	}

	if strings.Contains(prompt.text(), marker) {
		return prompt, nil
	}

	out := make(body, 0, len(prompt)+1+len(block))
	out = append(out, prompt...)
	out = append(out, segment{src: contractSource, text: "\n\n"})
	out = append(out, block...)
	return out, nil
}

// renderFileSegments reads path, substitutes it through vars, and trims the
// trailing newlines a $(...) command substitution would strip; every call
// site relies on this one trim. It trims segments, not the joined string, to
// keep per-segment attribution.
func renderFileSegments(path string, owner Source, vars map[string]body) (body, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return renderSegments(string(data), owner, vars).trimTrailingNewlines(), nil
}

// varBody is the attributed body substitution variable name renders to. An
// empty value yields an empty body rather than a segment carrying "", so no
// zero-byte source reaches the composition report.
func varBody(name, value string) body {
	if value == "" {
		return body{}
	}
	return body{{src: Source{Kind: SourceVar, Name: name}, text: value}}
}

type promptBodies struct {
	base        body
	baseName    string
	review      body   // nil when the cell renders no review prompt
	reviewName  string // basename review was rendered from; "" when review is nil
	sessionMode string
	vars        map[string]body
	gates       map[string]bool
	kind        *dispatchkind.Descriptor
}

// assemblePromptBodies renders Assemble's prompt and review-prompt bodies in
// attributed segment form. It also returns vars, gates, and kind, so the
// agent-prompt renders substitute through the same attributed vars and their
// fragment sources reach Result.Fragments.
func assemblePromptBodies(e Env, reg Registry) (promptBodies, error) {
	return assemblePromptBodiesMasked(e, reg, false)
}

// perDispatchVars are the substitution vars whose value is a fact that
// differs per Dispatch, not per harness setup. TemplateHashes masks exactly
// these, so a hash moves only when the template or its setup does.
var perDispatchVars = []string{
	"ISSUE_NUMBER", "ISSUE_TITLE", "ISSUE_TEXT", "BRANCH", "DISPATCH_KEY", "RUN_NONCE",
	"CI_FAILURE_SUMMARY",
	"CHORE_HEAD", "CHORE_DIFF_RANGE", "CHORE_SLICE", "CHORE_CLASSES", "CHORE_PATCH_CLASSES", "CHORE_MAX_FINDINGS",
	choreInputVar,
}

// setupVars are the complement of perDispatchVars: substitution vars whose
// value is a fact of the harness setup. Declared explicitly so a new var's
// author must pick a list; only TestSubstitutionVarsAreClassified reads it,
// and it enforces that every substitution var is in exactly one list.
var setupVars = []string{
	"BASE_BRANCH", "IN_PROGRESS_LABEL", "COMPLETE_LABEL",
	"CHORE_NAME", "CHORE_CLASS_LIST", chorePromptVar,
	"SKILLS_FOUND", "REVIEW_FANOUT_AGENT", forgeDisplayNameVar,
	"REPO_SLUG",
}

// forgeDisplayNameVar is the ${FORGE_DISPLAY_NAME} substitution: the display
// name of the backend whose folder the row being rendered resolved to (issue
// #5647). On tracker-axis and plain rows that is the tracker arm's backend,
// not necessarily the forge: jira renders "Jira", its own read signal's arm. It
// is rebound before every row, so extraSubstRaw carries only the
// initial value a base template sees.
const forgeDisplayNameVar = "FORGE_DISPLAY_NAME"

// rowDisplayName is the DisplayName of the backend folder row resolves to; a
// plain row takes the tracker read axis's. A folder with no registered
// backend falls back to the folder name.
func rowDisplayName(row FragmentRow, e Env) (string, error) {
	folder, err := row.axisFolder(e)
	if err != nil {
		return "", err
	}
	if folder == "" {
		return trackerReadDisplayName(e), nil
	}
	return folderDisplayName(folder), nil
}

// folderDisplayName is the DisplayName of the backend named folder, or folder
// itself when none is registered.
func folderDisplayName(folder string) string {
	if d, ok := backend.ByName(folder); ok && d.DisplayName != "" {
		return d.DisplayName
	}
	return folder
}

// trackerReadDisplayName is the display name of the tracker read axis's arm.
func trackerReadDisplayName(e Env) string {
	read, _ := trackerReadFilerSignals(e)
	return folderDisplayName(signalFolder(read))
}

// extraSubstRaw is the raw source of each extraSubstVars substitution var.
// CI_FAILURE_SUMMARY's field also drives its own gate, since its presence is
// the gate (issue #2354). REVIEW_FANOUT_AGENT is resolved from the run's
// provisioned agents (issue #3447).
func extraSubstRaw(e Env) map[string]string {
	return map[string]string{
		"SKILLS_FOUND":        e.SkillsFound,
		"CI_FAILURE_SUMMARY":  e.CIFailureSummary,
		"REVIEW_FANOUT_AGENT": reviewFanoutAgentFor(e),
		forgeDisplayNameVar:   initialDisplayName(e),
	}
}

// maskPlaceholder is the fixed stand-in for a masked per-Dispatch var.
func maskPlaceholder(name string) string { return "${" + name + "}" }

// assemblePromptBodiesMasked is assemblePromptBodies with an optional mask:
// when mask is set every perDispatchVars value is replaced by its placeholder
// before any fragment renders. Masking acts on vars, never on Env, because
// Env drives the gates and ChoreName is a lookup key.
func assemblePromptBodiesMasked(e Env, reg Registry, mask bool) (promptBodies, error) {
	gates := Gates(e)
	// SKILLS_FOUND is a filesystem-derived presence gate Gates never computes,
	// because I/O is out of its scope.
	gates["SKILLS_FOUND"] = e.SkillsFound != ""

	// One flat var set shared by every render here, not scoped per fragment.
	scalars := map[string]string{
		"ISSUE_NUMBER":      e.IssueNumber,
		"ISSUE_TITLE":       e.IssueTitle,
		"BRANCH":            e.Branch,
		"BASE_BRANCH":       e.BaseBranch,
		"IN_PROGRESS_LABEL": e.InProgressLabel,
		"COMPLETE_LABEL":    e.CompleteLabel,
		"RUN_NONCE":         e.RunNonce,
		"DISPATCH_KEY":      e.DispatchKey,
		"REPO_SLUG":         e.RepoSlug,
	}

	// Each var is an attributed body, so a fragment referencing an earlier
	// var carries that var's attribution through rather than absorbing its
	// bytes.
	vars := make(map[string]body, len(scalars))
	for k, v := range scalars {
		vars[k] = varBody(k, v)
	}

	// ISSUE_TEXT (issue #3445) stays out of scalars above: its value is
	// Go-derived (issueTextSection's fenced text, never e.IssueText raw),
	// while scalars mirrors the raw-env substitution list byte for byte.
	issueSection := issueTextSection(e)
	if mask {
		switch e.descriptor().Keying {
		case dispatchkind.ByChore:
			// A chore dispatch carries no issue facts to mask.
		default:
			// Unconditional, so the hash does not depend on whether this
			// Dispatch happened to carry issue text. The fixed section prose
			// stays; only the interpolated facts are masked.
			me := e
			me.IssueNumber = maskPlaceholder("ISSUE_NUMBER")
			me.IssueText = maskPlaceholder("ISSUE_TEXT")
			issueSection = issueTextSection(me)
		}
	}
	vars["ISSUE_TEXT"] = varBody("ISSUE_TEXT", issueSection)

	// CHORE_PROMPT (ADR 0056, issue #3875) is embedded into butler-prompt.md's
	// body rather than appended like ISSUE_TEXT: it is host-authored, not
	// untrusted issue prose. A missing chores/<name>.md fails assembly, unlike
	// ISSUE_TEXT's silent-empty default.
	chorePrompt, err := choreSection(e)
	if err != nil {
		return promptBodies{}, err
	}
	vars[chorePromptVar] = varBody(chorePromptVar, chorePrompt)

	// CHORE_INPUT is the host-rendered Chore input (the tuning digest), the
	// fenced section butler-prompt.md splices in right after ${CHORE_PROMPT}.
	// choreInputSection carries its own leading separator, so a code Chore
	// (empty input) renders byte-identically to a template without the token.
	inputSection := choreInputSection(e)
	if mask && e.ChoreInput != "" {
		// Only when this Dispatch carries an input: a code Chore never does,
		// and masking unconditionally would add the section's fixed prose to
		// its hash. The prose stays; only the digest is masked.
		me := e
		me.ChoreInput = maskPlaceholder(choreInputVar)
		inputSection = choreInputSection(me)
	}
	vars[choreInputVar] = varBody(choreInputVar, inputSection)

	scalars["CHORE_NAME"] = e.ChoreName
	scalars["CHORE_HEAD"] = e.ChoreHead
	scalars["CHORE_DIFF_RANGE"] = e.ChoreDiffRange
	scalars["CHORE_SLICE"] = e.ChoreSlice
	scalars["CHORE_CLASSES"] = e.ChoreClasses
	scalars["CHORE_CLASS_LIST"] = e.ChoreClassList
	scalars["CHORE_PATCH_CLASSES"] = e.ChorePatchClasses
	scalars["CHORE_MAX_FINDINGS"] = e.ChoreMaxFindings
	for _, k := range []string{"CHORE_NAME", "CHORE_HEAD", "CHORE_DIFF_RANGE", "CHORE_SLICE", "CHORE_CLASSES", "CHORE_CLASS_LIST", "CHORE_PATCH_CLASSES", "CHORE_MAX_FINDINGS"} {
		vars[k] = varBody(k, scalars[k])
	}

	extraRaw := extraSubstRaw(e)
	seenExtra := map[string]bool{}
	for _, row := range reg.Rows {
		for _, extra := range row.ExtraSubstVars {
			if seenExtra[extra] {
				continue
			}
			seenExtra[extra] = true
			v := extraRaw[extra]
			vars[extra] = varBody(extra, v)
		}
	}

	if mask {
		for _, k := range perDispatchVars {
			// ISSUE_TEXT and CHORE_INPUT already hold their masked sections.
			if _, ok := vars[k]; !ok || k == "ISSUE_TEXT" || k == choreInputVar {
				continue
			}
			vars[k] = varBody(k, maskPlaceholder(k))
		}
	}

	// The "\n\n" separator is appended here, not baked into the fragment
	// file, because command substitution strips trailing newlines. Rows with
	// exclusive gates may share one var (issue #6344), so an off or empty row
	// must not clobber a binding an on-gate row already made.
	bound := map[string]bool{}
	for _, row := range reg.Rows {
		fragName, err := row.resolvedFragment(e)
		if err != nil {
			return promptBodies{}, err
		}
		displayName, err := rowDisplayName(row, e)
		if err != nil {
			return promptBodies{}, err
		}
		vars[forgeDisplayNameVar] = varBody(forgeDisplayNameVar, displayName)
		fragSource := Source{Kind: SourceFragment, Name: fragName}
		// An ungated row (empty Gate) renders unconditionally, so the
		// orchestrator fragments need no switch gate.
		if row.Gate == "" || gates[row.Gate] {
			var rendered body
			// Each fallback is read only when the one before is absent;
			// fragName and fragSource then name the file actually read.
			candidates := []string{fragName}
			if row.Axis != "" && row.Shared {
				candidates = append(candidates, row.Fragment)
			}
			if row.LegacyFragment != "" {
				candidates = append(candidates, row.LegacyFragment)
			}
			for _, name := range candidates {
				fragName = name
				fragSource = Source{Kind: SourceFragment, Name: name}
				rendered, err = renderFileSegments(filepath.Join(e.PromptsDir, "fragments", name), fragSource, vars)
				if !errors.Is(err, os.ErrNotExist) {
					break
				}
			}
			if err != nil {
				// A missing fragment renders empty rather than aborting, as
				// the bash original's printf-argument substitution never
				// tripped `set -e`; any other read error fails hard.
				if !errors.Is(err, os.ErrNotExist) {
					return promptBodies{}, fmt.Errorf("read fragment %s: %w", fragName, err)
				}
				if !bound[row.Var] {
					vars[row.Var] = body{}
				}
				continue
			}
			fragBody := append(body{}, rendered...)
			if !row.Inline {
				fragBody = append(fragBody, segment{src: fragSource, text: "\n\n"})
			}
			vars[row.Var] = fragBody
			bound[row.Var] = true
		} else if !bound[row.Var] {
			vars[row.Var] = body{}
		}
	}

	// The last row's binding must not leak into the base templates.
	vars[forgeDisplayNameVar] = varBody(forgeDisplayNameVar, initialDisplayName(e))

	// Base template selection and session mode, in precedence order: research
	// first regardless of FixPass, then a warm fix pass, then the default
	// work cell. Result.Prompt must carry no trailing newline, because the
	// writer prints it raw and nothing re-adds one downstream.
	d := e.descriptor()

	var baseName, sessionMode string
	switch {
	case d.Prompts.Base != "":
		if e.SelfContained {
			baseName = d.Prompts.SelfContainedBase
		} else {
			baseName = d.Prompts.Base
		}
		sessionMode = "initial"
	case e.FixPass > 0:
		baseName = d.Prompts.Fix
		sessionMode = "resume"
	default:
		baseName = d.Prompts.Issue
		sessionMode = "initial"
		if e.ResumeAfterHold {
			sessionMode = "resume"
		}
	}

	if baseName == "" {
		return promptBodies{}, fmt.Errorf("kind %s has no prompt", d.Name)
	}
	basePath := filepath.Join(e.PromptsDir, baseName)
	baseSource := Source{Kind: SourceTemplate, Name: baseName}
	baseText, err := os.ReadFile(basePath)
	if err != nil {
		return promptBodies{}, fmt.Errorf("read %s: %w", baseName, err)
	}
	// A prompt-dir override ships the raw research markers the baked prompt
	// has rendered at nix eval time (issues #2630, #4159). Parsing only when a
	// target is present keeps a malformed RESEARCH_VERDICTS from failing a
	// work or butler assembly, which never reads it.
	if forge.HasVerdictTargets(string(baseText)) {
		researchVerdicts, err := forge.ParseResearchVerdicts(e.ResearchVerdicts)
		if err != nil {
			return promptBodies{}, fmt.Errorf("research verdicts: %w", err)
		}
		baseText = []byte(researchVerdicts.RenderPrompt(string(baseText)))
	}
	base := renderSegments(string(baseText), baseSource, vars).trimTrailingNewlines()

	// ContractInline (butler) injects nothing: its OUTCOME section is
	// self-contained, and the comms/check blocks assume a landing branch the
	// butler never cuts (ADR 0056). For issue-prompt.md the landing injection
	// is a no-op, since those blocks are sliced from it and the marker guard
	// fires. The code-comments policy is inlined in the templates (#3505).
	switch d.Contract {
	case dispatchkind.ContractVerdict:
		base, err = injectSharedBlockSegments(base, e.ResearchOutcomeContractFile, vars)
		if err != nil {
			return promptBodies{}, err
		}
	case dispatchkind.ContractInline:
	case dispatchkind.ContractLanding:
		for _, contractFile := range []string{e.CommsContractFile, e.CheckContractFile, e.OutcomeContractFile} {
			base, err = injectSharedBlockSegments(base, contractFile, vars)
			if err != nil {
				return promptBodies{}, err
			}
		}
	default:
		return promptBodies{}, fmt.Errorf("kind %q: unknown prompt contract %d", d.Name, d.Contract)
	}

	// Issue-text section (issue #3445): appended after every other base-body
	// transformation, so the block layered on later lands on a stable prefix.
	// Skipped rather than emitted empty, to keep a stray separator or
	// zero-byte source out of Compose's report.
	if issueSection != "" {
		base = append(base, segment{src: Source{Kind: SourceVar, Name: "ISSUE_TEXT"}, text: "\n\n" + issueSection})
	}

	// The review prompt is populated only on the fresh-work path: an
	// advise-only dispatch never reviews (ADR 0022), and a warm FixPass box
	// has its own review-less flow.
	var review body
	var reviewName string
	if !d.AdviseOnly && e.FixPass == 0 {
		reviewName = "review-prompt.md"
		reviewPromptPath := filepath.Join(e.PromptsDir, reviewName)
		reviewSource := Source{Kind: SourceTemplate, Name: reviewName}
		reviewBody, err := renderFileSegments(reviewPromptPath, reviewSource, vars)
		if err != nil {
			return promptBodies{}, fmt.Errorf("read review-prompt.md: %w", err)
		}
		// Same rule as base's append above, attributed to the same ISSUE_TEXT
		// source so Compose's per-pass reconciliation sees it once per body
		// rather than drifting between the two.
		if issueSection != "" {
			reviewBody = append(reviewBody, segment{src: Source{Kind: SourceVar, Name: "ISSUE_TEXT"}, text: "\n\n" + issueSection})
		}
		review = reviewBody
	}

	return promptBodies{
		base:        base,
		baseName:    baseName,
		review:      review,
		reviewName:  reviewName,
		sessionMode: sessionMode,
		vars:        vars,
		gates:       gates,
		kind:        d,
	}, nil
}

// Assemble renders the covered Env cell's prompt, --agents JSON, and driver
// hand-off facts. An Env outside those cells is rejected before any file
// I/O, with an error wrapping ErrUnsupportedCell.
func Assemble(e Env, reg Registry) (Result, error) {
	if err := checkCoveredCell(e); err != nil {
		return Result{}, err
	}

	bodies, err := assemblePromptBodies(e, reg)
	if err != nil {
		return Result{}, err
	}

	frags := map[string][]string{}
	addFragmentRenders(frags, bodies.base)
	addFragmentRenders(frags, bodies.review)

	var removed removedVarScan
	removed.check("prompt", bodies.base)
	removed.check("review prompt", bodies.review)

	result := Result{
		Prompt: bodies.base.text(),
		Handoff: Handoff{
			SessionMode:      bodies.sessionMode,
			AdvisoryReviewer: bodies.kind.Prompts.Reviewer != "",
		},
	}

	result.ReviewPromptText = bodies.review.text()

	// An empty template means no --agents flag at all: AgentsJSON stays "".
	if e.AgentsJSONTemplate != "" {
		agentsTemplate := e.AgentsJSONTemplate

		// Issue #2277: the code-owned review pass replaces the inline
		// reviewer subagent, so its key is dropped after its model is read.
		var agentsKeys map[string]json.RawMessage
		if err := json.Unmarshal([]byte(agentsTemplate), &agentsKeys); err != nil {
			return Result{}, fmt.Errorf("parse agents json template: %w", err)
		}
		if reviewerRaw, ok := agentsKeys["reviewer"]; ok {
			var reviewer struct {
				Model  string `json:"model"`
				Effort string `json:"effort"`
			}
			// A malformed reviewer entry mirrors jq's `// empty`: an
			// Unmarshal error and a zero-value field both leave
			// ReviewModel/ReviewEffort empty rather than failing.
			_ = json.Unmarshal(reviewerRaw, &reviewer)
			result.Handoff.ReviewModel = reviewer.Model
			result.Handoff.ReviewEffort = reviewer.Effort
		}
		// A kind with its own reviewer prompt (butler, ADR 0056) is
		// AdviseOnly and never gets the code-owned review pass, so its
		// inline reviewer is its only review: keep the key.
		if bodies.kind.Prompts.Reviewer == "" {
			delete(agentsKeys, "reviewer")
		}
		strippedJSON, err := json.Marshal(agentsKeys)
		if err != nil {
			return Result{}, fmt.Errorf("marshal reviewer-stripped agents json: %w", err)
		}
		agentsTemplate = string(strippedJSON)

		agentsJSON, err := renderAgentsJSON(e, agentsTemplate, bodies.vars, frags, &removed, bodies.kind.Prompts.Reviewer)
		if err != nil {
			return Result{}, err
		}
		result.AgentsJSON = agentsJSON
	}

	// Must run after the --agents loop: an existing reviewer.md's frontmatter
	// model and effort overwrite what the JSON path set.
	if e.DriverAgentFilesDir != "" {
		if err := rewriteAgentFiles(e, bodies.vars, &removed, &result.Handoff.ReviewModel, &result.Handoff.ReviewEffort, bodies.kind.Prompts.Reviewer); err != nil {
			return Result{}, err
		}
	}

	result.Fragments = slices.Sorted(maps.Keys(frags))
	result.fragmentRenders = frags
	result.removedVarErr = removed.err
	result.deprecatedVarWarnings = removed.warnings

	// Dispatch-time overrides (issue #3171) bind last, over both extraction
	// paths above: dispatch env beats a baked roster entry beats the
	// coordinator-model fallback. They apply even when the roster opted the
	// reviewer out, because the review pass always runs. WriteAssembly then
	// lets a REVIEW_MODEL level suffix fill ReviewEffort (issue #5625).
	if e.ReviewModelOverride != "" {
		result.Handoff.ReviewModel = e.ReviewModelOverride
	}
	if e.ReviewEffortOverride != "" {
		result.Handoff.ReviewEffort = e.ReviewEffortOverride
	}

	return result, nil
}

// reviewFanoutAgent must name a lib/roster.nix defaultRoster entry.
// nix/checks/code-review-fragment-parity.nix extracts this literal and pins
// it against the roster, so the baked anchor cannot drift to an ungoverned
// agent type.
const reviewFanoutAgent = "review-axis"

// reviewFanoutFallbackAgent is the upstream /code-review skill's own fan-out
// default, and the only agent type that can work when this run provisions no
// axis agent at all.
const reviewFanoutFallbackAgent = "general-purpose"

// reviewFanoutAgentFor resolves code-review-baked.md's REVIEW_FANOUT_AGENT
// from what this run provisions: a template key for an --agents JSON Driver,
// a <name>.md for an on-disk agent-file Driver. A roster can omit the entry
// (issue #392's reviewModel="" opt-out), and naming an agent the session never
// defines errors after burning turns.
func reviewFanoutAgentFor(e Env) string {
	if e.AgentsJSONTemplate != "" {
		var keys map[string]json.RawMessage
		// A malformed template is not this resolver's error to raise:
		// Assemble's own agents-JSON path reports it with context.
		if err := json.Unmarshal([]byte(e.AgentsJSONTemplate), &keys); err == nil {
			if _, ok := keys[reviewFanoutAgent]; ok {
				return reviewFanoutAgent
			}
		}
	}
	if e.DriverAgentFilesDir != "" {
		if _, err := os.Stat(filepath.Join(e.DriverAgentFilesDir, reviewFanoutAgent+".md")); err == nil {
			return reviewFanoutAgent
		}
	}
	return reviewFanoutFallbackAgent
}

// reviewerPromptOverride swaps in the kind's own reviewer prompt (butler,
// ADR 0056) for the roster's review-prompt.md, since its reviewer judges a
// delegation-message finding rather than a branch diff. A roster with no
// "reviewer" entry stays without one: nothing reads promptFiles["reviewer"]
// when the agents template carries no "reviewer" key.
func reviewerPromptOverride(reviewerPrompt string, promptFiles map[string]string) map[string]string {
	if reviewerPrompt == "" {
		return promptFiles
	}
	if promptFiles == nil {
		promptFiles = map[string]string{}
	}
	promptFiles["reviewer"] = reviewerPrompt
	return promptFiles
}

// fragmentRenderGap marks, in a fragment's render, where something else
// interrupted it. It is NUL, which is not whitespace, so a clause normalised
// with strings.Fields cannot match across it. Each nested fragment in the
// interrupting run follows as name + gap, letting a contract pin where a child
// is spliced; template text alone leaves a bare gap.
const fragmentRenderGap = "\x00"

// addFragmentRenders records, for each lib/fragments.nix fragment b's
// segments carry, that fragment's own rendered prose: its literal segments and
// the plain vars substituted directly into it. Text of a nested fragment, or
// of anything between two renders of the fragment, is left out and replaced by
// a gap naming the fragments that interrupted it.
func addFragmentRenders(renders map[string][]string, b body) {
	builders := map[string]*strings.Builder{}
	var order []string
	// interrupted has a key for each started fragment whose last segment was
	// not its own; the value is the distinct other fragments seen since.
	interrupted := map[string][]string{}
	for _, seg := range b {
		if seg.text == "" {
			continue
		}
		owner := seg.src
		if owner.Kind == SourceVar {
			owner = seg.into
		}
		isFrag := owner.Kind == SourceFragment
		for name := range builders {
			if isFrag && name == owner.Name {
				continue
			}
			names := interrupted[name]
			if isFrag && !slices.Contains(names, owner.Name) {
				names = append(names, owner.Name)
			}
			interrupted[name] = names
		}
		if !isFrag {
			continue
		}
		sb, ok := builders[owner.Name]
		if !ok {
			sb = &strings.Builder{}
			builders[owner.Name] = sb
			order = append(order, owner.Name)
		} else if names, gap := interrupted[owner.Name]; gap {
			sb.WriteString(fragmentRenderGap)
			for _, n := range names {
				sb.WriteString(n + fragmentRenderGap)
			}
		}
		delete(interrupted, owner.Name)
		sb.WriteString(seg.text)
	}
	for _, name := range order {
		renders[name] = append(renders[name], builders[name].String())
	}
}

// renderAgentsJSON sets .{name}.prompt for every key in agentsTemplate whose
// AgentsPromptFiles entry names a file that exists under PromptsDir.
// agentsTemplate is a parameter so the caller can pass the reviewer-stripped
// template (issue #2353). Every fragment reaching an agent prompt is added to
// frags.
func renderAgentsJSON(e Env, agentsTemplate string, vars map[string]body, frags map[string][]string,
	removed *removedVarScan, reviewerPrompt string) (string, error) {
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
	promptFiles = reviewerPromptOverride(reviewerPrompt, promptFiles)

	// Sorted so the first removed-var hit is deterministic.
	for _, name := range slices.Sorted(maps.Keys(template)) {
		promptFile := promptFiles[name]
		if promptFile == "" {
			continue
		}
		path := filepath.Join(e.PromptsDir, promptFile)
		renderedBody, err := renderFileSegments(path, Source{Kind: SourceTemplate, Name: promptFile}, vars)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("read agent prompt file %s: %w", promptFile, err)
		}
		addFragmentRenders(frags, renderedBody)
		removed.check(fmt.Sprintf("agent %q prompt", name), renderedBody)
		rendered := renderedBody.text()

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
// "---" fence. A file missing a second fence, never true for a real baked
// agent file, falls through to the whole file with trailing newlines
// stripped, matching the bash original's command substitution.
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

// frontmatterScalar extracts the `key:` YAML scalar from a baked reviewer.md's
// frontmatter, or "" when absent. The baked shape is always double-quoted
// (`model: "opus"`), so a prefix cut plus a quote trim suffices.
func frontmatterScalar(frontmatter, key string) string {
	prefix := key + ": "
	for _, line := range strings.Split(frontmatter, "\n") {
		if v, ok := strings.CutPrefix(line, prefix); ok {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

// rewriteAgentFiles is renderAgentsJSON's twin for a Driver whose subagents
// use on-disk agent files; call it only when DriverAgentFilesDir is set.
// reviewer.md's `model:` and `reasoningEffort:` overwrite *reviewModel and
// *reviewEffort (each "" when absent) and the file is removed; a missing one
// leaves both untouched. A kind with its own reviewer prompt keeps
// reviewer.md, rewritten from that prompt like any other agent file. Names
// are rewritten in sorted order so Go map order cannot vary results.
func rewriteAgentFiles(e Env, vars map[string]body, removed *removedVarScan, reviewModel, reviewEffort *string, reviewerPrompt string) error {
	if reviewerPrompt == "" {
		reviewerPath := filepath.Join(e.DriverAgentFilesDir, "reviewer.md")
		if data, err := os.ReadFile(reviewerPath); err == nil {
			fm := frontmatterOf(data)
			*reviewModel = frontmatterScalar(fm, "model")
			*reviewEffort = frontmatterScalar(fm, "reasoningEffort")
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
	promptFiles = reviewerPromptOverride(reviewerPrompt, promptFiles)

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
		renderedBody, err := renderFileSegments(promptPath, Source{Kind: SourceTemplate, Name: promptFiles[name]}, vars)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("read agent prompt file %s: %w", promptFiles[name], err)
		}
		removed.check(fmt.Sprintf("agent %q prompt", name), renderedBody)
		// Unlike renderAgentsJSON, no addFragmentRenders: no golden pins agent
		// files, so recording these would claim coverage no golden backs.
		rendered := renderedBody.text()

		frontmatter := frontmatterOf(agentFileData)
		out := frontmatter + "\n" + rendered + "\n"
		if err := os.WriteFile(agentFilePath, []byte(out), 0o644); err != nil {
			return fmt.Errorf("write agent file %s: %w", agentFilePath, err)
		}
	}

	return nil
}

// initialDisplayName is FORGE_DISPLAY_NAME's value outside any row's render:
// the tracker read axis's, the same a plain row takes.
func initialDisplayName(e Env) string {
	return trackerReadDisplayName(e)
}
