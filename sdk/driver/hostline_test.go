package driver

import "testing"

func TestHostLine(t *testing.T) {
	for _, tc := range []struct {
		name, line, role string
		ok               bool
	}{
		{"review pass", `{"type":"spindrift_op","spindrift_op":{"op":"pass_start","pass":2,"role":"review"}}`, ReviewerRole, true},
		{"delta-review pass", `{"type":"spindrift_op","spindrift_op":{"op":"pass_start","role":"delta-review"}}` + "\n", ReviewerRole, true},
		{"implement pass", ` {"type":"spindrift_op","spindrift_op":{"op":"pass_start","role":"implement"}}`, ImplementorRole, true},
		{"fix pass", `{"type":"spindrift_op","spindrift_op":{"op":"pass_start","role":"fix"}}`, ImplementorRole, true},
		{"land pass", `{"type":"spindrift_op","spindrift_op":{"op":"pass_start","role":"land"}}`, ImplementorRole, true},
		{"unmapped pass", `{"type":"spindrift_op","spindrift_op":{"op":"pass_start","role":"mystery"}}`, "", true},
		{"legacy pass without a role", `{"type":"spindrift_op","spindrift_op":{"op":"pass_start"}}`, "", true},
		{"other op ignores role", `{"type":"spindrift_op","spindrift_op":{"op":"verdict","role":"review"}}`, "", true},
		{"empty payload", `{"type":"spindrift_op"}`, "", false},
		{"other type", `{"type":"assistant","spindrift_op":{"op":"pass_start","role":"review"}}`, "", false},
		{"not json", `plain text`, "", false},
		{"empty", ``, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			role, ok := HostLine(tc.line)
			if role != tc.role || ok != tc.ok {
				t.Errorf("HostLine(%q) = (%q, %v), want (%q, %v)", tc.line, role, ok, tc.role, tc.ok)
			}
		})
	}
}
