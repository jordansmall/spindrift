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
	name        string
	lines       []string
	wantClass   outcome.Class
	wantReason  outcome.Reason
	wantResetAt *time.Time // nil means expect nil
}{
	{
		name: "RateLimit_WithResetsAt",
		lines: []string{
			`{"type":"error","error":{"type":"rate_limit_error","message":"Rate limit exceeded"},"resetsAt":1783192800}`,
			`Error: 429 Too Many Requests`,
		},
		wantClass:   outcome.Transient,
		wantReason:  outcome.RateLimit,
		wantResetAt: func() *time.Time { t := time.Unix(1783192800, 0).UTC(); return &t }(),
	},
	{
		name: "RateLimit_WithResetsAt_OnSeparateLine",
		lines: []string{
			`Error: 429 Too Many Requests`,
			`{"resetsAt":1783192800}`,
		},
		wantClass:   outcome.Transient,
		wantReason:  outcome.RateLimit,
		wantResetAt: func() *time.Time { t := time.Unix(1783192800, 0).UTC(); return &t }(),
	},
	{
		name: "RateLimit_WithoutResetsAt",
		lines: []string{
			`Error: 429 Too Many Requests`,
			`rate limit exceeded, please retry later`,
		},
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
		wantClass:   outcome.Transient,
		wantReason:  outcome.Overloaded,
		wantResetAt: nil,
	},
	{
		name: "Overloaded_error_type",
		lines: []string{
			`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
		},
		wantClass:   outcome.Transient,
		wantReason:  outcome.Overloaded,
		wantResetAt: nil,
	},
	{
		// Bare "Overloaded" plain-text marker — exercises the lowest-priority
		// Overloaded pattern, which is not reached by overloaded_error or
		// "529 Overloaded" test strings.
		name: "Overloaded_PlainText",
		lines: []string{
			`Overloaded`,
		},
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
		wantClass:   outcome.Transient,
		wantReason:  outcome.Network,
		wantResetAt: nil,
	},
	{
		name: "Network_ConnectionReset",
		lines: []string{
			`read tcp 127.0.0.1:42000->127.0.0.1:443: connection reset by peer`,
		},
		wantClass:   outcome.Transient,
		wantReason:  outcome.Network,
		wantResetAt: nil,
	},
	{
		name: "Network_DialTcp",
		lines: []string{
			`dial tcp 1.2.3.4:443: i/o timeout`,
		},
		wantClass:   outcome.Transient,
		wantReason:  outcome.Network,
		wantResetAt: nil,
	},
	{
		name: "Network_RequestCanceled",
		lines: []string{
			`Get "https://api.anthropic.com/v1/messages": net/http: request canceled`,
		},
		wantClass:   outcome.Transient,
		wantReason:  outcome.Network,
		wantResetAt: nil,
	},
	{
		name: "Network_ContextDeadlineExceeded",
		lines: []string{
			`Post "https://api.anthropic.com/v1/messages": context deadline exceeded`,
		},
		wantClass:   outcome.Transient,
		wantReason:  outcome.Network,
		wantResetAt: nil,
	},
	{
		name: "Network_NoSuchHost",
		lines: []string{
			`lookup api.anthropic.com on 8.8.8.8:53: no such host`,
		},
		wantClass:   outcome.Transient,
		wantReason:  outcome.Network,
		wantResetAt: nil,
	},
	{
		// First matching line in the log wins even when a higher-priority pattern
		// (rate_limit_error) appears on a later line.
		name: "Network_FirstMatchWins_EarlierLineBeatsLaterHigherPriority",
		lines: []string{
			`connection refused`,
			`rate_limit_error occurred`,
		},
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
		wantClass:   outcome.Terminal,
		wantReason:  outcome.TaskFailed,
		wantResetAt: nil,
	},
	{
		name:        "Terminal_NoLog",
		lines:       nil, // no lines — will use a nonexistent file
		wantClass:   outcome.Terminal,
		wantReason:  outcome.TaskFailed,
		wantResetAt: nil,
	},
	{
		name:        "Terminal_EmptyLog",
		lines:       []string{},
		wantClass:   outcome.Terminal,
		wantReason:  outcome.TaskFailed,
		wantResetAt: nil,
	},
	{
		// Claude Code session-limit: structured JSON error type.
		name: "RateLimit_SessionLimit_ErrorType",
		lines: []string{
			`{"type":"error","error":{"type":"usage_limit_reached","message":"Claude Code usage limit reached"}}`,
		},
		wantClass:   outcome.Transient,
		wantReason:  outcome.RateLimit,
		wantResetAt: nil,
	},
	{
		// Claude Code session-limit with a resetsAt field — ResetAt must propagate.
		name: "RateLimit_SessionLimit_WithResetsAt",
		lines: []string{
			`{"type":"error","error":{"type":"usage_limit_reached","message":"Claude Code usage limit reached"},"resetsAt":1783192800}`,
		},
		wantClass:   outcome.Transient,
		wantReason:  outcome.RateLimit,
		wantResetAt: func() *time.Time { t := time.Unix(1783192800, 0).UTC(); return &t }(),
	},
	{
		// Claude Code session-limit: plain-text fallback message.
		name: "RateLimit_SessionLimit_PlainText",
		lines: []string{
			`Claude Code usage limit reached`,
		},
		wantClass:   outcome.Transient,
		wantReason:  outcome.RateLimit,
		wantResetAt: nil,
	},
	{
		// Rate-limit markers nested inside an assistant message's own content
		// (the agent's prose about rate-limit code, or a diff/test fixture it
		// wrote) must not poison classification — no terminating API error
		// event means Terminal, not RateLimit (issue #579).
		name: "Terminal_SelfPoisoning_MarkersOnlyInAssistantContent",
		lines: []string{
			`{"type":"assistant","message":{"content":[{"type":"text","text":"Adding a rate_limit_error test case with 429 Too Many Requests and resetsAt:1783963200 fixture data"}]}}`,
		},
		wantClass:   outcome.Terminal,
		wantReason:  outcome.TaskFailed,
		wantResetAt: nil,
	},
	{
		// Rate-limit markers nested inside a tool_result turn (the agent
		// grepping/catting its own rate-limit source or a fixture log) must
		// not poison classification either (issue #579).
		name: "Terminal_SelfPoisoning_MarkersOnlyInToolResultContent",
		lines: []string{
			`{"type":"user","message":{"content":[{"type":"tool_result","content":"logs/issue-565.log:1: rate_limit_error 429 Too Many Requests \"resetsAt\":1783963200"}]}}`,
		},
		wantClass:   outcome.Terminal,
		wantReason:  outcome.TaskFailed,
		wantResetAt: nil,
	},
	{
		// A genuine terminating rate-limit event followed by continued,
		// substantive agent activity means the run recovered — the earlier
		// event is not the reason the box eventually exited, so it must not
		// be attributed as the cause (issue #579).
		name: "Terminal_RecoveredMidRun429NotAttributed",
		lines: []string{
			`{"type":"error","error":{"type":"rate_limit_error","message":"Rate limit exceeded"},"resetsAt":1783192800}`,
			`{"type":"assistant","message":{"content":[{"type":"text","text":"Hit a rate limit, retrying..."}]}}`,
			`{"type":"assistant","message":{"content":[{"type":"text","text":"Continuing the task after the retry succeeded."}]}}`,
			`Agent completed with no valid outcome.`,
		},
		wantClass:   outcome.Terminal,
		wantReason:  outcome.TaskFailed,
		wantResetAt: nil,
	},
	{
		// Redacted reconstruction of the box log that stranded
		// agent-issue-565 (issue #579): the box edits rate-limit-handling
		// code, its own diff/test-fixture content quotes rate_limit_error /
		// 429 / a fixture "resetsAt" timestamp, and it then OOM-dies with no
		// SPINDRIFT_OUTCOME line and no genuine terminating API error event.
		// Must classify as Terminal/TaskFailed — no multi-hour hold on the
		// fixture timestamp.
		name: "Terminal_Issue565Reconstruction_NoHoldOnFixtureResetsAt",
		lines: []string{
			`{"type":"assistant","message":{"content":[{"type":"text","text":"Working on issue #565: hold-and-retry rate-limited boxes."}]}}`,
			`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Edit","input":{"file_path":"classify_test.go","new_string":"lines: []string{\"429 Too Many Requests\", \"rate_limit_error\"}, wantResetAt: \"resetsAt\":1783963200"}}]}}`,
			`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"File edited successfully."}]}}`,
			`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"go test ./..."}}]}}`,
			`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":"ok  	spindrift.dev/launcher/internal/outcome	0.05s"}]}}`,
		},
		wantClass:   outcome.Terminal,
		wantReason:  outcome.TaskFailed,
		wantResetAt: nil,
	},
	{
		// Issue numbers, byte counts, or port numbers containing "429" or "529"
		// must not be mistaken for API rate-limit / overload errors.
		name: "Terminal_NoBareDigitFalsePositive",
		lines: []string{
			`Closes #1429`,
			`wrote 4290 bytes`,
			`listening on port 5290`,
			`gcc: error at line 529 in module.c`,
		},
		wantClass:   outcome.Terminal,
		wantReason:  outcome.TaskFailed,
		wantResetAt: nil,
	},
}

// TestClassify_OversizedLine_ChunkMatchesMarker locks in the chunk-matching
// oversized-line policy: a marker planted past the internal 4 MiB scan
// buffer, inside one giant line, must still be found.
func TestClassify_OversizedLine_ChunkMatchesMarker(t *testing.T) {
	const fiveMiB = 5 * 1024 * 1024
	path := filepath.Join(t.TempDir(), "big.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	big := make([]byte, fiveMiB)
	for i := range big {
		big[i] = 'x'
	}
	if _, err := f.Write(big); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte(`"rate_limit_error"`), fiveMiB-100); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	c, err := outcome.Classify(path)
	if err != nil {
		t.Fatalf("Classify() error: %v", err)
	}
	if c.Class != outcome.Transient {
		t.Errorf("Class: got %q, want %q", c.Class, outcome.Transient)
	}
	if c.Reason != outcome.RateLimit {
		t.Errorf("Reason: got %q, want %q", c.Reason, outcome.RateLimit)
	}
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

			c, err := outcome.Classify(logPath)
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
