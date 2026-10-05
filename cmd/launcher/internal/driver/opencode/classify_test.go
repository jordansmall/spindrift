package opencode_test

import (
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/opencode"
)

func TestClassify_ErrorEvent_RateLimit_IsTransient(t *testing.T) {
	logPath := opencode.WriteLog(t,
		`{"type":"error","error":"rate_limit_error: 429 Too Many Requests"}`,
	)

	got, err := opencode.Classify(logPath)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got.Class != driverkit.Transient || got.Reason != driverkit.RateLimit {
		t.Errorf("Class/Reason: got %s/%s, want %s/%s", got.Class, got.Reason, driverkit.Transient, driverkit.RateLimit)
	}
}

// Classify scans only type:"error" events for transient markers, so a
// rate-limit string in agent-authored prose is not attributed as the cause.
func TestClassify_TextEvent_QuotingRateLimit_IsNotAttributed(t *testing.T) {
	logPath := opencode.WriteLog(t,
		`{"type":"text","part":{"text":"I hit a rate_limit_error while testing."}}`,
	)

	got, err := opencode.Classify(logPath)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got.Class != driverkit.Terminal || got.Reason != driverkit.TaskFailed {
		t.Errorf("Class/Reason: got %s/%s, want %s/%s", got.Class, got.Reason, driverkit.Terminal, driverkit.TaskFailed)
	}
}

func TestClassify_NoErrorEvent_IsTerminal(t *testing.T) {
	logPath := opencode.WriteLog(t,
		`{"type":"text","part":{"text":"Investigating the issue."}}`,
	)

	got, err := opencode.Classify(logPath)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got.Class != driverkit.Terminal || got.Reason != driverkit.TaskFailed {
		t.Errorf("Class/Reason: got %s/%s, want %s/%s", got.Class, got.Reason, driverkit.Terminal, driverkit.TaskFailed)
	}
}

// Only an anchored HTTP status in the error payload reads as transient; a
// digit run in a message or event metadata (token counts, timestamps) does
// not (issue #4423).
func TestClassify_ErrorEvent_StatusMarkers(t *testing.T) {
	for _, tc := range []struct {
		name       string
		line       string
		wantClass  driverkit.Class
		wantReason driverkit.Reason
	}{
		{"529 in token count", `{"type":"error","error":"prompt is too long: 205290 tokens > 200000 maximum"}`, driverkit.Terminal, driverkit.TaskFailed},
		{"429 in timestamp, string", `{"type":"error","timestamp":1759542912345,"sessionID":"ses_1","error":"boom"}`, driverkit.Terminal, driverkit.TaskFailed},
		{"429 in timestamp, object", `{"type":"error","timestamp":1759542912345,"error":{"name":"UnknownError","data":{"message":"boom"}}}`, driverkit.Terminal, driverkit.TaskFailed},
		{"missing error field", `{"type":"error","timestamp":1759542912345}`, driverkit.Terminal, driverkit.TaskFailed},
		{"object statusCode 429", `{"type":"error","timestamp":1759542900000,"error":{"name":"APIError","data":{"message":"Too many requests","statusCode":429}}}`, driverkit.Transient, driverkit.RateLimit},
		{"object statusCode 529", `{"type":"error","timestamp":1759542900000,"error":{"name":"APIError","data":{"message":"Busy","statusCode":529}}}`, driverkit.Transient, driverkit.Overloaded},
		{"spaced object statusCode 429", `{"type":"error","error":{"name": "APIError", "data": {"statusCode": 429}}}`, driverkit.Transient, driverkit.RateLimit},
		{"status code 429", `{"type":"error","error":"Request failed with status code 429"}`, driverkit.Transient, driverkit.RateLimit},
		{"status code 529", `{"type":"error","error":"Request failed with status code 529"}`, driverkit.Transient, driverkit.Overloaded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := opencode.Classify(opencode.WriteLog(t, tc.line))
			if err != nil {
				t.Fatalf("Classify: %v", err)
			}
			if got.Class != tc.wantClass || got.Reason != tc.wantReason {
				t.Errorf("Class/Reason: got %s/%s, want %s/%s", got.Class, got.Reason, tc.wantClass, tc.wantReason)
			}
		})
	}
}

// This test pins opencode's intra-extras ordering: the status 429 RateLimit
// marker precedes overloaded_error in transientExtras, so an event carrying
// both classifies as RateLimit on first match. Both are opencode extras, not
// shared base (driverkit.BaseTransientPatterns holds only Network markers),
// so reordering that list flips this to Overloaded (issue #2149).
func TestClassify_ErrorEvent_Status429BeatsOverloaded(t *testing.T) {
	logPath := opencode.WriteLog(t,
		`{"type":"error","error":"overloaded_error: upstream returned status 429"}`,
	)

	got, err := opencode.Classify(logPath)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got.Class != driverkit.Transient || got.Reason != driverkit.RateLimit {
		t.Errorf("Class/Reason: got %s/%s, want %s/%s", got.Class, got.Reason, driverkit.Transient, driverkit.RateLimit)
	}
}

// Across several type:"error" events the last event's reason wins, not the
// first. The driverkit.ClassifyScan refactor (issue #2269) regressed this by
// latching first-match-wins with no opt-out.
func TestClassify_LastErrorEventWins(t *testing.T) {
	logPath := opencode.WriteLog(t,
		`{"type":"error","error":"rate_limit_error: 429 Too Many Requests"}`,
		`{"type":"error","error":"overloaded_error: upstream unavailable"}`,
	)

	got, err := opencode.Classify(logPath)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got.Class != driverkit.Transient || got.Reason != driverkit.Overloaded {
		t.Errorf("Class/Reason: got %s/%s, want %s/%s", got.Class, got.Reason, driverkit.Transient, driverkit.Overloaded)
	}
}

// The missing-log-file contract is shared with the claude driver's Classify.
func TestClassify_MissingFile_IsTerminal(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "does-not-exist.log")

	got, err := opencode.Classify(logPath)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got.Class != driverkit.Terminal || got.Reason != driverkit.TaskFailed {
		t.Errorf("Class/Reason: got %s/%s, want %s/%s", got.Class, got.Reason, driverkit.Terminal, driverkit.TaskFailed)
	}
}
