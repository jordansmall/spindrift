package claude_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/driver/driverkit"
)

var classifyTests = []struct {
	name        string
	lines       []string
	wantClass   driverkit.Class
	wantReason  driverkit.Reason
	wantResetAt *time.Time
}{
	{
		name: "RateLimit_WithResetsAt",
		lines: []string{
			`{"type":"error","error":{"type":"rate_limit_error","message":"Rate limit exceeded"},"resetsAt":1783192800}`,
			`Error: 429 Too Many Requests`,
		},
		wantClass:   driverkit.Transient,
		wantReason:  driverkit.RateLimit,
		wantResetAt: func() *time.Time { t := time.Unix(1783192800, 0).UTC(); return &t }(),
	},
	{
		name: "RateLimit_WithResetsAt_OnSeparateLine",
		lines: []string{
			`Error: 429 Too Many Requests`,
			`{"resetsAt":1783192800}`,
		},
		wantClass:   driverkit.Transient,
		wantReason:  driverkit.RateLimit,
		wantResetAt: func() *time.Time { t := time.Unix(1783192800, 0).UTC(); return &t }(),
	},
	{
		name: "RateLimit_WithoutResetsAt",
		lines: []string{
			`Error: 429 Too Many Requests`,
			`rate limit exceeded, please retry later`,
		},
		wantClass:   driverkit.Transient,
		wantReason:  driverkit.RateLimit,
		wantResetAt: nil,
	},
	{
		name: "Overloaded_529",
		lines: []string{
			`Error: 529 Overloaded`,
			`The server is temporarily overloaded.`,
		},
		wantClass:   driverkit.Transient,
		wantReason:  driverkit.Overloaded,
		wantResetAt: nil,
	},
	{
		name: "Overloaded_error_type",
		lines: []string{
			`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
		},
		wantClass:   driverkit.Transient,
		wantReason:  driverkit.Overloaded,
		wantResetAt: nil,
	},
	{
		// Exercises the lowest-priority bare "Overloaded" pattern, which the
		// overloaded_error and "529 Overloaded" cases never reach.
		name: "Overloaded_PlainText",
		lines: []string{
			`Overloaded`,
		},
		wantClass:   driverkit.Transient,
		wantReason:  driverkit.Overloaded,
		wantResetAt: nil,
	},
	{
		// Anthropic mid-stream 5xx server error as a structured JSON error
		// type, mapped onto the existing Overloaded reason (issue #815).
		name: "Overloaded_ServerError_ErrorType",
		lines: []string{
			`{"type":"error","error":{"type":"server_error","message":"Server error"}}`,
		},
		wantClass:   driverkit.Transient,
		wantReason:  driverkit.Overloaded,
		wantResetAt: nil,
	},
	{
		// The claude CLI's synthetic terminator for a mid-stream 5xx is a
		// CLI-injected event, not agent-authored content, so
		// isAgentContentEvent must not swallow it (issue #815).
		name: "Overloaded_SyntheticServerErrorTerminator",
		lines: []string{
			`{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"API Error: Server error mid-response. The response above may be incomplete."}],"stop_reason":"stop_sequence"},"error":"server_error"}`,
			`{"type":"result","is_error":true,"result":"API Error: Server error mid-response","stop_reason":"stop_sequence","terminal_reason":"completed"}`,
		},
		wantClass:   driverkit.Transient,
		wantReason:  driverkit.Overloaded,
		wantResetAt: nil,
	},
	{
		// A genuine assistant turn that quotes "server_error" in its own prose
		// must not be mistaken for the CLI's synthetic terminator. The #579
		// self-poison guard still applies (issue #815).
		name: "Terminal_SelfPoisoning_ServerErrorMarkerInGenuineAssistantContent",
		lines: []string{
			`{"type":"assistant","message":{"model":"claude-sonnet-4-6","content":[{"type":"text","text":"Adding a server_error transient pattern test case"}]}}`,
		},
		wantClass:   driverkit.Terminal,
		wantReason:  driverkit.TaskFailed,
		wantResetAt: nil,
	},
	{
		// The CLI's ordinary, non-error type:"result" line echoes the preceding
		// assistant turn's text into "result". That echo must not be scanned
		// as a fresh signal (issue #818).
		name: "Terminal_SelfPoisoning_ServerErrorMarkerEchoedInResultLine",
		lines: []string{
			`{"type":"assistant","message":{"model":"claude-sonnet-4-6","content":[{"type":"text","text":"Fixing the server_error guard now."}]}}`,
			`{"type":"result","is_error":false,"result":"Fixing the server_error guard now.","stop_reason":"end_turn"}`,
		},
		wantClass:   driverkit.Terminal,
		wantReason:  driverkit.TaskFailed,
		wantResetAt: nil,
	},
	{
		// A type:"system" heartbeat line (see heartbeat_test.go) can land
		// between the genuine assistant turn and the echoing result line. It
		// must not consume the pending echo, so the guard has to see past it
		// to the real result line (issue #1197).
		name: "Terminal_SelfPoisoning_ServerErrorMarkerEchoedAfterInterveningSystemLine",
		lines: []string{
			`{"type":"assistant","message":{"model":"claude-sonnet-4-6","content":[{"type":"text","text":"Fixing the server_error guard now."}]}}`,
			`{"type":"system","session_id":"s1"}`,
			`{"type":"result","is_error":false,"result":"Fixing the server_error guard now.","stop_reason":"end_turn"}`,
		},
		wantClass:   driverkit.Terminal,
		wantReason:  driverkit.TaskFailed,
		wantResetAt: nil,
	},
	{
		// Only the type:"result" line itself consumes the pending echo, no
		// matter how many non-content lines come first (issue #1197).
		name: "Terminal_SelfPoisoning_ServerErrorMarkerEchoedAfterMultipleInterveningLines",
		lines: []string{
			`{"type":"assistant","message":{"model":"claude-sonnet-4-6","content":[{"type":"text","text":"Fixing the server_error guard now."}]}}`,
			`{"type":"system","session_id":"s1"}`,
			`{"type":"system","session_id":"s1"}`,
			`{"type":"result","is_error":false,"result":"Fixing the server_error guard now.","stop_reason":"end_turn"}`,
		},
		wantClass:   driverkit.Terminal,
		wantReason:  driverkit.TaskFailed,
		wantResetAt: nil,
	},
	{
		// The #579 guard resets sr when the genuine turn is scanned, but that
		// reset is immaterial: the terminator carries its own top-level
		// "error":"server_error", which matchTransient re-matches on the next
		// line. Pins that against a future isAgentContentEvent or scanLog
		// change that widens the reset window (issue #815).
		name: "Transient_GenuineAssistantContent_ThenSyntheticServerErrorTerminator",
		lines: []string{
			`{"type":"assistant","message":{"model":"claude-sonnet-4-6","content":[{"type":"text","text":"Investigating the server_error transient pattern before writing the fix"}]}}`,
			`{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"API Error: Server error mid-response. The response above may be incomplete."}],"stop_reason":"stop_sequence"},"error":"server_error"}`,
			`{"type":"result","is_error":true,"result":"API Error: Server error mid-response","stop_reason":"stop_sequence","terminal_reason":"completed"}`,
		},
		wantClass:   driverkit.Transient,
		wantReason:  driverkit.Overloaded,
		wantResetAt: nil,
	},
	{
		// The echo-suppression guard (issue #818) applies only to
		// is_error:false completions, since only those echo the assistant
		// turn's text, so a genuine is_error:true result that coincidentally
		// matches the preceding marker is still a fresh signal (issue #1196).
		name: "Transient_GenuineIsErrorResultCoincidentallyMatchesPrecedingMarker",
		lines: []string{
			`{"type":"assistant","message":{"model":"claude-sonnet-4-6","content":[{"type":"text","text":"Investigating the rate_limit_error handling code now."}]}}`,
			`{"type":"result","is_error":true,"result":"API Error: rate_limit_error occurred","stop_reason":"stop_sequence"}`,
		},
		wantClass:   driverkit.Transient,
		wantReason:  driverkit.RateLimit,
		wantResetAt: nil,
	},
	{
		// Unlike the same-marker case above, this outcome is not invariant to
		// the #579 guard: if isAgentContentEvent stopped exempting the genuine
		// turn, its "rate_limit_error" would set sr.found and the terminator's
		// "server_error" would never be scanned. Disabling that branch makes
		// this test fail (issue #1199).
		name: "Transient_GenuineAssistantContent_RateLimitMarker_ThenSyntheticServerErrorTerminator",
		lines: []string{
			`{"type":"assistant","message":{"model":"claude-sonnet-4-6","content":[{"type":"text","text":"Investigating the rate_limit_error transient pattern before writing the fix"}]}}`,
			`{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"API Error: Server error mid-response. The response above may be incomplete."}],"stop_reason":"stop_sequence"},"error":"server_error"}`,
			`{"type":"result","is_error":true,"result":"API Error: Server error mid-response","stop_reason":"stop_sequence","terminal_reason":"completed"}`,
		},
		wantClass:   driverkit.Transient,
		wantReason:  driverkit.Overloaded,
		wantResetAt: nil,
	},
	{
		name: "Network_ConnectionRefused",
		lines: []string{
			`dial tcp: connection refused`,
			`retrying...`,
		},
		wantClass:   driverkit.Transient,
		wantReason:  driverkit.Network,
		wantResetAt: nil,
	},
	{
		name: "Network_ConnectionReset",
		lines: []string{
			`read tcp 127.0.0.1:42000->127.0.0.1:443: connection reset by peer`,
		},
		wantClass:   driverkit.Transient,
		wantReason:  driverkit.Network,
		wantResetAt: nil,
	},
	{
		name: "Network_DialTcp",
		lines: []string{
			`dial tcp 1.2.3.4:443: i/o timeout`,
		},
		wantClass:   driverkit.Transient,
		wantReason:  driverkit.Network,
		wantResetAt: nil,
	},
	{
		name: "Network_RequestCanceled",
		lines: []string{
			`Get "https://api.anthropic.com/v1/messages": net/http: request canceled`,
		},
		wantClass:   driverkit.Transient,
		wantReason:  driverkit.Network,
		wantResetAt: nil,
	},
	{
		name: "Network_ContextDeadlineExceeded",
		lines: []string{
			`Post "https://api.anthropic.com/v1/messages": context deadline exceeded`,
		},
		wantClass:   driverkit.Transient,
		wantReason:  driverkit.Network,
		wantResetAt: nil,
	},
	{
		name: "Network_NoSuchHost",
		lines: []string{
			`lookup api.anthropic.com on 8.8.8.8:53: no such host`,
		},
		wantClass:   driverkit.Transient,
		wantReason:  driverkit.Network,
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
		wantClass:   driverkit.Transient,
		wantReason:  driverkit.Network,
		wantResetAt: nil,
	},
	{
		name: "Terminal_GenuineTaskFailure",
		lines: []string{
			`Agent completed with no valid outcome.`,
			`SPINDRIFT_OUTCOME issue=1 landing= status=blocked note=failed to open PR`,
		},
		wantClass:   driverkit.Terminal,
		wantReason:  driverkit.TaskFailed,
		wantResetAt: nil,
	},
	{
		name:        "Terminal_NoLog",
		lines:       nil, // The runner points this case at a nonexistent file.
		wantClass:   driverkit.Terminal,
		wantReason:  driverkit.TaskFailed,
		wantResetAt: nil,
	},
	{
		name:        "Terminal_EmptyLog",
		lines:       []string{},
		wantClass:   driverkit.Terminal,
		wantReason:  driverkit.TaskFailed,
		wantResetAt: nil,
	},
	{
		name: "RateLimit_SessionLimit_ErrorType",
		lines: []string{
			`{"type":"error","error":{"type":"usage_limit_reached","message":"Claude Code usage limit reached"}}`,
		},
		wantClass:   driverkit.Transient,
		wantReason:  driverkit.RateLimit,
		wantResetAt: nil,
	},
	{
		name: "RateLimit_SessionLimit_WithResetsAt",
		lines: []string{
			`{"type":"error","error":{"type":"usage_limit_reached","message":"Claude Code usage limit reached"},"resetsAt":1783192800}`,
		},
		wantClass:   driverkit.Transient,
		wantReason:  driverkit.RateLimit,
		wantResetAt: func() *time.Time { t := time.Unix(1783192800, 0).UTC(); return &t }(),
	},
	{
		name: "RateLimit_SessionLimit_PlainText",
		lines: []string{
			`Claude Code usage limit reached`,
		},
		wantClass:   driverkit.Transient,
		wantReason:  driverkit.RateLimit,
		wantResetAt: nil,
	},
	// The three OAuth plain-text "resets ... (UTC)" cases live in
	// TestClassify_OAuthPlainTextResetsAt_ExactEpoch instead of this table:
	// this runner uses Classify and the real wall clock, so a fixed epoch
	// would flake near a day or week boundary. That test uses ClassifyAt with
	// a fixed now.
	{
		// Same synthetic-terminator shape as the case below but with no
		// top-level "error" field, so isAgentContentEvent treats the line as
		// ordinary agent content and clears the candidate. #1539's fix depends
		// on the CLI setting "error" on this event.
		name: "Terminal_OAuthSessionLimit_NoErrorField_Swallowed",
		lines: []string{
			`{"type":"assistant","message":{"id":"a2645b97-8af6-46ec-aa20-7cde65f631ea","model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"You've hit your session limit · resets 6:30pm (UTC)"}]},"session_id":"e89ee32d-c257-468d-c90b-5549c606b8bd","uuid":"1f7d9873-9ac8-4f1a-a7a5-d6ed1a3a6793"}`,
		},
		wantClass:   driverkit.Terminal,
		wantReason:  driverkit.TaskFailed,
		wantResetAt: nil,
	},
	{
		// Real captured log from issue #1539. ResetAt must propagate from the
		// rate_limit_event's "resetsAt" field.
		name: "RateLimit_OAuthSessionLimit_SyntheticTerminator",
		lines: []string{
			`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning","resetsAt":1784399400,"rateLimitType":"five_hour","utilization":0.99,"isUsingOverage":false,"surpassedThreshold":0.9},"uuid":"d090b94c-5ef8-4d26-a88d-ac84f5287512","session_id":"e89ee32d-c257-468d-c90b-5549c606b8bd"}`,
			`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1784399400,"rateLimitType":"five_hour","overageStatus":"rejected","overageDisabledReason":"org_level_disabled","isUsingOverage":false},"uuid":"1d41b14e-4652-4f9e-8f45-bada98ee0553","session_id":"e89ee32d-c257-468d-c90b-5549c606b8bd"}`,
			`{"type":"assistant","message":{"id":"a2645b97-8af6-46ec-aa20-7cde65f631ea","model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"You've hit your session limit · resets 6:30pm (UTC)"}]},"error":"rate_limit","session_id":"e89ee32d-c257-468d-c90b-5549c606b8bd","uuid":"1f7d9873-9ac8-4f1a-a7a5-d6ed1a3a6793"}`,
			`{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"result":"You've hit your session limit · resets 6:30pm (UTC)","stop_reason":"stop_sequence","session_id":"e89ee32d-c257-468d-c90b-5549c606b8bd","terminal_reason":"api_error","uuid":"b58acae4-f040-47fd-b18d-6a49eabb4b5b"}`,
		},
		wantClass:   driverkit.Transient,
		wantReason:  driverkit.RateLimit,
		wantResetAt: func() *time.Time { t := time.Unix(1784399400, 0).UTC(); return &t }(),
	},
	{
		// Rate-limit markers inside an assistant message's own content must not
		// poison classification: with no terminating API error event the run
		// is Terminal, not RateLimit (issue #579).
		name: "Terminal_SelfPoisoning_MarkersOnlyInAssistantContent",
		lines: []string{
			`{"type":"assistant","message":{"content":[{"type":"text","text":"Adding a rate_limit_error test case with 429 Too Many Requests and resetsAt:1783963200 fixture data"}]}}`,
		},
		wantClass:   driverkit.Terminal,
		wantReason:  driverkit.TaskFailed,
		wantResetAt: nil,
	},
	{
		// Rate-limit markers inside a tool_result turn (the agent reading its
		// own rate-limit source or a fixture log) must not poison
		// classification either (issue #579).
		name: "Terminal_SelfPoisoning_MarkersOnlyInToolResultContent",
		lines: []string{
			`{"type":"user","message":{"content":[{"type":"tool_result","content":"logs/issue-565.log:1: rate_limit_error 429 Too Many Requests \"resetsAt\":1783963200"}]}}`,
		},
		wantClass:   driverkit.Terminal,
		wantReason:  driverkit.TaskFailed,
		wantResetAt: nil,
	},
	{
		// A genuine rate-limit event followed by continued agent activity means
		// the run recovered, so that event is not why the box exited and must
		// not be attributed as the cause (issue #579).
		name: "Terminal_RecoveredMidRun429NotAttributed",
		lines: []string{
			`{"type":"error","error":{"type":"rate_limit_error","message":"Rate limit exceeded"},"resetsAt":1783192800}`,
			`{"type":"assistant","message":{"content":[{"type":"text","text":"Hit a rate limit, retrying..."}]}}`,
			`{"type":"assistant","message":{"content":[{"type":"text","text":"Continuing the task after the retry succeeded."}]}}`,
			`Agent completed with no valid outcome.`,
		},
		wantClass:   driverkit.Terminal,
		wantReason:  driverkit.TaskFailed,
		wantResetAt: nil,
	},
	{
		// Redacted reconstruction of the box log that stranded agent-issue-565:
		// the box edits rate-limit code, its own fixture content quotes
		// rate_limit_error, 429 and a fixture "resetsAt", then it dies with no
		// SPINDRIFT_OUTCOME and no genuine API error event. Must be Terminal,
		// with no multi-hour hold on the fixture timestamp (issue #579).
		name: "Terminal_Issue565Reconstruction_NoHoldOnFixtureResetsAt",
		lines: []string{
			`{"type":"assistant","message":{"content":[{"type":"text","text":"Working on issue #565: hold-and-retry rate-limited boxes."}]}}`,
			`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Edit","input":{"file_path":"classify_test.go","new_string":"lines: []string{\"429 Too Many Requests\", \"rate_limit_error\"}, wantResetAt: \"resetsAt\":1783963200"}}]}}`,
			`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"File edited successfully."}]}}`,
			`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"go test ./..."}}]}}`,
			`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":"ok  	spindrift.dev/launcher/internal/outcome	0.05s"}]}}`,
		},
		wantClass:   driverkit.Terminal,
		wantReason:  driverkit.TaskFailed,
		wantResetAt: nil,
	},
	{
		// A claude-code build predating the --agents flag rejects it with a
		// plain-text usage error. It gets its own Reason, apart from the
		// generic TaskFailed bucket, so the operator sees that the fix is to
		// bump claude-code (issue #1552).
		name: "Terminal_UnsupportedFlag_UnknownAgentsOption",
		lines: []string{
			`==> claude implementing issue #142 on agent/issue-142`,
			`error: unknown option '--agents'`,
		},
		wantClass:   driverkit.Terminal,
		wantReason:  driverkit.UnsupportedFlag,
		wantResetAt: nil,
	},
	{
		// A genuine assistant turn quoting the unknown-option marker in its own
		// prose must not be misattributed as the CLI rejecting --agents. The
		// #579 self-poison guard still applies (issue #1552).
		name: "Terminal_SelfPoisoning_UnknownAgentsOptionInGenuineAssistantContent",
		lines: []string{
			`{"type":"assistant","message":{"model":"claude-sonnet-4-6","content":[{"type":"text","text":"Adding a classifier case for error: unknown option '--agents'"}]}}`,
		},
		wantClass:   driverkit.Terminal,
		wantReason:  driverkit.TaskFailed,
		wantResetAt: nil,
	},
	{
		// The CLI's ordinary, non-error result line echoes the preceding
		// assistant turn's text, and that echo must not be scanned as a fresh
		// signal (issue #818, applied to #1552).
		name: "Terminal_SelfPoisoning_UnknownAgentsOptionEchoedInResultLine",
		lines: []string{
			`{"type":"assistant","message":{"model":"claude-sonnet-4-6","content":[{"type":"text","text":"Fixing the unknown option '--agents' guard now."}]}}`,
			`{"type":"result","is_error":false,"result":"Fixing the unknown option '--agents' guard now.","stop_reason":"end_turn"}`,
		},
		wantClass:   driverkit.Terminal,
		wantReason:  driverkit.TaskFailed,
		wantResetAt: nil,
	},
	{
		// Issue numbers, byte counts and port numbers containing "429" or
		// "529" must not be mistaken for API rate-limit or overload errors.
		name: "Terminal_NoBareDigitFalsePositive",
		lines: []string{
			`Closes #1429`,
			`wrote 4290 bytes`,
			`listening on port 5290`,
			`gcc: error at line 529 in module.c`,
		},
		wantClass:   driverkit.Terminal,
		wantReason:  driverkit.TaskFailed,
		wantResetAt: nil,
	},
}

// Locks in claude's intra-extras ordering: the specific "429 Too Many
// Requests" marker precedes the bare "Overloaded" fallback within
// transientExtras, so a line carrying both classifies as RateLimit. Both are
// claude extras, not shared base, so reordering that extras list is what would
// flip this line to Overloaded (issue #2149).
func TestClassify_RateLimitBeatsBareOverloaded_SameLine(t *testing.T) {
	logPath := claude.WriteLog(t, `Error: 429 Too Many Requests — server Overloaded`)

	c, err := claude.Classify(logPath)
	if err != nil {
		t.Fatalf("Classify() error: %v", err)
	}
	if c.Class != driverkit.Transient {
		t.Errorf("Class: got %q, want %q", c.Class, driverkit.Transient)
	}
	if c.Reason != driverkit.RateLimit {
		t.Errorf("Reason: got %q, want %q", c.Reason, driverkit.RateLimit)
	}
}

// Locks in the chunk-matching oversized-line policy: a marker planted past the
// internal 4 MiB scan buffer, inside one giant line, must still be found.
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

	c, err := claude.Classify(path)
	if err != nil {
		t.Fatalf("Classify() error: %v", err)
	}
	if c.Class != driverkit.Transient {
		t.Errorf("Class: got %q, want %q", c.Class, driverkit.Transient)
	}
	if c.Reason != driverkit.RateLimit {
		t.Errorf("Reason: got %q, want %q", c.Reason, driverkit.RateLimit)
	}
}

func TestClassify(t *testing.T) {
	for _, tc := range classifyTests {
		t.Run(tc.name, func(t *testing.T) {
			var logPath string
			if tc.name == "Terminal_NoLog" {
				logPath = filepath.Join(t.TempDir(), "nonexistent.log")
			} else {
				logPath = claude.WriteLog(t, tc.lines...)
			}

			c, err := claude.Classify(logPath)
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

// Covers the three OAuth plain-text markers whose "resets ... (UTC)" suffix
// carries a clock time but no date, so extractResetsAt falls back to
// parseResetsAtText and rolls forward from now. ClassifyAt with a fixed now
// (2026-08-12 10:00:00 UTC, a Wednesday, the same reference
// TestParseResetsAtText uses) makes each ResetAt assertable as an exact epoch.
func TestClassify_OAuthPlainTextResetsAt_ExactEpoch(t *testing.T) {
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name        string
		line        string
		wantResetAt time.Time
	}{
		{
			// Plain-text notice carried in the CLI's synthetic-terminator
			// assistant event, worded differently from the API-key
			// usage_limit_reached form (issue #1539).
			name:        "RateLimit_OAuthSessionLimit_PlainText",
			line:        `You've hit your session limit · resets 6:30pm (UTC)`,
			wantResetAt: time.Date(2026, 8, 12, 18, 30, 0, 0, time.UTC),
		},
		{
			// Aug 12 2026 is a Wednesday, so the next Monday is Aug 17 2026.
			name:        "RateLimit_OAuthWeeklyLimit_PlainText",
			line:        `You've hit your weekly limit · resets Mon 12:00am (UTC)`,
			wantResetAt: time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC),
		},
		{
			name:        "RateLimit_OAuthOpusLimit_PlainText",
			line:        `You've hit your Opus limit · resets 6:30pm (UTC)`,
			wantResetAt: time.Date(2026, 8, 12, 18, 30, 0, 0, time.UTC),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logPath := claude.WriteLog(t, tc.line)

			c, err := claude.ClassifyAt(logPath, now)
			if err != nil {
				t.Fatalf("ClassifyAt() error: %v", err)
			}

			if c.Class != driverkit.Transient {
				t.Errorf("Class: got %q, want %q", c.Class, driverkit.Transient)
			}
			if c.Reason != driverkit.RateLimit {
				t.Errorf("Reason: got %q, want %q", c.Reason, driverkit.RateLimit)
			}

			if c.ResetAt == nil {
				t.Fatal("ResetAt: got nil, want non-nil")
			}
			if !c.ResetAt.Equal(tc.wantResetAt) {
				t.Errorf("ResetAt: got %v, want %v", *c.ResetAt, tc.wantResetAt)
			}
		})
	}
}

// Issue #2443's captured log shape: an OAuth session-limit run with no paired
// rate_limit_event line, so ResetAt can only come from the plain-text "resets
// 11:10pm (UTC)" fallback. The synthetic-terminator event re-populates resetsAt
// after the preceding tool_result turn clears the candidate under the #579
// guard. Uses ClassifyAt with a fixed now, since the fallback depends on now.
func TestClassify_OAuthSessionLimit_TaskNotification(t *testing.T) {
	logPath := claude.WriteLog(t,
		`{"type":"system","subtype":"task_notification","task_id":"aa24ca2b1b465489b","tool_use_id":"toolu_01DkvcwtBco2hyARyZuhFqax","status":"failed","output_file":"/tmp/claude-1000/-work/1b098c96-0158-f1c7-e7da-777c6edcf041/tasks/aa24ca2b1b465489b.output","summary":"Agent terminated early due to an API error: You've hit your session limit · resets 11:10pm (UTC)","uuid":"7e18a671-13d9-4b65-9400-fb5193cac2bd","session_id":"1b098c96-0158-f1c7-e7da-777c6edcf041"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"Agent terminated early due to an API error: You've hit your session limit · resets 11:10pm (UTC)","is_error":true,"tool_use_id":"toolu_01DkvcwtBco2hyARyZuhFqax"}]},"parent_tool_use_id":null,"session_id":"1b098c96-0158-f1c7-e7da-777c6edcf041","uuid":"99cb046f-7c27-4fe7-a652-718254b32cd6","timestamp":"2026-08-11T19:01:33.187Z","tool_use_result":"Error: Agent terminated early due to an API error: You've hit your session limit · resets 11:10pm (UTC)"}`,
		`{"type":"assistant","message":{"id":"38ef7ec2-9b5b-4559-85fe-d33bf40f34ad","container":null,"model":"<synthetic>","role":"assistant","stop_details":null,"stop_reason":"stop_sequence","stop_sequence":"","type":"message","usage":{"input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"server_tool_use":{"web_search_requests":0,"web_fetch_requests":0},"service_tier":null,"cache_creation":{"ephemeral_1h_input_tokens":0,"ephemeral_5m_input_tokens":0},"inference_geo":null,"iterations":null,"speed":null},"content":[{"type":"text","text":"You've hit your session limit · resets 11:10pm (UTC)"}],"context_management":null},"parent_tool_use_id":null,"session_id":"1b098c96-0158-f1c7-e7da-777c6edcf041","uuid":"5f02bbf1-98b0-486c-a4a7-b6da7bbe854b","timestamp":"2026-08-11T19:01:33.866Z","error":"rate_limit","request_id":"req_011CdwUgS12SHFzdzvKPu7X1","is_api_error_message":true}`,
		`{"is_error":true,"duration_api_ms":1792383,"num_turns":67,"stop_reason":"stop_sequence","session_id":"1b098c96-0158-f1c7-e7da-777c6edcf041","total_cost_usd":6.753610549999998,"usage":{"input_tokens":132,"cache_creation_input_tokens":122537,"cache_read_input_tokens":6694041,"output_tokens":52972,"server_tool_use":{"web_search_requests":0,"web_fetch_requests":0},"service_tier":"standard","cache_creation":{"ephemeral_1h_input_tokens":122537,"ephemeral_5m_input_tokens":0},"inference_geo":"not_available","iterations":[{"input_tokens":2,"output_tokens":628,"cache_read_input_tokens":143245,"cache_creation_input_tokens":722,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":722},"speed":"standard"}]},"modelUsage":{},"permission_denials":[],"terminal_reason":"api_error","fast_mode_state":"off","fast_mode_disabled_reason":"sdk_opt_in_required","subtype":"success","api_error_status":429,"result":"You've hit your session limit · resets 11:10pm (UTC)","type":"result","duration_ms":2206065,"uuid":"a5971787-0e52-4c8d-a23d-e9213753db23"}`,
	)

	now := time.Date(2026, 8, 11, 19, 5, 0, 0, time.UTC)
	c, err := claude.ClassifyAt(logPath, now)
	if err != nil {
		t.Fatalf("ClassifyAt() error: %v", err)
	}

	if c.Class != driverkit.Transient {
		t.Errorf("Class: got %q, want %q", c.Class, driverkit.Transient)
	}
	if c.Reason != driverkit.RateLimit {
		t.Errorf("Reason: got %q, want %q", c.Reason, driverkit.RateLimit)
	}
	if c.ResetAt == nil {
		t.Fatal("ResetAt: got nil, want non-nil")
	}
	wantResetAt := time.Date(2026, 8, 11, 23, 10, 0, 0, time.UTC)
	if !c.ResetAt.Equal(wantResetAt) {
		t.Errorf("ResetAt: got %v, want %v", *c.ResetAt, wantResetAt)
	}
}
