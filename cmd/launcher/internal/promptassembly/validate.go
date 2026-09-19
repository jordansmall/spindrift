package promptassembly

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// The When values Validate compares a row's When field against. Using named
// constants turns a typo into a compile error instead of a runtime trip through
// Validate's default arm.
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
// validateMarkers registry. The JSON tags copy the nix attrset's field names
// literally so that registry's builtins.toJSON output decodes without renaming.
type ValidateMarkerRow struct {
	ID string `json:"id"`
	// Marker is the literal substring Validate scans a row's haystack for.
	Marker string `json:"marker"`
	// Carrier records which rendered text is supposed to carry Marker.
	// Validate never branches on it.
	Carrier string `json:"carrier"`
	// Severity is "reject" (fatal, stop checking further rows) or "warn"
	// (advisory, collected while checking continues).
	Severity string `json:"severity"`
	// When names the gate that activates this row, one of the when constants
	// above.
	When string `json:"when"`
	// Message is the row's diagnostic prose, marker already interpolated by
	// the nix registry.
	Message string `json:"message"`
}

// LoadValidateMarkers decodes a validateMarkers registry from r: a bare JSON
// array of rows, the shape lib/prompt-contract.nix's builtins.toJSON writes.
func LoadValidateMarkers(r io.Reader) ([]ValidateMarkerRow, error) {
	var rows []ValidateMarkerRow
	if err := json.NewDecoder(r).Decode(&rows); err != nil {
		return nil, fmt.Errorf("decode validate markers registry: %w", err)
	}
	return rows, nil
}

// LoadValidateMarkersFile opens path and loads it via LoadValidateMarkers.
func LoadValidateMarkersFile(path string) ([]ValidateMarkerRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open validate markers registry %s: %w", path, err)
	}
	defer f.Close()
	return LoadValidateMarkers(f)
}

// Validate runs the reject/warn marker matrix at the tail of prompt assembly,
// after every fragment is rendered and before the Driver is invoked (it
// succeeds agent/entrypoint.sh's _validate_prompt_contract; issues #2249 and
// #2356). It reads gates from one Gates(e) call so it cannot drift from the
// gating Assemble used, and dispatches on row data rather than id (#2318).
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
			// ReviewPromptText, not Handoff.ReviewPromptFile: the latter became
			// a real on-disk path and no longer holds rendered text (#2975).
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

// filerPromptFrom mirrors _validate_prompt_contract's `jq -r '.filer.prompt //
// empty'` (entrypoint.sh: 583). Empty or malformed agentsJSON yields "".
func filerPromptFrom(agentsJSON string) string {
	var parsed struct {
		Filer struct {
			Prompt string `json:"prompt"`
		} `json:"filer"`
	}
	_ = json.Unmarshal([]byte(agentsJSON), &parsed)
	return parsed.Filer.Prompt
}
