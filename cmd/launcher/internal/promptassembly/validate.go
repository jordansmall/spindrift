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
	whenResearchFileRelay   = "researchFileRelay"
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
	// "readOnlyResearch", "orchestratorEnabled", "boxAccessReadOnly",
	// "filerFileRelay", or "researchFileRelay". See Validate's switch for
	// exactly what each resolves to.
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
func Validate(e Env, result Result, rows []ValidateMarkerRow) (warnings []string, err error) {
	gates := Gates(e)
	kind := e.DispatchKind
	if kind == "" {
		kind = defaultDispatchKind
	}

	for _, row := range rows {
		var gateActive bool
		var haystack string

		switch row.When {
		case whenReadOnlyResearch:
			gateActive = kind == "research" && (gates["BOX_ACCESS_READ_ONLY"] || gates["FILER_FILE_RELAY"])
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
		case whenResearchFileRelay:
			gateActive = kind == "research" && gates["FILER_FILE_RELAY"]
			haystack = result.Prompt
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
