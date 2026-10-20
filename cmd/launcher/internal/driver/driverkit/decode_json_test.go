package driverkit

import (
	"encoding/json"
	"testing"
)

func TestCompactJSON(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{"empty", ``, ""},
		{"null", `null`, ""},
		{"whitespace null", ` null `, "null"},
		{"string", `"x"`, `"x"`},
		{"object", `{ "a" : 1 }`, `{"a":1}`},
		{"array", `[1, 2]`, `[1,2]`},
		{"invalid", `{bad`, ""},
		{"number", `42`, "42"},
		{"bool", `true`, "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := CompactJSON(json.RawMessage(tc.raw)); got != tc.want {
				t.Errorf("CompactJSON(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestErrorText(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{"empty", ``, ""},
		{"null", `null`, ""},
		{"whitespace null", ` null `, ""},
		{"string", `"x"`, "x"},
		{"object", `{ "a" : 1 }`, `{"a":1}`},
		{"array", `[1, 2]`, `[1,2]`},
		{"invalid", `{bad`, ""},
		{"number", `42`, "42"},
		{"bool", `true`, "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ErrorText(json.RawMessage(tc.raw)); got != tc.want {
				t.Errorf("ErrorText(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestToolTarget(t *testing.T) {
	fields := map[string]string{"bash": "command"}
	for _, tc := range []struct{ name, tool, input, want string }{
		{"hit", "bash", `{"command":"ls"}`, "ls"},
		{"unknown tool", "nope", `{"command":"ls"}`, ""},
		{"missing field", "bash", `{"other":"ls"}`, ""},
		{"non-string field", "bash", `{"command":1}`, ""},
		{"invalid input", "bash", `{bad`, ""},
		{"empty input", "bash", ``, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ToolTarget(fields, tc.tool, json.RawMessage(tc.input)); got != tc.want {
				t.Errorf("ToolTarget(%q, %q) = %q, want %q", tc.tool, tc.input, got, tc.want)
			}
		})
	}
}
