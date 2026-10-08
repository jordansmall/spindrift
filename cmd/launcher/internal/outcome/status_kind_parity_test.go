package outcome_test

import (
	"slices"
	"testing"

	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/outcome"
)

// The descriptor rows hand-copy lib/prompt-contract.nix outcomeStatusSets
// because dispatchkind cannot import outcome; this guards them against
// drifting from the generated vocabularies.
var generatedStatuses = map[*dispatchkind.Descriptor][]string{
	dispatchkind.Work:     outcome.WorkStatuses,
	dispatchkind.Research: outcome.ResearchStatuses,
}

func TestDescriptorStatusesMatchGenerated(t *testing.T) {
	for d, want := range generatedStatuses {
		if !slices.Equal(d.Statuses, want) {
			t.Errorf("%s.Statuses = %v, want %v", d.Name, d.Statuses, want)
		}
	}
}

// A kind with no outcomeStatusSets row must carry no Statuses; a new row or
// kind fails here until it is wired into generatedStatuses.
func TestOtherDescriptorsHaveNoStatuses(t *testing.T) {
	for _, d := range dispatchkind.All {
		if _, ok := generatedStatuses[d]; ok {
			continue
		}
		if len(d.Statuses) != 0 {
			t.Errorf("%s.Statuses = %v, want empty", d.Name, d.Statuses)
		}
	}
}
