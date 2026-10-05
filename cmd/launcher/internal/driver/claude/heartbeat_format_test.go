package claude

import (
	"fmt"
	"testing"
)

// TestToolKind_SubagentSpawnTools ensures toolKind agrees with
// isSubagentSpawnTool: both "Task" (legacy name) and "Agent" (current Box
// `claude` name) must map to the "subagent" count kind (issue #2078).
func TestToolKind_SubagentSpawnTools(t *testing.T) {
	for _, name := range []string{"Task", "Agent"} {
		if got := toolKind(name); got != "subagent" {
			t.Errorf("toolKind(%q) = %q, want %q", name, got, "subagent")
		}
	}
}

// sanitizeLine drops Unicode bidi controls and line/paragraph separators so
// agent text cannot reorder or visually break a heartbeat line (issue #4396),
// while ordinary non-ASCII text survives.
func TestSanitizeLineDropsBidiAndSeparators(t *testing.T) {
	for _, r := range []rune{
		0x202A, 0x202B, 0x202C, 0x202D, 0x202E, // embeddings/overrides
		0x2066, 0x2067, 0x2068, 0x2069, // isolates
		0x200E, 0x200F, 0x061C, // LRM/RLM/ALM
		0x2028, 0x2029, // separators
		0x200B, 0xFEFF, // zero-width/BOM
	} {
		t.Run(fmt.Sprintf("%U", r), func(t *testing.T) {
			if got := sanitizeLine("a" + string(r) + "b"); got != "ab" {
				t.Errorf("sanitizeLine(a%Ub) = %q, want %q", r, got, "ab")
			}
		})
	}
	t.Run("ordinary non-ASCII survives", func(t *testing.T) {
		const in = "café ✓"
		if got := sanitizeLine(in); got != in {
			t.Errorf("sanitizeLine(%q) = %q, want unchanged", in, got)
		}
	})
}
