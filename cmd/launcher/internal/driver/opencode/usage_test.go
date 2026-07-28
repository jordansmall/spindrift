package opencode_test

import (
	"testing"

	"spindrift.dev/launcher/internal/driver/opencode"
)

// TestExtractUsage_IsDeliberateNoOp verifies that ExtractUsage always
// reports Found: false — opencode's usage-event schema is not yet wired
// (see usage.go's doc comment) — regardless of log content.
func TestExtractUsage_IsDeliberateNoOp(t *testing.T) {
	logPath := opencode.WriteLog(t,
		`{"type":"text","part":{"text":"hello"}}`,
	)

	report, err := opencode.ExtractUsage(logPath)
	if err != nil {
		t.Fatalf("ExtractUsage: %v", err)
	}
	if report.Found {
		t.Errorf("Found: got true, want false")
	}
}
