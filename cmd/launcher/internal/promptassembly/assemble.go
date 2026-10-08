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
	// ReviewPromptText is the rendered review-prompt.md body, populated
	// under the same condition Handoff.ReviewPromptFile describes
	// (default fresh-work dispatch, FixPass == 0). It lives
	// on Result, not Handoff, because it is rendered text rather than the
	// path Handoff serializes to disk (issue #2975).
	ReviewPromptText string
	Handoff          Handoff
	// Fragments is the sorted, de-duplicated set of lib/fragments.nix
	// fragment names whose bytes reach Prompt, ReviewPromptText, or an agent
	// prompt in AgentsJSON. It is the per-cell coverage record the
	// prompt-assembly golden suite pins (issue #3838).
	Fragments []string

	// removedVarErr is the first removed fragment variable Assemble found in
	// an operator-authored segment of a rendered prompt; Validate reports it.
	// Deferred rather than returned so it surfaces as a *ValidateError via
	// WriteAssembly (printed bare as operator prose); skipping Validate drops it.
	removedVarErr error
}

// ArgvShape describes how the CLI wrapper assembles the Driver's argv: which
// flag spells each input, whether the model flag is omitted when Model is
// empty (some Drivers reject an empty --model rather than defaulting), and
// the flag order the Driver's parser requires. Assemble never populates this;
// the wrapper fills it from per-Driver static configuration (issue #2975).
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
	// SessionMode is "resume" or "initial" (entrypoint.sh: 1037-1052).
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
	// has no reviewer model, mirroring jq's `.reviewer.model // empty`. An explicit
	// dispatch-time REVIEW_MODEL (issue #3171) binds over it last.
	ReviewModel string
	// ReviewEffort mirrors ReviewModel, from the same reviewer key's "effort"
	// field under the same condition, and is likewise overridden last by an
	// explicit dispatch-time REVIEW_EFFORT (issue #3171).
	ReviewEffort string
	// AdvisoryReviewer is true when the kind supplies its own reviewer prompt
	// (butler, ADR 0056): its inline reviewer only
	// advises, so its verdict must not steer the pass loop (issue #3925).
	AdvisoryReviewer bool
	// Model, Effort, Driver, DriverBin, and DriverFlags are the Driver
	// invocation's static configuration, never derived from Env/gate logic.
	Model       string
	Effort      string
	Driver      string
	DriverBin   string
	DriverFlags string
	// Devshell and DevshellName gate whether the Driver runs inside a Nix
	// devShell wrapper, and which one.
	Devshell     bool
	DevshellName string
	Issue        string
	HeartbeatLog string
	ArgvShape    ArgvShape
	Caps         Caps
}

// checkCoveredCell validates that e sits in a covered Env cell: DispatchKind,
// plus SelfContained against the resolved kind's Prompts. DispatchKind is set
// programmatically at runtime with no schema entry to guard it, while
// IssueTracker and CodeForge are validated by lib/mkHarness.nix's
// choicesCheckOk assert and main.go's validate() (issue #2540). No
// combination of the other axes is rejected (issue #2354).
func checkCoveredCell(e Env) error {
	d, ok := dispatchkind.ByName(e.kind())
	if !ok {
		return fmt.Errorf("dispatch kind %q: %w", e.DispatchKind, ErrUnsupportedCell)
	}

	// A kind with its own base prompt but no SelfContainedBase has nothing to
	// render under SelfContained=true; main.go's validate() rejects this
	// combination on the CLI path, but the entrypoint env path calls Assemble
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
// trailing newlines, the same treatment renderFileSegments gives an on-disk
// file. With bare, a bare $NAME is substituted too (GNU envsubst parity). Bare
// mode exists only for conflict-resolve-prompt.md Consumer PROMPTS_DIR
// overrides, which use a bare $BASE_BRANCH; the main assembly path stays
// braces-only because the default prompts carry literal shell text such as
// `echo $CODE_FORGE`, which bare mode would rewrite once its name became a
// substitution var.
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
// trailing newlines a $(...) command substitution would strip. That invariant
// lives here alone, not at each of the fragment, base-template, and
// per-agent call sites. The trim runs on segments rather than the joined
// string so per-segment attribution stays intact.
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

// assemblePromptBodies performs Assemble's prompt path in attributed segment
// form: gates, the substitution vars, the fragment loop, base-template
// selection, shared-block injection, and the review-prompt render. It also
// returns vars, gates, and kind, so Assemble's agent-prompt renders
// substitute through the same attributed vars and their fragment sources
// reach Result.Fragments.
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

	// The substitution vars: the fixed scalars below plus every registry
	// row's var and extraSubstVars, one flat set shared by every render in this
	// function rather than scoped per-fragment.
	scalars := map[string]string{
		"ISSUE_NUMBER":      e.IssueNumber,
		"ISSUE_TITLE":       e.IssueTitle,
		"BRANCH":            e.Branch,
		"BASE_BRANCH":       e.BaseBranch,
		"IN_PROGRESS_LABEL": e.InProgressLabel,
		"COMPLETE_LABEL":    e.CompleteLabel,
		"RUN_NONCE":         e.RunNonce,
		"DISPATCH_KEY":      e.DispatchKey,
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
	if mask && e.descriptor().Keying != dispatchkind.ByChore {
		// Unconditional, so the hash does not depend on whether this
		// Dispatch happened to carry issue text. The fixed section prose
		// stays; only the interpolated facts are masked.
		me := e
		me.IssueNumber = maskPlaceholder("ISSUE_NUMBER")
		me.IssueText = maskPlaceholder("ISSUE_TEXT")
		issueSection = issueTextSection(me)
	}
	vars["ISSUE_TEXT"] = varBody("ISSUE_TEXT", issueSection)

	// CHORE_PROMPT (ADR 0056, issue #3875) is the butler's ${CHORE_PROMPT}
	// substitution: the named Chore's own prompt file, embedded into
	// butler-prompt.md's body rather than appended like ISSUE_TEXT, since the
	// Chore prompt is host-authored template text, not untrusted issue prose.
	// A ByChore kind with no matching chores/<name>.md fails assembly outright
	// (choreSection), unlike ISSUE_TEXT's silent-empty default.
	chorePrompt, err := choreSection(e)
	if err != nil {
		return promptBodies{}, err
	}
	vars[chorePromptVar] = varBody(chorePromptVar, chorePrompt)

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

	// extraSubstVars raw sources. CI_FAILURE_SUMMARY's field also drives its
	// own gate, since its presence is the gate (issue #2354).
	// REVIEW_FANOUT_AGENT is resolved from the run's provisioned agents
	// (issue #3447).
	extraRaw := map[string]string{
		"SKILLS_FOUND":        e.SkillsFound,
		"CI_FAILURE_SUMMARY":  e.CIFailureSummary,
		"REVIEW_FANOUT_AGENT": reviewFanoutAgentFor(e),
	}
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
			// ISSUE_TEXT already holds the masked section from above.
			if _, ok := vars[k]; !ok || k == "ISSUE_TEXT" {
				continue
			}
			vars[k] = varBody(k, maskPlaceholder(k))
		}
	}

	// For each row in registry order, render its fragment when its gate is on
	// and assign renderedText + "\n\n" to its var. The "\n\n" is appended
	// here, not baked into the fragment file or the substitution result,
	// because command substitution strips trailing newlines.
	for _, row := range reg.Rows {
		fragSource := Source{Kind: SourceFragment, Name: row.Fragment}
		// An ungated row (empty Gate) renders unconditionally, so the
		// orchestrator fragments need no switch gate.
		if row.Gate == "" || gates[row.Gate] {
			path := filepath.Join(e.PromptsDir, "fragments", row.Fragment)
			rendered, err := renderFileSegments(path, fragSource, vars)
			if err != nil {
				// A missing fragment file resolves to an empty string rather
				// than aborting, reproducing the bash original: its command
				// substitution sat as a printf argument, so a failed read
				// never tripped `set -e`. Any other read error still fails
				// hard, since bash would not have swallowed that either.
				if !errors.Is(err, os.ErrNotExist) {
					return promptBodies{}, fmt.Errorf("read fragment %s: %w", row.Fragment, err)
				}
				vars[row.Var] = body{}
				continue
			}
			fragBody := append(append(body{}, rendered...), segment{src: fragSource, text: "\n\n"})
			vars[row.Var] = fragBody
		} else {
			vars[row.Var] = body{}
		}
	}

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

	// ContractVerdict (research) injects only research-verdict;
	// ContractInline (butler) injects nothing at all (its OUTCOME section is
	// self-contained in butler-prompt.md, and the work-shaped comms/check
	// blocks below assume a landing branch/PR the butler never cuts, ADR
	// 0056); ContractLanding (work) injects comms, then check, then outcome,
	// in that order. For issue-prompt.md this is a no-op: those markers are
	// sliced from issue-prompt.md itself, so the already-contains-marker
	// guard always fires. The code-comments policy is inlined verbatim in
	// the templates themselves (issue #3505), so it is not part of this
	// injection list.
	switch d.Contract {
	case dispatchkind.ContractVerdict:
		base, err = injectSharedBlockSegments(base, e.ResearchOutcomeContractFile, vars)
		if err != nil {
			return promptBodies{}, err
		}
	case dispatchkind.ContractInline:
		// No shared-block injection for the butler (ADR 0056): nothing to do.
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

	frags := map[string]struct{}{}
	addFragmentNames(frags, bodies.base)
	addFragmentNames(frags, bodies.review)

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

	// A cell that renders no review prompt leaves bodies.review nil, whose
	// text() is "".
	result.ReviewPromptText = bodies.review.text()

	// An empty template means no --agents flag at all: AgentsJSON stays "".
	if e.AgentsJSONTemplate != "" {
		agentsTemplate := e.AgentsJSONTemplate

		// Issue #2277: extract the reviewer's configured model before
		// dropping the reviewer key entirely. The code-owned review pass
		// replaces the inline reviewer subagent, so that subagent is
		// never provisioned into --agents at all, not merely muted.
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
		// A kind with its own reviewer prompt (butler's
		// butler-review-prompt.md, ADR 0056) never gets the code-owned
		// review pass above (it's AdviseOnly), so its inline reviewer
		// subagent is the only review it gets — keep the key instead of
		// dropping it.
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

	// The on-disk agent-file rewrite is the twin of the --agents JSON loop
	// above, for a Driver (opencode) whose subagents use baked agent files.
	// It must run after that loop: an existing reviewer.md's frontmatter
	// model overwrites whatever the JSON path set in ReviewModel, and a
	// missing reviewer.md leaves the JSON-path value unchanged.
	if e.DriverAgentFilesDir != "" {
		if err := rewriteAgentFiles(e, bodies.vars, &removed, &result.Handoff.ReviewModel, bodies.kind.Prompts.Reviewer); err != nil {
			return Result{}, err
		}
	}

	result.Fragments = slices.Sorted(maps.Keys(frags))
	result.removedVarErr = removed.err

	// Dispatch-time overrides (issue #3171) bind last, over both extraction
	// paths above: dispatch env beats a baked roster entry beats the
	// coordinator-model fallback. They apply even when the roster opted the
	// reviewer out, because the review pass always runs.
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
// from what this run provisions: the --agents JSON driver (claude) by a
// template key, the agent-files driver (opencode) by an on-disk <name>.md.
// A roster can omit the entry (issue #392's reviewModel="" opt-out), and
// naming an agent the driver session never defines errors after burning turns.
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

// reviewerPromptOverride swaps in the kind's own reviewer prompt (butler's
// butler-review-prompt.md, ADR 0056) for the roster's review-prompt.md, so a
// kind whose reviewer subagent judges a delegation-message finding rather
// than a branch diff gets a rubric that matches. reviewerPrompt == "" (a kind
// with no reviewer prompt of its own) leaves promptFiles untouched; a roster
// with no "reviewer" entry at all stays without one either way, since
// nothing reads promptFiles["reviewer"] when the agents template carries no
// "reviewer" key.
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

// addFragmentNames adds the lib/fragments.nix fragment names that b's
// segments carry to set.
func addFragmentNames(set map[string]struct{}, b body) {
	for _, seg := range b {
		if seg.src.Kind == SourceFragment {
			set[seg.src.Name] = struct{}{}
		}
	}
}

// renderAgentsJSON sets .{name}.prompt for every key in agentsTemplate whose
// AgentsPromptFiles entry names a file that exists under PromptsDir.
// agentsTemplate is a parameter rather than read from e.AgentsJSONTemplate so
// the caller can pass the reviewer-stripped template (issue #2353).
// reviewerPrompt is the dispatch kind's own
// reviewer prompt filename, or "" when it has none. Every fragment that
// reaches a rendered agent prompt is added to frags.
func renderAgentsJSON(e Env, agentsTemplate string, vars map[string]body, frags map[string]struct{},
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
		addFragmentNames(frags, renderedBody)
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

// reviewerModelFrontmatter extracts the `model:` YAML scalar from a baked
// opencode reviewer.md's frontmatter. The baked shape is always a
// double-quoted scalar, e.g. `model: "opus"`, so a prefix cut plus a quote
// trim replaces the original's jq unwrap without a JSON parse. Returns ""
// when the frontmatter has no `model:` line.
func reviewerModelFrontmatter(frontmatter string) string {
	for _, line := range strings.Split(frontmatter, "\n") {
		if v, ok := strings.CutPrefix(line, "model: "); ok {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

// rewriteAgentFiles is renderAgentsJSON's twin for a Driver (opencode) whose
// subagents use on-disk agent files. Call it only when DriverAgentFilesDir is
// set. reviewer.md's `model:` scalar overwrites
// *reviewModel and the file is removed; a missing one leaves it untouched. A
// kind with its own reviewer prompt (reviewerPrompt != "") keeps reviewer.md
// instead, since it never gets the code-owned review pass this removal makes
// room for — the rewrite loop below then rewrites it from that kind's prompt
// like any other agent file. Names are rewritten in sorted order, so Go map
// order cannot vary results.
func rewriteAgentFiles(e Env, vars map[string]body, removed *removedVarScan, reviewModel *string, reviewerPrompt string) error {
	if reviewerPrompt == "" {
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
		// Unlike renderAgentsJSON, no addFragmentNames: no golden pins opencode
		// agent files, so recording these would claim coverage no golden backs.
		rendered := renderedBody.text()

		frontmatter := frontmatterOf(agentFileData)
		out := frontmatter + "\n" + rendered + "\n"
		if err := os.WriteFile(agentFilePath, []byte(out), 0o644); err != nil {
			return fmt.Errorf("write agent file %s: %w", agentFilePath, err)
		}
	}

	return nil
}
