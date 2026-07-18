package claude

import "testing"

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
