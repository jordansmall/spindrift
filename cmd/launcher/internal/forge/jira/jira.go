// Package jira is the jira backend's Go half: its Strategy, generated knobs,
// and the knob validation core runs at startup. The tracker itself is the jira
// Connector, built through the Strategy (ADR 0068); code lands via whichever
// Code Forge CODE_FORGE selects (ADR 0013).
package jira

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"spindrift.dev/launcher/internal/forge"
)

var statusMappingKeys = map[string]forge.DispatchState{
	"dispatchable": forge.Dispatchable,
	"inProgress":   forge.InProgress,
	"complete":     forge.Complete,
	"failed":       forge.Failed,
	"ambiguous":    forge.Ambiguous,
}

// StatusTarget is what a canonical state maps to in Jira: the native status
// name, plus the resolution to set on the transition (empty leaves it to the
// workflow's default).
type StatusTarget struct {
	Status     string `json:"status"`
	Resolution string `json:"resolution"`
}

// statusTargetFields are the fields a JIRA_STATUS_MAPPING object value allows.
var statusTargetFields = []string{"status", "resolution"}

// quotedStatusTargetFields renders statusTargetFields as the "status", "resolution"
// list the object-shape error message shows.
func quotedStatusTargetFields() string {
	quoted := make([]string, len(statusTargetFields))
	for i, f := range statusTargetFields {
		quoted[i] = fmt.Sprintf("%q", f)
	}
	return strings.Join(quoted, ", ")
}

// jsonTypeName names v's JSON type as the error messages report it.
func jsonTypeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "bool"
	case []any:
		return "array"
	default:
		return "object"
	}
}

// ParseStatusMapping parses the JIRA_STATUS_MAPPING knob, a JSON object mapping
// statusMappingKeys to a native Jira status name or a {"status", "resolution"}
// object. An unknown key or field is an error so a typo fails fast at startup
// instead of silently dropping the mapping. Keys are checked in sorted order so
// the first error is deterministic; the Nix mirror reports the same one. Two
// keys may share a status (compared with strings.EqualFold, as the claim check
// is) only if they are failed and ambiguous, which both mean a human must act
// and fit one Blocked column; any other share would, say, make every ready card
// look claimed. The Nix eval-time mirror folds ASCII only, so a non-ASCII
// case-only share is caught at startup only.
func ParseStatusMapping(s string) (map[forge.DispatchState]StatusTarget, error) {
	out := map[forge.DispatchState]StatusTarget{}
	if s == "" {
		return out, nil
	}
	var top any
	if err := json.Unmarshal([]byte(s), &top); err != nil {
		return nil, fmt.Errorf("parse JIRA_STATUS_MAPPING: %w", err)
	}
	if top == nil {
		return out, nil
	}
	raw, ok := top.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("JIRA_STATUS_MAPPING: expected a JSON object, got %s", jsonTypeName(top))
	}
	keys := slices.Sorted(maps.Keys(raw))
	for _, key := range keys {
		state, ok := statusMappingKeys[key]
		if !ok {
			return nil, fmt.Errorf("JIRA_STATUS_MAPPING: unknown key %q (want one of dispatchable, inProgress, complete, failed, ambiguous)", key)
		}
		target, err := parseStatusTarget(key, raw[key])
		if err != nil {
			return nil, err
		}
		out[state] = target
	}
	// Keys are sorted so the error names the same pair every run and
	// ambiguous always precedes failed.
	for i, k := range keys {
		status := out[statusMappingKeys[k]].Status
		if status == "" {
			continue
		}
		for _, p := range keys[:i] {
			held := out[statusMappingKeys[p]].Status
			if !strings.EqualFold(held, status) || (p == "ambiguous" && k == "failed") {
				continue
			}
			return nil, fmt.Errorf("JIRA_STATUS_MAPPING: keys %q and %q both map to status %q (only failed and ambiguous may share a status)", p, k, held)
		}
	}
	return out, nil
}

// parseStatusTarget decodes one JIRA_STATUS_MAPPING value: null or a string is
// a bare status name (empty means unmapped), an object carries the fields in
// statusTargetFields.
func parseStatusTarget(key string, v any) (StatusTarget, error) {
	switch v := v.(type) {
	case nil:
		return StatusTarget{}, nil
	case string:
		return StatusTarget{Status: v}, nil
	case map[string]any:
		fields := map[string]string{}
		for _, name := range slices.Sorted(maps.Keys(v)) {
			if !slices.Contains(statusTargetFields, name) {
				return StatusTarget{}, fmt.Errorf("JIRA_STATUS_MAPPING: key %q: unknown field %q (want status, resolution)", key, name)
			}
			str, ok := v[name].(string)
			if !ok {
				return StatusTarget{}, fmt.Errorf("JIRA_STATUS_MAPPING: key %q: field %q must be a string, got %s", key, name, jsonTypeName(v[name]))
			}
			fields[name] = str
		}
		if fields["status"] == "" {
			return StatusTarget{}, fmt.Errorf("JIRA_STATUS_MAPPING: key %q: %q must be a non-empty string", key, "status")
		}
		if res, present := fields["resolution"]; present && res == "" {
			return StatusTarget{}, fmt.Errorf("JIRA_STATUS_MAPPING: key %q: %q must be a non-empty string", key, "resolution")
		}
		return StatusTarget{Status: fields["status"], Resolution: fields["resolution"]}, nil
	default:
		return StatusTarget{}, fmt.Errorf("JIRA_STATUS_MAPPING: key %q: expected a status name or a {%s} object, got %s", key, quotedStatusTargetFields(), jsonTypeName(v))
	}
}

// The JIRA_LIFECYCLE modes.
const (
	LifecycleHybrid = "hybrid"
	LifecycleStatus = "status"
)

// CheckLifecycle validates a JIRA_LIFECYCLE value against the parsed status
// mapping: status mode lets the mapped status stand alone as the work state, so
// every work state must name one. lib/jira-status-mapping.nix's lifecycleError
// mirrors it; testdata/lifecycle-cases.json pins both. Unmapped states are
// listed sorted so the error is stable.
func CheckLifecycle(lifecycle string, mapping map[forge.DispatchState]StatusTarget) error {
	switch lifecycle {
	case "", LifecycleHybrid:
		return nil
	case LifecycleStatus:
	default:
		return fmt.Errorf("JIRA_LIFECYCLE: unknown mode %q (want hybrid or status)", lifecycle)
	}
	var unmapped []string
	for _, key := range slices.Sorted(maps.Keys(statusMappingKeys)) {
		if mapping[statusMappingKeys[key]].Status == "" {
			unmapped = append(unmapped, key)
		}
	}
	if len(unmapped) > 0 {
		return fmt.Errorf("JIRA_LIFECYCLE=status: JIRA_STATUS_MAPPING must map every work state; unmapped: %s", strings.Join(unmapped, ", "))
	}
	return nil
}

// ValidateJiraEnv checks the JIRA_* knobs required when ISSUE_TRACKER=jira,
// returning an error for the first unmet requirement.
func ValidateJiraEnv(baseURL, projectKey, token, statusMapping, lifecycle string) error {
	if baseURL == "" {
		return fmt.Errorf("set JIRA_BASE_URL (Jira site base URL) when ISSUE_TRACKER=jira")
	}
	if projectKey == "" {
		return fmt.Errorf("set JIRA_PROJECT_KEY when ISSUE_TRACKER=jira")
	}
	if token == "" {
		return fmt.Errorf("set JIRA_TOKEN when ISSUE_TRACKER=jira")
	}
	mapping, err := ParseStatusMapping(statusMapping)
	if err != nil {
		return err
	}
	return CheckLifecycle(lifecycle, mapping)
}
