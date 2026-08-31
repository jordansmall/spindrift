package main

// choiceKnobRow is one entry in the ordered choice-knob registry (issue
// #2924): validate() and validateConfig() (both main.go) walk the same
// choiceKnobRegistry slice instead of each hand-maintaining its own
// enumeration of the six knobs, so the two can never drift on which knobs
// get validated.
type choiceKnobRow struct {
	Env   string
	Value func(c config) string
	// AfterCrossKnobChecks marks the one row (BOX_FORGE_AND_ISSUE_ACCESS)
	// that validate() must check after its cross-knob checks rather than
	// before. checks.go's launcherChecks doc comment documents that the
	// nine doctor.Check rows split into launcherRequiredKnobChecks (6 rows,
	// run before validate()'s validateChoice calls) and
	// launcherCrossKnobChecks (3 rows, run after those calls) to match
	// validate()'s own ordering, and BOX_FORGE_AND_ISSUE_ACCESS's
	// validateChoice call is deliberately placed after that same
	// cross-knob block. splitChoiceKnobRegistry derives the same split
	// from this field, rather than a hardcoded index, so reordering
	// choiceKnobRegistry can never silently break it.
	AfterCrossKnobChecks bool
}

// choiceKnobRegistry is the ordered set of the six choice knobs whose
// resolved values validateChoice checks against schemaFlags' declared
// choices. This is the one place the six knobs get enumerated; validate()
// and validateConfig() (main.go) both walk this registry instead of each
// carrying its own hand-written list.
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
// post-cross-knob-check rows, preserving registry order within each —
// deriving the split from each row's own AfterCrossKnobChecks field rather
// than a hardcoded index, mirroring splitGateRegistryByNetwork
// (launchgates.go).
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

// walkChoiceKnobRegistry walks rows in order, running each through
// validateChoice, and collects every non-nil error — mirroring
// walkGateRegistry's collectAll shape (launchgates.go) for the same
// stop-vs-collect duplication, minus the check/report writers walkGateRegistry
// threads through, since choice-knob validation has no report output of its
// own. When collectAll is false, the first failure is appended and the walk
// returns immediately, matching validate()'s fail-fast precedence; when true,
// every row is walked and every failure collected, for a caller
// (validateConfig) to errors.Join so every simultaneously-invalid knob is
// reported.
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

// validateChoiceKnobsFailFast returns the first non-nil error encountered
// walking rows, or nil — a thin wrapper over walkChoiceKnobRegistry with
// collectAll false.
func validateChoiceKnobsFailFast(c config, rows []choiceKnobRow) error {
	errs := walkChoiceKnobRegistry(c, rows, false)
	if len(errs) == 0 {
		return nil
	}
	return errs[0]
}

// validateChoiceKnobsErrors returns every non-nil error encountered walking
// rows — a thin wrapper over walkChoiceKnobRegistry with collectAll true.
func validateChoiceKnobsErrors(c config, rows []choiceKnobRow) []error {
	return walkChoiceKnobRegistry(c, rows, true)
}
