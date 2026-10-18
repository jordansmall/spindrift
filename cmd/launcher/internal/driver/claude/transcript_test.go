package claude_test

import (
	"fmt"
	"testing"

	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/driver/driverkit"
)

func spawnLine(parent, tool, id, input string) string {
	return fmt.Sprintf(`{"type":"assistant","parent_tool_use_id":%q,"message":{"id":"s-%s","content":[{"type":"tool_use","id":%q,"name":%q,"input":%s}]}}`, parent, id, id, tool, input)
}

func textLine(parent, text string) string {
	return fmt.Sprintf(`{"type":"assistant","parent_tool_use_id":%q,"message":{"id":"t-%s","content":[{"type":"text","text":%q}]}}`, parent, text, text)
}

// TestTranscriptRoleAttribution drives the role rules through the Driver's
// transcript: a spawn block records its role under its tool-use ID, and a
// later message attributes to the role recorded for its parent.
func TestTranscriptRoleAttribution(t *testing.T) {
	cases := []struct {
		name         string
		topLevelRole string
		lines        []string
		want         string
	}{
		{"no parent is the implementor", "", []string{textLine("", "hi")}, "[implementor] hi"},
		{"unknown parent defaults to subagent", "", []string{textLine("toolu_unknown", "hi")}, "[subagent] hi"},
		{"known parent returns the recorded role", "", []string{
			spawnLine("", "Task", "toolu_1", `{"subagent_type":"scout"}`),
			textLine("toolu_1", "hi"),
		}, "[implementor] Task(scout)\n[scout] hi"},
		{"top-level role applies when the parent is empty", "reviewer", []string{textLine("", "hi")}, "[reviewer] hi"},
		{"empty top-level role falls back to the implementor", "", []string{textLine("", "hi")}, "[implementor] hi"},
		{"top-level role does not affect a real parent", "reviewer", []string{
			spawnLine("", "Task", "toolu_1", `{"subagent_type":"scout"}`),
			textLine("toolu_1", "hi"),
		}, "[reviewer] Task(scout)\n[scout] hi"},
		{"Agent spawn records its subagent_type", "", []string{
			spawnLine("", "Agent", "toolu_1", `{"subagent_type":"reviewer"}`),
			textLine("toolu_1", "hi"),
		}, "[implementor] Agent(reviewer)\n[reviewer] hi"},
		{"empty subagent_type defaults to subagent", "", []string{
			spawnLine("", "Task", "toolu_2", `{}`),
			textLine("toolu_2", "hi"),
		}, "[implementor] Task()\n[subagent] hi"},
		{"spawn from a nested event is recorded", "", []string{
			spawnLine("", "Task", "toolu_1", `{"subagent_type":"scout"}`),
			spawnLine("toolu_1", "Task", "toolu_2", `{"subagent_type":"scout2"}`),
			textLine("toolu_2", "hi"),
		}, "[implementor] Task(scout)\n[scout] Task(scout2)\n[scout2] hi"},
		{"spawn from a nested event via the Agent tool name is recorded", "", []string{
			spawnLine("", "Task", "toolu_1", `{"subagent_type":"scout"}`),
			spawnLine("toolu_1", "Agent", "toolu_2", `{"subagent_type":"worker"}`),
			textLine("toolu_2", "hi"),
		}, "[implementor] Task(scout)\n[scout] Agent(worker)\n[worker] hi"},
		{"nested spawn with an empty subagent_type defaults to subagent", "", []string{
			spawnLine("toolu_A", "Task", "toolu_C", `{}`),
			textLine("toolu_C", "hi"),
		}, "[subagent] Task()\n[subagent] hi"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := claude.WriteLog(t, tc.lines...)
			got, err := newDriver().RenderTranscript(path, driverkit.RenderOptions{TopLevelRole: tc.topLevelRole})
			if err != nil {
				t.Fatalf("RenderTranscript: %v", err)
			}
			if got != tc.want+"\n" {
				t.Errorf("RenderTranscript = %q, want %q", got, tc.want)
			}
		})
	}
}
