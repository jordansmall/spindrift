package driver

import (
	"encoding/json"
	"strings"
	"testing"
)

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

// hostLineViaJSON is HostLine before it moved onto Scan (#7107): the reference
// the scanner-based implementation must agree with on every line.
func hostLineViaJSON(line string) (passRole string, ok bool) {
	var l struct {
		Type        string `json:"type"`
		SpindriftOp *struct {
			Op   string `json:"op"`
			Role string `json:"role"`
		} `json:"spindrift_op"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(line)), &l) != nil || l.Type != "spindrift_op" || l.SpindriftOp == nil {
		return "", false
	}
	if l.SpindriftOp.Op != "pass_start" {
		return "", true
	}
	return passRoles[l.SpindriftOp.Role], true
}

var hostLineSeeds = []string{
	`{"type":"spindrift_op","spindrift_op":{"op":"pass_start","role":"review"}}`,
	`{"type":"spindrift_op","type":"assistant","spindrift_op":{"op":"pass_start","role":"fix"}}`,
	`{"type":"assistant","type":"spindrift_op","spindrift_op":{"op":"pass_start","role":"fix"}}`,
	`{"type":"spindrift_op","type":null,"spindrift_op":{"op":"pass_start","role":"land"}}`,
	`{"TYPE":"spindrift_op","Spindrift_Op":{"OP":"pass_start","Role":"implement"}}`,
	`{"type":"spindrift_op","spindrift_op":{"op":"pass_start","role":"review"},"spindrift_op":null}`,
	`{"type":"spindrift_op","spindrift_op":null,"spindrift_op":{"op":"pass_start"}}`,
	`{"type":"spindrift_op","spindrift_op":{"op":"pass_start","role":"review"},"spindrift_op":{"op":"verdict"}}`,
	`{"type":"spindrift_op","spindrift_op":{"op":"pass_start","role":"review"},"spindrift_op":{"role":null}}`,
	`{"type":"spindrift_op","spindrift_op":{"op":"pass_start","role":"review","role":7}}`,
	`{"type":"spindrift_op","spindrift_op":{"op":1}}`,
	`{"type":"spindrift_op","spindrift_op":[]}`,
	`{"type":"spindrift_op","spindrift_op":"x"}`,
	`{"type":7,"spindrift_op":{"op":"x"}}`,
	`{"type":["spindrift_op"],"spindrift_op":{"op":"x"}}`,
	`{"type":"spindrift_op","spindrift_op":{"op":"x"},"other":[1,{"type":9}]}`,
	`{"type":"spindrift_op","spindrift_op":{"op":"pass_start","role":"review"}}`,
	`{"type":"spindrift_op","spindrift_op":{}}`,
	`{"type":"spindrift_op","spindrift_op":{"op":"x"}}`,
	`{"ſpindrift_op":{"op":"x"},"type":"spindrift_op"}`,
	`null`, `[]`, `"spindrift_op"`, `7`, `{}`, ``, ` `, `{"type":"spindrift_op","spindrift_op":{"op":"x"}`, "\v{\"type\":\"spindrift_op\",\"spindrift_op\":{\"op\":\"x\"}}\f",
}

func TestHostLineMatchesEncodingJSON(t *testing.T) {
	for _, line := range hostLineSeeds {
		wantRole, wantOK := hostLineViaJSON(line)
		if role, ok := HostLine(line); role != wantRole || ok != wantOK {
			t.Errorf("HostLine(%q) = (%q, %v), encoding/json gives (%q, %v)", line, role, ok, wantRole, wantOK)
		}
	}
}

func FuzzHostLine(f *testing.F) {
	for _, s := range hostLineSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, line string) {
		wantRole, wantOK := hostLineViaJSON(line)
		if role, ok := HostLine(line); role != wantRole || ok != wantOK {
			t.Fatalf("HostLine(%q) = (%q, %v), encoding/json gives (%q, %v)", line, role, ok, wantRole, wantOK)
		}
	})
}

func TestHostLineOfTakesAScannedRoot(t *testing.T) {
	v, ok := Scan(`{"type":"spindrift_op","spindrift_op":{"op":"pass_start","role":"review"}}`)
	if !ok {
		t.Fatal("Scan not ok")
	}
	if role, ok := HostLineOf(v); role != ReviewerRole || !ok {
		t.Errorf("HostLineOf = (%q, %v)", role, ok)
	}
	if role, ok := HostLineOf(Value{}); role != "" || ok {
		t.Errorf("HostLineOf(zero Value) = (%q, %v)", role, ok)
	}
}
