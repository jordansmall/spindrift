package claude

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/usage"
)

// TestExtractUsage_BreakdownByRoleError confirms ExtractUsage still returns
// the aggregate totals it already parsed via LastInLog when BreakdownByRole
// fails with a real I/O error, rather than discarding them (issue #674).
func TestExtractUsage_BreakdownByRoleError(t *testing.T) {
	line := `{"type":"result","num_turns":3,"total_cost_usd":0.5,"usage":{"input_tokens":100,"output_tokens":50}}`
	path := filepath.Join(t.TempDir(), "test.log")
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig := breakdownByRole
	breakdownByRole = func(string) ([]usage.RoleUsage, error) {
		return nil, errors.New("simulated I/O error")
	}
	defer func() { breakdownByRole = orig }()

	report, err := ExtractUsage(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !report.Found {
		t.Fatal("expected Found=true")
	}
	if report.InputTokens != 100 || report.OutputTokens != 50 {
		t.Errorf("Usage: got %+v, want InputTokens=100 OutputTokens=50", report.Usage)
	}
	if report.Roles != nil {
		t.Errorf("Roles: got %+v, want nil", report.Roles)
	}
}
