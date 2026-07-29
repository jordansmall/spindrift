package usage_test

import (
	"testing"

	"spindrift.dev/launcher/internal/usage"
)

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		ms   int64
		want string
	}{
		{0, "0s"},
		{999, "0s"},
		{1000, "1s"},
		{5000, "5s"},
		{59000, "59s"},
		{60000, "1m 0s"},
		{65000, "1m 5s"},
		{3600000, "1h 0m 0s"},
		{3665000, "1h 1m 5s"},
		{7384000, "2h 3m 4s"},
	}
	for _, tc := range cases {
		got := usage.FormatDuration(tc.ms)
		if got != tc.want {
			t.Errorf("FormatDuration(%d): got %q, want %q", tc.ms, got, tc.want)
		}
	}
}

func TestModelUsage_Fields(t *testing.T) {
	mu := usage.ModelUsage{
		Model:                "claude-opus-4-8",
		UncachedInputTokens:  1,
		OutputTokens:         2,
		CacheReadInputTokens: 3,
		CacheWrite5mTokens:   4,
		CacheWrite1hTokens:   5,
	}
	report := usage.Report{SummedByModel: []usage.ModelUsage{mu}}

	if len(report.SummedByModel) != 1 {
		t.Fatalf("len(report.SummedByModel) = %d, want 1", len(report.SummedByModel))
	}
	got := report.SummedByModel[0]
	if got.Model != "claude-opus-4-8" {
		t.Errorf("Model = %q, want %q", got.Model, "claude-opus-4-8")
	}
	if got.UncachedInputTokens != 1 {
		t.Errorf("UncachedInputTokens = %d, want 1", got.UncachedInputTokens)
	}
	if got.OutputTokens != 2 {
		t.Errorf("OutputTokens = %d, want 2", got.OutputTokens)
	}
	if got.CacheReadInputTokens != 3 {
		t.Errorf("CacheReadInputTokens = %d, want 3", got.CacheReadInputTokens)
	}
	if got.CacheWrite5mTokens != 4 {
		t.Errorf("CacheWrite5mTokens = %d, want 4", got.CacheWrite5mTokens)
	}
	if got.CacheWrite1hTokens != 5 {
		t.Errorf("CacheWrite1hTokens = %d, want 5", got.CacheWrite1hTokens)
	}
}
