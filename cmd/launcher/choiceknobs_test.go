package main

import (
	"reflect"
	"strings"
	"testing"
)

// TestSplitChoiceKnobRegistry_PreservesOrderWithinGroups proves
// splitChoiceKnobRegistry partitions by AfterCrossKnobChecks while preserving
// each group's original registry order, mirroring
// TestSplitGateRegistryByNetwork's shape for the sibling gate registry.
func TestSplitChoiceKnobRegistry_PreservesOrderWithinGroups(t *testing.T) {
	registry := []choiceKnobRow{
		{Env: "A", AfterCrossKnobChecks: false},
		{Env: "B", AfterCrossKnobChecks: true},
		{Env: "C", AfterCrossKnobChecks: false},
		{Env: "D", AfterCrossKnobChecks: true},
		{Env: "E", AfterCrossKnobChecks: false},
	}

	before, after := splitChoiceKnobRegistry(registry)

	wantBefore := []string{"A", "C", "E"}
	wantAfter := []string{"B", "D"}

	gotBefore := envNames(before)
	gotAfter := envNames(after)

	if !reflect.DeepEqual(gotBefore, wantBefore) {
		t.Fatalf("before envs = %v, want %v", gotBefore, wantBefore)
	}
	if !reflect.DeepEqual(gotAfter, wantAfter) {
		t.Fatalf("after envs = %v, want %v", gotAfter, wantAfter)
	}
}

func envNames(rows []choiceKnobRow) []string {
	var names []string
	for _, r := range rows {
		names = append(names, r.Env)
	}
	return names
}

// TestChoiceKnobRegistry_OnlyBoxForgeAndIssueAccessIsAfterCrossKnobChecks pins
// that the real package-level choiceKnobRegistry has exactly one row with
// AfterCrossKnobChecks: true, and that row is BOX_FORGE_AND_ISSUE_ACCESS.
// Unlike TestSplitChoiceKnobRegistry_PreservesOrderWithinGroups (a synthetic
// A-E registry that only exercises splitChoiceKnobRegistry's partitioning
// logic in the abstract), this reads choiceKnobRegistry itself.
// TestValidate_RegistryProxyCredentialErrorPrecedesBoxForgeAndIssueAccessChoiceError
// (main_test.go) also breaks if the flag is flipped, but only indirectly --
// via validate()'s error-precedence outcome when a cross-knob check and the
// BOX_FORGE_AND_ISSUE_ACCESS choice are both broken at once. This test pins
// the row's own field directly, so it stays a narrower, more localized
// signal when it fails: a flip here fails right at the registry, before it
// ever gets a chance to manifest as a precedence bug in validate().
func TestChoiceKnobRegistry_OnlyBoxForgeAndIssueAccessIsAfterCrossKnobChecks(t *testing.T) {
	var afterRows []string
	for _, r := range choiceKnobRegistry {
		if r.AfterCrossKnobChecks {
			afterRows = append(afterRows, r.Env)
		}
	}

	if len(afterRows) != 1 {
		t.Fatalf("choiceKnobRegistry has %d row(s) with AfterCrossKnobChecks: true, want 1: %v", len(afterRows), afterRows)
	}
	if afterRows[0] != "BOX_FORGE_AND_ISSUE_ACCESS" {
		t.Fatalf("choiceKnobRegistry's sole AfterCrossKnobChecks row is %q, want %q", afterRows[0], "BOX_FORGE_AND_ISSUE_ACCESS")
	}
}

// TestChoiceKnobRegistry_HasSevenExpectedRows pins choiceKnobRegistry's full
// row membership and order, not just the single AfterCrossKnobChecks row
// TestChoiceKnobRegistry_OnlyBoxForgeAndIssueAccessIsAfterCrossKnobChecks
// covers above. Without this, a knob silently dropped from the registry (or
// renamed to an Env string schemaFlags no longer recognizes) would leave
// that knob's choice unvalidated by both validate() and validateConfig(),
// with the rest of the suite staying green -- validateChoice is a no-op for
// an Env absent from schemaFlags, so nothing else would notice the gap.
func TestChoiceKnobRegistry_HasSevenExpectedRows(t *testing.T) {
	want := []string{
		"MERGE_MODE",
		"MERGE_METHOD",
		"SYNC_METHOD",
		"OVERLAP_GATE",
		"NETWORK_MODE",
		"REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT",
		"BOX_FORGE_AND_ISSUE_ACCESS",
	}

	got := envNames(choiceKnobRegistry)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("envNames(choiceKnobRegistry) = %v, want %v", got, want)
	}
}

// TestValidateChoiceKnobsFailFast_ReturnsFirstError proves
// validateChoiceKnobsFailFast returns the first row's error and never
// evaluates rows after it -- mirroring
// TestWalkGateRegistry_StopsAtFirstFailure's shape (launchgates_test.go):
// each row's Value closure appends its own Env to a shared calls slice, so
// the assertion can pin exactly which rows got evaluated, not just the
// returned error.
func TestValidateChoiceKnobsFailFast_ReturnsFirstError(t *testing.T) {
	var calls []string
	c := config{schemaConfig: schemaConfig{mergeMode: "bogus"}}
	rows := []choiceKnobRow{
		{Env: "MERGE_MODE", Value: func(c config) string { calls = append(calls, "MERGE_MODE"); return c.mergeMode }},
		{Env: "OVERLAP_GATE", Value: func(c config) string { calls = append(calls, "OVERLAP_GATE"); return "defer" }},
		{Env: "UNREACHABLE_ROW", Value: func(c config) string { calls = append(calls, "UNREACHABLE_ROW"); return "also-bogus" }},
	}

	err := validateChoiceKnobsFailFast(c, rows)
	if err == nil {
		t.Fatal("validateChoiceKnobsFailFast() = nil, want an error")
	}

	wantErr := validateChoice("MERGE_MODE", "bogus")
	if err.Error() != wantErr.Error() {
		t.Fatalf("validateChoiceKnobsFailFast() error = %q, want first row's error %q", err, wantErr)
	}

	want := []string{"MERGE_MODE"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("call order = %v, want %v (only the first, failing row must ever be evaluated)", calls, want)
	}
}

// TestValidateChoiceKnobsErrors_CollectsAll proves validateChoiceKnobsErrors
// walks every row rather than stopping at the first failure, returning one
// error per failing row.
func TestValidateChoiceKnobsErrors_CollectsAll(t *testing.T) {
	c := config{schemaConfig: schemaConfig{mergeMode: "bogus", overlapGate: "bogus"}}
	rows := []choiceKnobRow{
		{Env: "MERGE_MODE", Value: func(c config) string { return c.mergeMode }},
		{Env: "MERGE_METHOD", Value: func(c config) string { return "rebase" }},
		{Env: "OVERLAP_GATE", Value: func(c config) string { return c.overlapGate }},
	}

	errs := validateChoiceKnobsErrors(c, rows)
	if len(errs) != 2 {
		t.Fatalf("validateChoiceKnobsErrors() returned %d errors, want 2: %v", len(errs), errs)
	}

	wantFirst := validateChoice("MERGE_MODE", "bogus")
	wantSecond := validateChoice("OVERLAP_GATE", "bogus")
	if errs[0].Error() != wantFirst.Error() {
		t.Fatalf("errs[0] = %q, want %q", errs[0], wantFirst)
	}
	if errs[1].Error() != wantSecond.Error() {
		t.Fatalf("errs[1] = %q, want %q", errs[1], wantSecond)
	}
}

// TestChoiceKnobRegistry_InjectedRowReachesBothValidators proves AC3: an 8th
// row appended to the package-level choiceKnobRegistry reaches both
// validate() and validateConfig() with zero edits to either function. It
// uses CODE_FORGE -- a real schemaFlags env with non-empty choices that isn't
// one of the seven knobs already in the registry -- rather than a made-up env
// name, since validateChoice returns nil for any env absent from
// schemaFlags and so could never prove the injected row was actually
// walked.
func TestChoiceKnobRegistry_InjectedRowReachesBothValidators(t *testing.T) {
	withChoiceKnobRegistry(t, append(append([]choiceKnobRow{}, choiceKnobRegistry...), choiceKnobRow{
		Env:   "CODE_FORGE",
		Value: func(c config) string { return "bogus-code-forge" },
	}))

	c := minimalValidConfig()

	if err := validate(c); err == nil || !strings.Contains(err.Error(), "CODE_FORGE") {
		t.Fatalf("validate() = %v, want an error mentioning CODE_FORGE", err)
	}
	if err := validateConfig(c); err == nil || !strings.Contains(err.Error(), "CODE_FORGE") {
		t.Fatalf("validateConfig() = %v, want an error mentioning CODE_FORGE", err)
	}
}
