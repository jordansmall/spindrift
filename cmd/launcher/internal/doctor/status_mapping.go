package doctor

import (
	"fmt"

	"spindrift.dev/launcher/internal/forge"
)

// StatusMappingCheckName names the mapped-status Check row.
const StatusMappingCheckName = "status-mapping"

// StatusMappingChecks builds the row that fails (Required) when
// JIRA_STATUS_MAPPING names a status the project's workflow does not have: in
// either lifecycle mode the transition to it can never happen. A tracker
// without the capability gets no row.
func StatusMappingChecks(caps forge.Capabilities) []Check {
	checker := caps.StatusMappingChecker
	if checker == nil {
		return nil
	}
	return []Check{{
		Name: StatusMappingCheckName,
		Tier: Required,
		Probe: func() (any, error) {
			missing, err := checker.CheckMappedStatuses()
			if err != nil {
				// An unreadable statuses endpoint (a permission gap) cannot show
				// the mapping is wrong, so it must not block Run.
				return nil, fmt.Errorf("status mapping check failed: %w: %w", err, ErrDegraded)
			}
			if len(missing) > 0 {
				return nil, fmt.Errorf("JIRA_STATUS_MAPPING names status(es) the project's workflow does not have: %s", forge.QuoteList(missing))
			}
			return nil, nil
		},
		SuccessMsg: func(any) string { return "every mapped status exists in the project's workflow" },
	}}
}
