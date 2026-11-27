package jira

import (
	"testing"

	"spindrift.dev/launcher/internal/dispatchkind"
)

func TestStatusMappingFor(t *testing.T) {
	t.Parallel()
	const mapping = `{"dispatchable":"To Do"}`
	if got := statusMappingFor(dispatchkind.TrackerWork, mapping); got != mapping {
		t.Errorf("work = %q, want the mapping", got)
	}
	if got := statusMappingFor(dispatchkind.TrackerResearch, mapping); got != "" {
		t.Errorf("research = %q, want empty", got)
	}
}
