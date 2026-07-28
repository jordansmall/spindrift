package claude

import "testing"

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
