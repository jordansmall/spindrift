package outcome_test

import (
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/outcome"
)

// TestMarkerChannelTokens pins the regen-generated MarkerChannelTokens slice
// (cmd/launcher/internal/outcome/markerchannels_gen.go, issue #2974) against
// lib/prompt-contract.nix's markerChannels registry, in registry row order:
// outcome, comment, pr-intent, issue-intent, review-verdict.
func TestMarkerChannelTokens(t *testing.T) {
	want := []string{
		"SPINDRIFT_OUTCOME",
		"SPINDRIFT_COMMENT",
		"SPINDRIFT_PR_INTENT",
		"SPINDRIFT_ISSUE_INTENT",
		"VERDICT:",
	}
	if got := outcome.MarkerChannelTokens; !reflect.DeepEqual(got, want) {
		t.Errorf("MarkerChannelTokens = %v, want %v", got, want)
	}
}

// TestMarkerChannelFieldShapes pins the regen-generated
// MarkerChannelFieldShapes map (cmd/launcher/internal/outcome/markerchannels_gen.go,
// issue #2996) against lib/prompt-contract.nix's markerChannels registry's
// fieldShape column, keyed by each row's token.
func TestMarkerChannelFieldShapes(t *testing.T) {
	want := map[string]string{
		"SPINDRIFT_OUTCOME":      "issue=<num> landing=<landing-ref> status=<status> note=<text>",
		"SPINDRIFT_COMMENT":      "<nonce> <base64-payload>",
		"SPINDRIFT_PR_INTENT":    "<nonce> <base64-payload>",
		"SPINDRIFT_ISSUE_INTENT": "<nonce> <base64-payload>",
		"VERDICT:":               "APPROVE | BLOCK",
	}
	if got := outcome.MarkerChannelFieldShapes; !reflect.DeepEqual(got, want) {
		t.Errorf("MarkerChannelFieldShapes = %v, want %v", got, want)
	}
}
