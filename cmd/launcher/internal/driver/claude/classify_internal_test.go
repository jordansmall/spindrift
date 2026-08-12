package claude

import (
	"testing"
	"time"
)

// TestIsAgentContentEvent_SyntheticSentinel confirms the synthetic-terminator
// guard is keyed on the named syntheticModelSentinel const, not a coincidental
// inline literal (issue #820).
func TestIsAgentContentEvent_SyntheticSentinel(t *testing.T) {
	sentinelLine := `{"type":"assistant","message":{"model":"` + syntheticModelSentinel + `"},"error":"server_error"}`
	if got := isAgentContentEvent(sentinelLine); got {
		t.Errorf("isAgentContentEvent(sentinel) = %v, want false", got)
	}

	genuineLine := `{"type":"assistant","message":{"model":"claude-sonnet-4-6"},"error":"server_error"}`
	if got := isAgentContentEvent(genuineLine); !got {
		t.Errorf("isAgentContentEvent(genuine model) = %v, want true", got)
	}
}

// TestParseResetsAtText covers parseResetsAtText's human-readable
// "resets <clock-time>(am|pm) (UTC)" and "resets <Weekday> <clock-time>(am|pm)
// (UTC)" parsing (issue #2443). All cases share a fixed reference "now" of
// 2026-08-12 10:00:00 UTC, a Wednesday, so results are deterministic.
func TestParseResetsAtText(t *testing.T) {
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		content string
		now     time.Time // zero value means "use the shared Wednesday-10am now above"
		want    *time.Time
	}{
		{
			name:    "bare clock time later today",
			content: "You've hit your session limit · resets 6:30pm (UTC)",
			want:    timePtr(time.Date(2026, 8, 12, 18, 30, 0, 0, time.UTC)),
		},
		{
			name:    "bare clock time earlier today is stale, no reset time",
			content: "You've hit your session limit · resets 5:00am (UTC)",
			want:    nil,
		},
		{
			name:    "weekday matches now's weekday, time later today",
			content: "You've hit your weekly limit · resets Wed 6:30pm (UTC)",
			want:    timePtr(time.Date(2026, 8, 12, 18, 30, 0, 0, time.UTC)),
		},
		{
			name:    "weekday matches now's weekday, time earlier today is stale, no reset time",
			content: "You've hit your weekly limit · resets Wed 5:00am (UTC)",
			want:    nil,
		},
		{
			name:    "weekday different from now's weekday",
			content: "You've hit your weekly limit · resets Mon 12:00am (UTC)",
			want:    timePtr(time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)),
		},
		{
			name:    "bare clock time just missed by minutes is stale, no reset time",
			content: "You've hit your session limit · resets 11:10pm (UTC)",
			now:     time.Date(2026, 8, 12, 23, 15, 0, 0, time.UTC),
			want:    nil,
		},
		{
			name:    "weekday time just missed on the target weekday is stale, no reset time",
			content: "You've hit your weekly limit · resets Mon 12:00am (UTC)",
			now:     time.Date(2026, 8, 17, 0, 1, 0, 0, time.UTC),
			want:    nil,
		},
		{
			name:    "12:00am edge case parses to 00:00",
			content: "You've hit your weekly limit · resets Mon 12:00am (UTC)",
			want:    timePtr(time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)),
		},
		{
			name:    "12:00pm edge case parses to 12:00",
			content: "You've hit your Opus limit · resets 12:00pm (UTC)",
			want:    timePtr(time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)),
		},
		{
			name:    "missing am/pm does not match",
			content: "You've hit your session limit · resets 6:30 (UTC)",
			want:    nil,
		},
		{
			name:    "missing (UTC) suffix does not match",
			content: "You've hit your session limit · resets 6:30pm",
			want:    nil,
		},
		{
			name:    "garbage text does not match",
			content: "no reset info here at all",
			want:    nil,
		},
		{
			name:    "unrecognized weekday abbreviation does not match",
			content: "You've hit your weekly limit · resets Xyz 6:30pm (UTC)",
			want:    nil,
		},
		{
			name:    "empty content does not match",
			content: "",
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rowNow := now
			if !tt.now.IsZero() {
				rowNow = tt.now
			}
			got := parseResetsAtText(tt.content, rowNow)
			assertTimePtrEqual(t, got, tt.want)
		})
	}
}

// TestClassifyAt_PinnedClock_PlainTextResetsAt pins classifyAt's now
// parameter to a fixed instant and asserts an exact non-nil ResetAt epoch
// for a plain-text OAuth reset marker with no date component (issue #2443).
// Sibling to TestClassify_OAuthPlainTextResetsAt_ExactEpoch (which covers all
// three OAuth plain-text markers via the exported claude.ClassifyAt); this
// one exercises the unexported classifyAt directly for the session-limit case.
func TestClassifyAt_PinnedClock_PlainTextResetsAt(t *testing.T) {
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	logPath := WriteLog(t, `You've hit your session limit · resets 6:30pm (UTC)`)

	c, err := classifyAt(logPath, now)
	if err != nil {
		t.Fatalf("classifyAt() error: %v", err)
	}

	want := time.Date(2026, 8, 12, 18, 30, 0, 0, time.UTC)
	if c.ResetAt == nil {
		t.Fatal("ResetAt: got nil, want non-nil")
	}
	if !c.ResetAt.Equal(want) {
		t.Errorf("ResetAt: got %v, want %v", *c.ResetAt, want)
	}
}

func timePtr(t time.Time) *time.Time {
	return &t
}

func assertTimePtrEqual(t *testing.T, got, want *time.Time) {
	t.Helper()
	if (got == nil) != (want == nil) {
		t.Fatalf("parseResetsAtText() = %v, want %v", got, want)
	}
	if got == nil {
		return
	}
	if !got.Equal(*want) {
		t.Errorf("parseResetsAtText() = %v, want %v", got, want)
	}
}
