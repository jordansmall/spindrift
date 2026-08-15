package promptassembly

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// The When values a ValidateMarkerRow's When field is compared against in
// Validate, named so a row-value typo is a compile-time/IDE-assisted
// mismatch rather than only a runtime fallback-arm error.
const (
	whenReadOnlyResearch    = "readOnlyResearch"
	whenOrchestratorEnabled = "orchestratorEnabled"
	whenBoxAccessReadOnly   = "boxAccessReadOnly"
	whenFilerFileRelay      = "filerFileRelay"
)

// The Severity values a ValidateMarkerRow's Severity field is compared
// against in Validate.
const (
	severityReject = "reject"
	severityWarn   = "warn"
)

// ValidateMarkerRow is the Go mirror of one row in
// lib/prompt-contract.nix's validateMarkers registry. JSON tags mirror the
// nix attrset's field names literally, the same convention FragmentRow
// (registry.go) follows, so a later slice's nix-rendered JSON (built from
// the same validateMarkers list via builtins.toJSON) decodes into this type
// without any renaming.
type ValidateMarkerRow struct {
	// ID names the row, e.g. "verdict-comment-relay".
	ID string `json:"id"`
	// Marker is the literal substring Validate scans a row's haystack for.
	Marker string `json:"marker"`
	// Carrier documents (informationally only -- Validate never branches on
	// it) which rendered text is supposed to carry Marker, e.g.
	// "fragment-body" or "subagent-first-line".
	Carrier string `json:"carrier"`
	// Severity is "reject" (fatal, stop processing further rows) or "warn"
	// (advisory, collected and processing continues) when the row's gate is
	// active and Marker is missing.
	Severity string `json:"severity"`
	// When names which gate condition activates this row -- one of
	// "readOnlyResearch", "orchestratorEnabled", "boxAccessReadOnly", or
	// "filerFileRelay". See Validate's switch for exactly what each resolves
	// to.
	When string `json:"when"`
	// Message is the row's fully pre-rendered diagnostic prose, marker
	// already interpolated by the nix registry.
	Message string `json:"message"`
}

// LoadValidateMarkers reads and parses a validateMarkers registry JSON
// document (a bare JSON array of ValidateMarkerRow objects, matching
// lib/prompt-contract.nix's builtins.toJSON shape) from r. Malformed JSON is
// reported as a wrapped error, never a panic.
func LoadValidateMarkers(r io.Reader) ([]ValidateMarkerRow, error) {
	var rows []ValidateMarkerRow
	if err := json.NewDecoder(r).Decode(&rows); err != nil {
		return nil, fmt.Errorf("decode validate markers registry: %w", err)
	}
	return rows, nil
}

// LoadValidateMarkersFile opens path and loads it via LoadValidateMarkers --
// a convenience wrapper for callers working from a filesystem path (e.g. the
// nix-baked registry file a later slice's CLI verb reads) rather than an
// already-open reader. A missing or unreadable file is reported as a wrapped
// error, never a panic.
func LoadValidateMarkersFile(path string) ([]ValidateMarkerRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open validate markers registry %s: %w", path, err)
	}
	defer f.Close()
	return LoadValidateMarkers(f)
}

// ForbiddenMarkerRow is the Go mirror of one row in
// lib/prompt-contract.nix's forbiddenMarkers registry (issue #2464), sharing
// ValidateMarkerRow's six fields/JSON tags -- id/marker/carrier/severity/
// when/message -- plus two more of its own, kind and enforce (issue #2499),
// decoded from nix-rendered JSON built via the same builtins.toJSON
// convention. Unlike validateMarkers, which asserts a
// marker is present under an active gate, forbiddenMarkers asserts a
// marker is absent -- specifically, never rendered as an imperative
// telling a read-only Box to perform the operation -- under an active
// gate.
type ForbiddenMarkerRow struct {
	// ID names the row, e.g. "forbidden-git-push".
	ID string `json:"id"`
	// Marker is the literal marker text a Box's rendered prompt must not
	// carry as an imperative.
	Marker string `json:"marker"`
	// Carrier documents (informationally only) where Marker would appear
	// if it were (wrongly) present, e.g. "fragment-body".
	Carrier string `json:"carrier"`
	// Severity is "reject" or "warn", same vocabulary as
	// ValidateMarkerRow.Severity.
	Severity string `json:"severity"`
	// When names which gate condition activates this row, same vocabulary
	// as ValidateMarkerRow.When.
	When string `json:"when"`
	// Kind is "substring" (the default meaning) for a row whose Marker is
	// checked as literal rendered text, or "gh-api-mutation" for the one
	// row backed by entrypoint.sh's `gh api` argument-scan shim, whose
	// Marker is display-only (not scanned as a prompt substring).
	Kind string `json:"kind"`
	// Enforce names which runtime mechanism actually stops this
	// operation: "command-shim" (a PATH-shadowing wrapper, e.g. the
	// read-only `gh` shim), "git-hook" (a pre-push/pre-receive hook), or
	// "prompt-only" (no runtime guard exists -- enforcement is solely
	// "the rendered prompt must not order this imperatively", checked by
	// promptassembly.Validate itself).
	Enforce string `json:"enforce"`
	// Message is the row's fully pre-rendered diagnostic prose, marker
	// already interpolated by the nix registry.
	Message string `json:"message"`
}

// LoadForbiddenMarkers reads and parses a forbiddenMarkers registry JSON
// document (a bare JSON array of ForbiddenMarkerRow objects, matching
// lib/prompt-contract.nix's builtins.toJSON shape) from r. Malformed JSON is
// reported as a wrapped error, never a panic.
func LoadForbiddenMarkers(r io.Reader) ([]ForbiddenMarkerRow, error) {
	var rows []ForbiddenMarkerRow
	if err := json.NewDecoder(r).Decode(&rows); err != nil {
		return nil, fmt.Errorf("decode forbidden markers registry: %w", err)
	}
	return rows, nil
}

// LoadForbiddenMarkersFile opens path and loads it via LoadForbiddenMarkers
// -- a convenience wrapper for callers working from a filesystem path (e.g.
// the nix-baked registry file a later slice's CLI verb reads) rather than an
// already-open reader. A missing or unreadable file is reported as a wrapped
// error, never a panic.
func LoadForbiddenMarkersFile(path string) ([]ForbiddenMarkerRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open forbidden markers registry %s: %w", path, err)
	}
	defer f.Close()
	return LoadForbiddenMarkers(f)
}

// Validate is the Go successor to agent/entrypoint.sh's
// _validate_prompt_contract (issue #2249), moved here by issue #2356: the
// in-box reject/warn matrix run once, at the tail of prompt assembly, after
// every fragment has been rendered/injected and Result's prompt/agents JSON
// are fully assembled, strictly before the Driver is ever invoked. It scans
// for the markers lib/prompt-contract.nix's validateMarkers registry
// (rows) names, each gated on the exact same condition that gated the
// fragment/step supposed to carry it.
//
// Validate reads exactly the gates Assemble itself computed for e (via a
// single Gates(e) call up front, reused for every row) rather than
// re-deriving any gate condition a second, possibly-diverging way -- the
// actual source of truth for what got rendered, so this can never silently
// drift from Assemble's own fragment-gating logic (see brief for #2249).
//
// A "reject" row's marker missing under its gate condition means the Box's
// own contract with the launcher/host is unmet in a way nothing downstream
// can recover from: Validate returns immediately with a non-nil error (its
// bash predecessor's exit 1), so no later row is even checked. A "warn" row
// already has a working non-fatal backstop, so its marker missing is only
// ever advisory: its message is appended to warnings and the loop continues
// to the next row.
//
// Dispatches on each row's When field to decide whether its gate is active
// and which rendered text to scan, then on its Severity field to decide
// whether a missing marker under an active gate is fatal or advisory --
// driven by rows' own data rather than a hardcoded per-id switch (issue
// #2318).
//
// Validate also checks forbiddenRows (issue #2464): for each row whose gate
// is active, it rejects/warns if ForbiddenMarkerIsImperative reports the
// row's Marker was rendered as an imperative instruction rather than an
// absent or merely-mentioned one.
//
// liveCodeForge == "git" is excluded from the whenBoxAccessReadOnly
// forbidden-row gate entirely (both loops below). templates/default/
// prompts/issue-prompt.md's `**`CODE_FORGE=git`**` branch (a push-only Code
// Forge with no PR mechanism) contains a genuine, ungated, un-negated "git
// push" numbered-list instruction -- correct, load-bearing content for that
// branch, since a CODE_FORGE=git Box must push directly to land its work;
// it is never a drifted-fragment bug this check needs to catch. Unlike
// cmd/launcher/main.go's checkReadOnlyCapabilityGate (which separately
// refuses at launcher startup to ever dispatch
// BOX_FORGE_AND_ISSUE_ACCESS=read-only with CODE_FORGE=git, since
// CODE_FORGE=git doesn't implement forge.BundleRelay), this
// promptassembly-package Validate call has no such protection of its own --
// entrypoint.sh's bats coverage exercises read-only + CODE_FORGE=git
// directly (e.g. tests/entrypoint-pr-intent-nudge.bats's "PR-intent gate:
// never fires under CODE_FORGE=git"), so this package must tolerate the
// combination rather than assume it away.
func Validate(e Env, result Result, rows []ValidateMarkerRow, forbiddenRows []ForbiddenMarkerRow) (warnings []string, err error) {
	gates := Gates(e)
	kind := e.DispatchKind
	if kind == "" {
		kind = defaultDispatchKind
	}

	liveCodeForge := e.CodeForge
	if liveCodeForge == "" {
		liveCodeForge = defaultCodeForge
	}

	for _, row := range rows {
		var gateActive bool
		var haystack string

		switch row.When {
		case whenReadOnlyResearch:
			gateActive = kind == "research" && gates["BOX_ACCESS_READ_ONLY"]
			haystack = result.Prompt
		case whenOrchestratorEnabled:
			gateActive = gates["ORCHESTRATOR"] && result.Handoff.ReviewPromptFile != ""
			haystack = result.Handoff.ReviewPromptFile
		case whenBoxAccessReadOnly:
			gateActive = gates["BOX_ACCESS_READ_ONLY"] && kind != "research"
			haystack = result.Prompt
		case whenFilerFileRelay:
			gateActive = gates["FILER_FILE_RELAY"]
			haystack = filerPromptFrom(result.AgentsJSON)
		default:
			return warnings, fmt.Errorf("promptassembly: validate: no known gate for when %q (row %q)", row.When, row.ID)
		}

		if !gateActive || strings.Contains(haystack, row.Marker) {
			continue
		}

		message := row.Message

		switch row.Severity {
		case severityReject:
			return warnings, fmt.Errorf("%s", message)
		case severityWarn:
			warnings = append(warnings, message)
		default:
			return warnings, fmt.Errorf("promptassembly: validate: unknown severity %q for row %q", row.Severity, row.ID)
		}
	}

	for _, row := range forbiddenRows {
		var gateActive bool
		var haystacks []string

		switch row.When {
		case whenBoxAccessReadOnly:
			gateActive = gates["BOX_ACCESS_READ_ONLY"] && kind != "research" && liveCodeForge != "git"
			// Three possible rendered texts a read-only Box's contract
			// spans -- the main prompt, the filer sub-agent's own prompt,
			// and the orchestrator's review prompt file -- mirroring the
			// three haystacks the validateRows loop above already
			// dispatches across per-When (issue #2464 follow-up: "gh issue
			// create"/"gh issue comment" only ever render inside the filer
			// prompt, never result.Prompt). The filer prompt haystack is
			// dropped when the filer is legitimately using its own direct
			// gh/fj write path (FILER_FILE_DIRECT_GH/FORGEJO) rather than
			// the host-mediated relay -- that path's own token is
			// independent of the main Box's BOX_ACCESS_READ_ONLY status
			// (issue #2019), so "gh issue create" there is expected
			// content, not a violation (issue #2464 follow-up: today's
			// degraded direct-file path, tests/entrypoint-prompt-
			// fragments.bats's "filer write step: read-only with
			// orchestrator off keeps today's degraded direct-file path
			// unchanged").
			haystacks = []string{result.Prompt, result.Handoff.ReviewPromptFile}
			if !gates["FILER_FILE_DIRECT_ANY"] {
				haystacks = append(haystacks, filerPromptFrom(result.AgentsJSON))
			}
		default:
			return warnings, fmt.Errorf("promptassembly: validate: no known gate for when %q (row %q)", row.When, row.ID)
		}

		violated := false
		if gateActive {
			for _, h := range haystacks {
				if h == "" {
					continue
				}
				if ForbiddenMarkerIsImperative(row.Marker, h, liveCodeForge) {
					violated = true
					break
				}
			}
		}

		if !violated {
			continue
		}

		message := row.Message

		switch row.Severity {
		case severityReject:
			return warnings, fmt.Errorf("%s", message)
		case severityWarn:
			warnings = append(warnings, message)
		default:
			return warnings, fmt.Errorf("promptassembly: validate: unknown severity %q for row %q", row.Severity, row.ID)
		}
	}

	return warnings, nil
}

// filerPromptFrom extracts the filer's own rendered prompt text from
// agentsJSON, mirroring _validate_prompt_contract's `jq -r '.filer.prompt //
// empty'` (entrypoint.sh: 583) on agents_json. An empty or malformed
// agentsJSON never panics, just yields "".
func filerPromptFrom(agentsJSON string) string {
	var parsed struct {
		Filer struct {
			Prompt string `json:"prompt"`
		} `json:"filer"`
	}
	_ = json.Unmarshal([]byte(agentsJSON), &parsed)
	return parsed.Filer.Prompt
}
