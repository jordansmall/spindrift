package promptassembly

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// The When values Validate dispatches on, named so a row-value typo is a
// compile-time mismatch rather than only a runtime fallback-arm error.
const (
	whenReadOnlyResearch    = "readOnlyResearch"
	whenOrchestratorEnabled = "orchestratorEnabled"
	whenBoxAccessReadOnly   = "boxAccessReadOnly"
	whenFilerFileRelay      = "filerFileRelay"
	whenResearchFileRelay   = "researchFileRelay"
)

const (
	severityReject = "reject"
	severityWarn   = "warn"
)

// ValidateMarkerRow is the Go mirror of one row in lib/prompt-contract.nix's
// validateMarkers registry. JSON tags mirror the nix attrset's field names
// literally so its builtins.toJSON output decodes without renaming.
type ValidateMarkerRow struct {
	ID string `json:"id"`
	// Marker is the literal substring Validate scans a row's haystack for.
	Marker string `json:"marker"`
	// Carrier is informational only — Validate never branches on it.
	Carrier string `json:"carrier"`
	// Severity is "reject" (fatal, stop processing further rows) or "warn"
	// (advisory, collected and processing continues) when the row's gate is
	// active and Marker is missing.
	Severity string `json:"severity"`
	// When names which gate condition activates this row. See Validate's
	// switch for what each resolves to.
	When string `json:"when"`
	// Message is fully pre-rendered by the nix registry, marker interpolated.
	Message string `json:"message"`
}

// LoadValidateMarkers parses a bare JSON array of ValidateMarkerRow objects
// from r. Malformed JSON is reported as a wrapped error, never a panic.
func LoadValidateMarkers(r io.Reader) ([]ValidateMarkerRow, error) {
	var rows []ValidateMarkerRow
	if err := json.NewDecoder(r).Decode(&rows); err != nil {
		return nil, fmt.Errorf("decode validate markers registry: %w", err)
	}
	return rows, nil
}

// LoadValidateMarkersFile opens path and loads it via LoadValidateMarkers.
// A missing or unreadable file is reported as a wrapped error.
func LoadValidateMarkersFile(path string) ([]ValidateMarkerRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open validate markers registry %s: %w", path, err)
	}
	defer f.Close()
	return LoadValidateMarkers(f)
}

// Validate runs the reject/warn matrix once at the tail of prompt assembly,
// after every fragment is rendered and Result is fully assembled, strictly
// before the Driver is invoked. Each row's marker is gated on the same
// condition that gated the fragment supposed to carry it.
//
// It reuses one Gates(e) call rather than re-deriving any gate condition a
// second, possibly-diverging way, so it can never drift from Assemble's own
// fragment-gating logic.
//
// A missing "reject" marker means the Box's contract with the launcher is
// unmet in a way nothing downstream can recover from: Validate returns
// immediately and no later row is checked. A "warn" row already has a working
// non-fatal backstop, so its message is appended to warnings and the loop
// continues.
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
			// ReviewPromptText, not Handoff.ReviewPromptFile: Assemble
			// stopped populating the latter with rendered text once it
			// became a genuine on-disk path (issue #2975).
			gateActive = gates["ORCHESTRATOR"] && result.ReviewPromptText != ""
			haystack = result.ReviewPromptText
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

// filerPromptFrom extracts the filer's rendered prompt text from agentsJSON.
// An empty or malformed agentsJSON yields "".
func filerPromptFrom(agentsJSON string) string {
	var parsed struct {
		Filer struct {
			Prompt string `json:"prompt"`
		} `json:"filer"`
	}
	_ = json.Unmarshal([]byte(agentsJSON), &parsed)
	return parsed.Filer.Prompt
}
