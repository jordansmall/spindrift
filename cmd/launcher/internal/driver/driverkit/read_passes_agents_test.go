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
