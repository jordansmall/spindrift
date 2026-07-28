package claude

import (
	"encoding/json"
	"testing"
)

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

func TestCollectTaskRoles_RecordsSubagentTypeFromAgentInput(t *testing.T) {
	ev := Event{
		Type: "assistant",
		Message: &Message{
			Content: []ContentBlock{
				{Type: "tool_use", Name: "Agent", ID: "toolu_1", Input: []byte(`{"subagent_type":"reviewer"}`)},
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

func TestCollectTaskRoles_RecordsSpawnFromNestedEvent(t *testing.T) {
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
	if got := taskRole["toolu_2"]; got != "scout" {
		t.Errorf("taskRole[toolu_2] = %q, want %q", got, "scout")
	}
}

func TestCollectTaskRoles_RecordsNestedSpawnFromChildEvent(t *testing.T) {
	ev := Event{
		Type:            "assistant",
		ParentToolUseID: "toolu_A",
		Message: &Message{
			Content: []ContentBlock{
				{Type: "tool_use", Name: "Agent", ID: "toolu_B", Input: []byte(`{"subagent_type":"worker"}`)},
			},
		},
	}
	taskRole := map[string]string{}
	CollectTaskRoles(ev, taskRole)
	if got := taskRole["toolu_B"]; got != "worker" {
		t.Errorf("taskRole[toolu_B] = %q, want %q", got, "worker")
	}
}

func TestCollectTaskRoles_NestedSpawnEmptySubagentTypeDefaultsToSubagent(t *testing.T) {
	ev := Event{
		Type:            "assistant",
		ParentToolUseID: "toolu_A",
		Message: &Message{
			Content: []ContentBlock{
				{Type: "tool_use", Name: "Task", ID: "toolu_C", Input: []byte(`{}`)},
			},
		},
	}
	taskRole := map[string]string{}
	CollectTaskRoles(ev, taskRole)
	if got := taskRole["toolu_C"]; got != DefaultRole {
		t.Errorf("taskRole[toolu_C] = %q, want %q", got, DefaultRole)
	}
}

func TestTokenUsage_CacheCreationTTLSplit(t *testing.T) {
	const withSplit = `{"content":null,"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":30,"cache_creation":{"ephemeral_5m_input_tokens":20,"ephemeral_1h_input_tokens":10}}}`
	var m Message
	if err := json.Unmarshal([]byte(withSplit), &m); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if m.Usage.CacheCreation == nil {
		t.Fatal("Usage.CacheCreation = nil, want non-nil")
	}
	if got := m.Usage.CacheCreation.Ephemeral5mInputTokens; got != 20 {
		t.Errorf("Ephemeral5mInputTokens = %d, want 20", got)
	}
	if got := m.Usage.CacheCreation.Ephemeral1hInputTokens; got != 10 {
		t.Errorf("Ephemeral1hInputTokens = %d, want 10", got)
	}

	const withoutSplit = `{"content":null,"usage":{"input_tokens":10,"output_tokens":5}}`
	var m2 Message
	if err := json.Unmarshal([]byte(withoutSplit), &m2); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if m2.Usage.CacheCreation != nil {
		t.Errorf("Usage.CacheCreation = %+v, want nil", m2.Usage.CacheCreation)
	}
}
