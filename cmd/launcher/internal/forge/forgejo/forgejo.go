// Package forgejo is the Forgejo adapter. The IssueTracker, CodeForge and
// PRForge seams are all served by the Connector (connector_tracker.go,
// connector_forge.go); this file holds the merge-style and env helpers around it.
package forgejo

import (
	"fmt"
)

// MergeStyle resolves MergeMethod to a merge style. An unset method resolves
// to "rebase", matching the github adapter's default; the forge build gates on
// rebase and AutoMergeRemedy names the style.
func MergeStyle(method string) string {
	switch method {
	case "merge":
		return "merge"
	case "squash":
		return "squash"
	default:
		return "rebase"
	}
}

// ValidateForgejoEnv checks the FORGEJO_* knobs required when <knob>=forgejo
// and returns an error for the first unmet requirement. knob names the
// selecting env var and is quoted in the error.
func ValidateForgejoEnv(knob, baseURL string) error {
	if baseURL == "" {
		return errBaseURLRequired(knob)
	}
	return nil
}

// errBaseURLRequired is the missing-FORGEJO_BASE_URL error for the axis knob.
func errBaseURLRequired(knob string) error {
	return fmt.Errorf("set FORGEJO_BASE_URL (Forgejo instance base URL) when %s=forgejo", knob)
}
