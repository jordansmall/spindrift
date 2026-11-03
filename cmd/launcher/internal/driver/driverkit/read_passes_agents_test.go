package driverkit

import (
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/usage"
)

func TestReadPassesAgentsPerSegment(t *testing.T) {
	call := func(id, role string, sub bool, in, out int) []Event {
		return []Event{{Kind: KindUsage, MessageID: id, Role: role, InSubagent: sub,
			Tokens: Tokens{Input: in, Output: out, CacheRead: 3, CacheWrite5m: 1, CacheWrite1h: 1}}}
	}
	t.Run("PerCallTotals", func(t *testing.T) {
		got := readPasses(t, PassOptions{Totals: PerCallTotals}, "", nil,
			call("m1", "implement", false, 1, 10),
			call("m1", "implement", false, 1, 10),
			call("s1", "worker", true, 5, 50),
			call("s2", "worker", true, 5, 50),
			[]Event{passStartEv()},
			call("m2", "fix", false, 2, 20),
			call("s3", "", true, 1, 1),
			[]Event{passStartEv()},
			[]Event{{Kind: KindText}},
		)
		if len(got) != 3 {
			t.Fatalf("segments = %d", len(got))
		}
		want0 := []usage.AgentUsage{
			{Agent: usage.MainLoopAgent, APICalls: 1, UncachedInputTokens: 1, OutputTokens: 10, CacheReadInputTokens: 3, CacheCreationInputTokens: 2},
			{Agent: "worker", APICalls: 2, UncachedInputTokens: 10, OutputTokens: 100, CacheReadInputTokens: 6, CacheCreationInputTokens: 4},
		}
		if !reflect.DeepEqual(got[0].Agents, want0) {
			t.Errorf("segment 0 Agents = %+v, want %+v", got[0].Agents, want0)
		}
		want1 := []usage.AgentUsage{
			{Agent: usage.MainLoopAgent, APICalls: 2, UncachedInputTokens: 3, OutputTokens: 21, CacheReadInputTokens: 6, CacheCreationInputTokens: 4},
		}
		if !reflect.DeepEqual(got[1].Agents, want1) {
			t.Errorf("segment 1 Agents = %+v, want %+v (a roleless subagent is the main loop)", got[1].Agents, want1)
		}
		if got[2].Agents != nil {
			t.Errorf("segment without usage events Agents = %+v, want nil", got[2].Agents)
		}
	})
	t.Run("SessionTotals patches the main row from the segment's results", func(t *testing.T) {
		result := []Event{{Kind: KindResult, Tokens: Tokens{Output: 77}}}
		got := readPasses(t, PassOptions{Totals: SessionTotals}, "", nil,
			call("m1", "implement", false, 1, 10),
			call("s1", "worker", true, 5, 50),
			result,
			[]Event{passStartEv()},
			call("s2", "worker", true, 5, 50),
			result,
			[]Event{passStartEv()},
			result,
		)
		want0 := []usage.AgentUsage{
			{Agent: usage.MainLoopAgent, APICalls: 1, UncachedInputTokens: 1, OutputTokens: 77, CacheReadInputTokens: 3, CacheCreationInputTokens: 2},
			{Agent: "worker", APICalls: 1, UncachedInputTokens: 5, CacheReadInputTokens: 3, CacheCreationInputTokens: 2},
		}
		if !reflect.DeepEqual(got[0].Agents, want0) {
			t.Errorf("segment 0 Agents = %+v, want %+v", got[0].Agents, want0)
		}
		want1 := []usage.AgentUsage{
			{Agent: usage.MainLoopAgent, OutputTokens: 77},
			{Agent: "worker", APICalls: 1, UncachedInputTokens: 5, CacheReadInputTokens: 3, CacheCreationInputTokens: 2},
		}
		if !reflect.DeepEqual(got[1].Agents, want1) {
			t.Errorf("subagent-only segment Agents = %+v, want %+v (main row synthesized)", got[1].Agents, want1)
		}
		if got[2].Agents != nil {
			t.Errorf("result-only segment Agents = %+v, want nil", got[2].Agents)
		}
	})
}

func TestReadPassesSubagentResidual(t *testing.T) {
	result := func(out int, mo map[string]int) []Event {
		return []Event{{Kind: KindResult, Tokens: Tokens{Output: out}, ModelOutput: mo}}
	}
	got := readPasses(t, PassOptions{Totals: SessionTotals}, "", nil,
		[]Event{{Kind: KindUsage, MessageID: "m1", Tokens: Tokens{Input: 1}}},
		result(100, map[string]int{"m": 300}),
		result(50, nil),
		[]Event{passStartEv()},
		[]Event{{Kind: KindUsage, MessageID: "m2", Tokens: Tokens{Input: 1}}},
		result(10, map[string]int{"m": 10}),
	)
	if got[0].Usage.OutputTokens != 350 {
		t.Errorf("segment 0 OutputTokens = %d, want 350 (150 main loop + 200 residual)", got[0].Usage.OutputTokens)
	}
	want0 := []usage.AgentUsage{
		{Agent: usage.MainLoopAgent, APICalls: 1, UncachedInputTokens: 1, OutputTokens: 150},
		{Agent: usage.SubagentsAgent, OutputTokens: 200},
	}
	if !reflect.DeepEqual(got[0].Agents, want0) {
		t.Errorf("segment 0 Agents = %+v, want %+v", got[0].Agents, want0)
	}
	if got[1].Usage.OutputTokens != 10 || len(got[1].Agents) != 1 {
		t.Errorf("segment 1 = %+v, want 10 output and no residual row", got[1])
	}
}
