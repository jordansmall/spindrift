package outcome_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/outcome"
)

// writeClassifyLog writes lines to a temp log and returns the path.
func writeClassifyLog(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "classify.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

var classifyTests = []struct {
	name      string
	lines     []string
	exitCode  int
	wantClass outcome.Class
	wantReason outcome.Reason
	wantResetAt *time.Time // nil means expect nil
}{
	{
		name: "RateLimit_WithResetsAt",
		lines: []string{
			`{"type":"error","error":{"type":"rate_limit_error","message":"Rate limit exceeded"},"resetsAt":1783192800}`,
			`Error: 429 Too Many Requests`,
		},
		exitCode:  1,
		wantClass:  outcome.Transient,
		wantReason: outcome.RateLimit,
		wantResetAt: func() *time.Time { t := time.Unix(1783192800, 0).UTC(); return &t }(),
	},
	{
		name: "RateLimit_WithResetsAt_OnSeparateLine",
		lines: []string{
			`Error: 429 Too Many Requests`,
			`{"resetsAt":1783192800}`,
		},
		exitCode:  1,
		wantClass:  outcome.Transient,
		wantReason: outcome.RateLimit,
		wantResetAt: func() *time.Time { t := time.Unix(1783192800, 0).UTC(); return &t }(),
	},
	{
		name: "RateLimit_WithoutResetsAt",
		lines: []string{
			`Error: 429 Too Many Requests`,
			`rate limit exceeded, please retry later`,
		},
		exitCode:    1,
		wantClass:   outcome.Transient,
		wantReason:  outcome.RateLimit,
		wantResetAt: nil,
	},
	{
		name: "Overloaded_529",
		lines: []string{
			`Error: 529 Overloaded`,
			`The server is temporarily overloaded.`,
		},
		exitCode:    1,
		wantClass:   outcome.Transient,
		wantReason:  outcome.Overloaded,
		wantResetAt: nil,
	},
	{
		name: "Overloaded_error_type",
		lines: []string{
			`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
		},
		exitCode:    1,
		wantClass:   outcome.Transient,
		wantReason:  outcome.Overloaded,
		wantResetAt: nil,
	},
	{
		name: "Network_ConnectionRefused",
		lines: []string{
			`dial tcp: connection refused`,
			`retrying...`,
		},
		exitCode:    1,
		wantClass:   outcome.Transient,
		wantReason:  outcome.Network,
		wantResetAt: nil,
	},
	{
		name: "Terminal_GenuineTaskFailure",
		lines: []string{
			`Agent completed with no valid outcome.`,
			`SPINDRIFT_OUTCOME issue=1 pr= status=blocked note=failed to open PR`,
		},
		exitCode:    1,
		wantClass:   outcome.Terminal,
		wantReason:  outcome.TaskFailed,
		wantResetAt: nil,
	},
	{
		name: "Terminal_NoLog",
		lines:       nil, // no lines — will use a nonexistent file
		exitCode:    1,
		wantClass:   outcome.Terminal,
		wantReason:  outcome.TaskFailed,
		wantResetAt: nil,
	},
	{
		name: "Terminal_EmptyLog",
		lines:       []string{},
		exitCode:    1,
		wantClass:   outcome.Terminal,
		wantReason:  outcome.TaskFailed,
		wantResetAt: nil,
	},
}

func TestClassify(t *testing.T) {
	for _, tc := range classifyTests {
		t.Run(tc.name, func(t *testing.T) {
			var logPath string
			if tc.name == "Terminal_NoLog" {
				logPath = filepath.Join(t.TempDir(), "nonexistent.log")
			} else {
				logPath = writeClassifyLog(t, tc.lines...)
			}

			c, err := outcome.Classify(logPath, tc.exitCode)
			if err != nil {
				t.Fatalf("Classify() error: %v", err)
			}
			if c.Class != tc.wantClass {
				t.Errorf("Class: got %q, want %q", c.Class, tc.wantClass)
			}
			if c.Reason != tc.wantReason {
				t.Errorf("Reason: got %q, want %q", c.Reason, tc.wantReason)
			}
			if tc.wantResetAt == nil {
				if c.ResetAt != nil {
					t.Errorf("ResetAt: got %v, want nil", c.ResetAt)
				}
			} else {
				if c.ResetAt == nil {
					t.Fatal("ResetAt: got nil, want non-nil")
				}
				if !c.ResetAt.Equal(*tc.wantResetAt) {
					t.Errorf("ResetAt: got %v, want %v", *c.ResetAt, *tc.wantResetAt)
				}
			}
		})
	}
}
