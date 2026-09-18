package main

// choiceKnobRow is one entry in the ordered choice-knob registry (issue #2924).
type choiceKnobRow struct {
	Env   string
	Value func(c config) string
	// AfterCrossKnobChecks marks the one row (BOX_FORGE_AND_ISSUE_ACCESS) that
	// validate() must check after its cross-knob checks rather than before.
	// splitChoiceKnobRegistry derives the split from this field rather than a
	// hardcoded index, so reordering choiceKnobRegistry cannot silently break it.
	AfterCrossKnobChecks bool
}

// choiceKnobRegistry is the ordered set of choice knobs whose resolved values
// validateChoice checks against schemaFlags' declared choices. It is the only
// enumeration of them: validate() and validateConfig() (main.go) both walk it,
// so the two cannot drift on which knobs get validated.
var choiceKnobRegistry = []choiceKnobRow{
	{Env: "MERGE_MODE", Value: func(c config) string { return c.mergeMode }},
	{Env: "MERGE_METHOD", Value: func(c config) string { return c.mergeMethod }},
	{Env: "SYNC_METHOD", Value: func(c config) string { return c.syncMethod }},
	{Env: "OVERLAP_GATE", Value: func(c config) string { return c.overlapGate }},
	{Env: "NETWORK_MODE", Value: func(c config) string { return c.networkMode }},
	{
		Env:                  "BOX_FORGE_AND_ISSUE_ACCESS",
		Value:                func(c config) string { return c.boxForgeAndIssueAccess },
		AfterCrossKnobChecks: true,
	},
}

// splitChoiceKnobRegistry partitions registry into its pre- and
// post-cross-knob-check rows, preserving registry order within each.
func splitChoiceKnobRegistry(registry []choiceKnobRow) (before, after []choiceKnobRow) {
	for _, r := range registry {
		if r.AfterCrossKnobChecks {
			after = append(after, r)
		} else {
			before = append(before, r)
		}
	}
	return before, after
}

// walkChoiceKnobRegistry runs each row in order through validateChoice. When
// collectAll is false it returns on the first failure, matching validate()'s
// fail-fast precedence. When it is true it walks every row, so validateConfig
// can errors.Join every knob that is invalid at once.
func walkChoiceKnobRegistry(c config, rows []choiceKnobRow, collectAll bool) []error {
	var errs []error
	for _, r := range rows {
		if err := validateChoice(r.Env, r.Value(c)); err != nil {
			errs = append(errs, err)
			if !collectAll {
				return errs
			}
		}
	}
	return errs
}

func validateChoiceKnobsFailFast(c config, rows []choiceKnobRow) error {
	errs := walkChoiceKnobRegistry(c, rows, false)
	if len(errs) == 0 {
		return nil
	}
	return errs[0]
}

func validateChoiceKnobsErrors(c config, rows []choiceKnobRow) []error {
	return walkChoiceKnobRegistry(c, rows, true)
}
