package claudetranscript

import "testing"

func TestResolveRole_ImplementorHasNoParent(t *testing.T) {
	ev := Event{Type: "assistant", ParentToolUseID: ""}
	taskRole := map[string]string{}
	got := ResolveRole(ev, taskRole)
	if got != ImplementorRole {
		t.Errorf("ResolveRole() = %q, want %q", got, ImplementorRole)
	}
}

func TestResolveRole_UnknownParentDefaultsToSubagent(t *testing.T) {
	ev := Event{Type: "assistant", ParentToolUseID: "toolu_unknown"}
	taskRole := map[string]string{}
	got := ResolveRole(ev, taskRole)
	if got != DefaultRole {
		t.Errorf("ResolveRole() = %q, want %q", got, DefaultRole)
	}
}

func TestResolveRole_KnownParentReturnsRecordedRole(t *testing.T) {
	ev := Event{Type: "assistant", ParentToolUseID: "toolu_1"}
	taskRole := map[string]string{"toolu_1": "scout"}
	got := ResolveRole(ev, taskRole)
	if got != "scout" {
		t.Errorf("ResolveRole() = %q, want %q", got, "scout")
	}
}

func TestCollectTaskRoles_RecordsSubagentTypeFromTaskInput(t *testing.T) {
	ev := Event{
		Type: "assistant",
		Message: &Message{
			Content: []ContentBlock{
				{Type: "tool_use", Name: "Task", ID: "toolu_1", Input: []byte(`{"subagent_type":"reviewer"}`)},
			},
		},
	}
	taskRole := map[string]string{}
	CollectTaskRoles(ev, taskRole)
	if got := taskRole["toolu_1"]; got != "reviewer" {
		t.Errorf("taskRole[toolu_1] = %q, want %q", got, "reviewer")
	}
}

func TestCollectTaskRoles_EmptySubagentTypeDefaultsToSubagent(t *testing.T) {
	ev := Event{
		Type: "assistant",
		Message: &Message{
			Content: []ContentBlock{
				{Type: "tool_use", Name: "Task", ID: "toolu_2", Input: []byte(`{}`)},
			},
		},
	}
	taskRole := map[string]string{}
	CollectTaskRoles(ev, taskRole)
	if got := taskRole["toolu_2"]; got != DefaultRole {
		t.Errorf("taskRole[toolu_2] = %q, want %q", got, DefaultRole)
	}
}

func TestCollectTaskRoles_IgnoresEventsWithParent(t *testing.T) {
	ev := Event{
		Type:            "assistant",
		ParentToolUseID: "toolu_1",
		Message: &Message{
			Content: []ContentBlock{
				{Type: "tool_use", Name: "Task", ID: "toolu_2", Input: []byte(`{"subagent_type":"scout"}`)},
			},
		},
	}
	taskRole := map[string]string{}
	CollectTaskRoles(ev, taskRole)
	if _, ok := taskRole["toolu_2"]; ok {
		t.Errorf("taskRole[toolu_2] should not be recorded for a subagent event")
	}
}
