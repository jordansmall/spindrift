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
	// (orchestrator on, default fresh-work dispatch, FixPass == 0). It lives
	// on Result, not Handoff, because it is rendered text rather than the
	// path Handoff serializes to disk (issue #2975).
	ReviewPromptText string
	Handoff          Handoff
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
// SessionMode, Invoker, ReviewModel, and ReviewEffort; the CLI wrapper
// populates every other field from flags and static config (issue #2975).
type Handoff struct {
	// SessionMode is "resume" or "initial" (entrypoint.sh: 1037-1052).
	SessionMode string
	// Invoker is "orchestrator" or "driver-exec" (entrypoint.sh: 1282-1286).
	Invoker string
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
	// whenever Invoker is "orchestrator", regardless of dispatch kind or
	// FixPass. It stays empty under "driver-exec" or when the template has no
	// reviewer model, mirroring jq's `.reviewer.model // empty`. An explicit
	// dispatch-time REVIEW_MODEL (issue #3171) binds over it last.
	ReviewModel string
	// ReviewEffort mirrors ReviewModel, from the same reviewer key's "effort"
	// field under the same condition, and is likewise overridden last by an
	// explicit dispatch-time REVIEW_EFFORT (issue #3171).
	ReviewEffort string
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

// checkCoveredCell validates that e sits in a covered Env cell. Only
// DispatchKind is checked: it is set programmatically at runtime with no
// schema entry to guard it, while IssueTracker and CodeForge are validated by
// lib/mkHarness.nix's choicesCheckOk assert and main.go's validate() (issue
// #2540). No combination of the other axes is rejected (issue #2354).
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

// substTokenRe matches the braced ${NAME} form envsubst recognizes. Every
// template and fragment under templates/default/prompts references its
// variables this way, never bare $NAME (verified against the tree, #2349).
var substTokenRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// substitute replaces every ${NAME} that is a key of allowlist; anything else
// passes through untouched. The single ReplaceAllStringFunc pass, rather than
// sequential per-name replacement, keeps a substituted value that itself
// contains ${NAME}-shaped text from being re-expanded.
func substitute(text string, allowlist map[string]string) string {
	return substTokenRe.ReplaceAllStringFunc(text, func(tok string) string {
		name := tok[2 : len(tok)-1]
		if v, ok := allowlist[name]; ok {
			return v
		}
		return tok
	})
}

// RenderText substitutes every ${NAME} token in text through vars and trims
// trailing newlines, the same treatment renderFile gives an on-disk file.
// Exported so a caller that needs only this substitution, not the rest of
// Assemble's pipeline, does not hand-roll its own strings.ReplaceAll pass.
func RenderText(text string, vars map[string]string) string {
	return strings.TrimRight(substitute(text, vars), "\n")
}

// renderFile reads path, substitutes it through allowlist, and trims the
// trailing newlines a $(...) command substitution would strip. That invariant
// lives here alone, not at each of the fragment, base-template, and
// per-agent call sites.
func renderFile(path string, allowlist map[string]string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(substitute(string(data), allowlist), "\n"), nil
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

// renderFileSegments renders path like renderFile, but the trim must run on
// segments rather than the joined string to keep per-segment attribution
// intact, so it can't simply wrap renderFile.
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
	allowlist   map[string]string
	gates       map[string]bool
	kind        string
}

// assemblePromptBodies performs Assemble's prompt path in attributed segment
// form: gates, the allowlist, the fragment loop, base-template selection,
// shared-block injection, and the review-prompt render. It also returns
// allowlist, gates, and kind in plain form, since Assemble's post-prompt
// steps need no attribution and stay on the string-keyed allowlist.
func assemblePromptBodies(e Env, reg Registry) (promptBodies, error) {
	gates := Gates(e)
	// SKILLS_FOUND is a filesystem-derived presence gate Gates never computes,
	// because I/O is out of its scope.
	gates["SKILLS_FOUND"] = e.SkillsFound != ""

	// The substitution allowlist: the fixed scalars below (RESEARCH_STATUS_ENUM
	// came from issue #2504) plus every registry row's var and extraSubstVars,
	// one flat set shared by every render in this function rather than scoped
	// per-fragment.
	scalars := map[string]string{
		"ISSUE_NUMBER":         e.IssueNumber,
		"ISSUE_TITLE":          e.IssueTitle,
		"BRANCH":               e.Branch,
		"BASE_BRANCH":          e.BaseBranch,
		"IN_PROGRESS_LABEL":    e.InProgressLabel,
		"COMPLETE_LABEL":       e.CompleteLabel,
		"RUN_NONCE":            e.RunNonce,
		"RESEARCH_STATUS_ENUM": e.ResearchStatusEnum,
	}

	// vars is the segment-attributed twin of allowlist: same keys, bodies
	// instead of strings, so a fragment referencing an earlier var carries
	// that var's attribution through rather than absorbing its bytes.
	vars := make(map[string]body, len(scalars))
	allowlist := make(map[string]string, len(scalars))
	for k, v := range scalars {
		allowlist[k] = v
		vars[k] = varBody(k, v)
	}

	// ISSUE_TEXT (issue #3445) stays out of scalars above: its value is
	// Go-derived (issueTextSection's fenced text, never e.IssueText raw),
	// while scalars mirrors the raw-env substitution list byte for byte.
	issueSection := issueTextSection(e)
	allowlist["ISSUE_TEXT"] = issueSection
	vars["ISSUE_TEXT"] = varBody("ISSUE_TEXT", issueSection)

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
			allowlist[extra] = v
			vars[extra] = varBody(extra, v)
		}
	}

	// For each row in registry order, render its fragment when its gate is on
	// and assign renderedText + "\n\n" to its var. The "\n\n" is appended
	// here, not baked into the fragment file or the substitution result,
	// because command substitution strips trailing newlines.
	for _, row := range reg.Rows {
		fragSource := Source{Kind: SourceFragment, Name: row.Fragment}
		if gates[row.Gate] {
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
				allowlist[row.Var] = ""
				continue
			}
			fragBody := append(append(body{}, rendered...), segment{src: fragSource, text: "\n\n"})
			vars[row.Var] = fragBody
			allowlist[row.Var] = fragBody.text()
		} else {
			vars[row.Var] = body{}
			allowlist[row.Var] = ""
		}
	}

	// Base template selection and session mode, in precedence order: research
	// first regardless of FixPass, then a warm fix pass, then the default
	// work cell. Result.Prompt must carry no trailing newline, because the
	// writer prints it raw and nothing re-adds one downstream.
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
	baseSource := Source{Kind: SourceTemplate, Name: baseName}
	base, err := renderFileSegments(basePath, baseSource, vars)
	if err != nil {
		return promptBodies{}, fmt.Errorf("read %s: %w", baseName, err)
	}

	// Research injects only research-verdict; every other cell injects comms,
	// then check, then outcome, in that order. For issue-prompt.md this is a
	// no-op: those markers are sliced from issue-prompt.md itself, so the
	// already-contains-marker guard always fires. The code-comments policy is
	// inlined verbatim in the templates themselves (issue #3505), so it is
	// not part of this injection list.
	if kind == "research" {
		base, err = injectSharedBlockSegments(base, e.ResearchOutcomeContractFile, vars)
		if err != nil {
			return promptBodies{}, err
		}
	} else {
		for _, contractFile := range []string{e.CommsContractFile, e.CheckContractFile, e.OutcomeContractFile} {
			base, err = injectSharedBlockSegments(base, contractFile, vars)
			if err != nil {
				return promptBodies{}, err
			}
		}
	}

	// Issue-text section (issue #3445): appended after every other base-body
	// transformation, so the block layered on later lands on a stable prefix.
	// Skipped rather than emitted empty, to keep a stray separator or
	// zero-byte source out of Compose's report.
	if issueSection != "" {
		base = append(base, segment{src: Source{Kind: SourceVar, Name: "ISSUE_TEXT"}, text: "\n\n" + issueSection})
	}

	// The review prompt is populated only on the fresh-work path with the
	// orchestrator on: a research dispatch never reviews (ADR 0022), and a
	// warm FixPass box has its own review-less flow.
	var review body
	var reviewName string
	if gates["ORCHESTRATOR"] && kind == defaultDispatchKind && e.FixPass == 0 {
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
		allowlist:   allowlist,
		gates:       gates,
		kind:        kind,
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

	allowlist := bodies.allowlist
	gates := bodies.gates

	invoker := "driver-exec"
	if gates["ORCHESTRATOR"] {
		invoker = "orchestrator"
	}

	result := Result{
		Prompt: bodies.base.text(),
		Handoff: Handoff{
			SessionMode: bodies.sessionMode,
			Invoker:     invoker,
		},
	}

	// A cell that renders no review prompt leaves bodies.review nil, whose
	// text() is "".
	result.ReviewPromptText = bodies.review.text()

	// An empty template means no --agents flag at all: AgentsJSON stays "".
	if e.AgentsJSONTemplate != "" {
		agentsTemplate := e.AgentsJSONTemplate

		if gates["ORCHESTRATOR"] {
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

	// The on-disk agent-file rewrite is the twin of the --agents JSON loop
	// above, for a Driver (opencode) whose subagents use baked agent files.
	// It must run after that loop: an existing reviewer.md's frontmatter
	// model overwrites whatever the JSON path set in ReviewModel, and a
	// missing reviewer.md leaves the JSON-path value unchanged.
	if e.DriverAgentFilesDir != "" {
		if err := rewriteAgentFiles(e, allowlist, gates["ORCHESTRATOR"], &result.Handoff.ReviewModel); err != nil {
			return Result{}, err
		}
	}

	// Dispatch-time overrides (issue #3171) bind last, over both extraction
	// paths above: dispatch env beats a baked roster entry beats the
	// coordinator-model fallback. They apply even when the roster opted the
	// reviewer out, because the review pass always runs under ORCHESTRATOR.
	if gates["ORCHESTRATOR"] {
		if e.ReviewModelOverride != "" {
			result.Handoff.ReviewModel = e.ReviewModelOverride
		}
		if e.ReviewEffortOverride != "" {
			result.Handoff.ReviewEffort = e.ReviewEffortOverride
		}
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

// renderAgentsJSON sets .{name}.prompt for every key in agentsTemplate whose
// AgentsPromptFiles entry names a file that exists under PromptsDir.
// agentsTemplate is a parameter rather than read from e.AgentsJSONTemplate so
// the caller can pass the reviewer-stripped template the orchestrator-on
// branch produces (issue #2353).
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
// set. Under orchestratorOn, reviewer.md's `model:` scalar overwrites
// *reviewModel and the file is removed; a missing one leaves it untouched.
// Names are rewritten in sorted order, so Go map order cannot vary results.
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
