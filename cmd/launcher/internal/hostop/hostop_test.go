package hostop_test

import (
	"testing"
	"time"

	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/landdelta"
	"spindrift.dev/launcher/internal/usage"
)

// The goldens were captured from the pre-move claude encoder and pin the wire
// format across the move (issue #5179).
func TestEncodeSpindriftOpWireFormat(t *testing.T) {
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name string
		op   hostop.SpindriftOp
		want string
	}{
		{"dispatch_start", hostop.SpindriftOp{Op: hostop.OpDispatchStart, Start: &hostop.DispatchStart{RecordID: "r1", HostToken: "tok", Kind: "work", DispatchKey: "issue-5179", ClaimTime: ts, Started: ts.Add(time.Minute), Revision: "abc", RoleModels: map[string]string{"implement": "m1"}, Driver: "claude", DriverVersion: "1.2", Knobs: map[string]string{"A": "b"}}},
			"{\"type\":\"spindrift_op\",\"spindrift_op\":{\"op\":\"dispatch_start\",\"dispatch_start\":{\"record_id\":\"r1\",\"host_token\":\"tok\",\"kind\":\"work\",\"dispatch_key\":\"issue-5179\",\"claim_time\":\"2026-01-02T03:04:05Z\",\"started\":\"2026-01-02T03:05:05Z\",\"revision\":\"abc\",\"role_models\":{\"implement\":\"m1\"},\"driver\":\"claude\",\"driver_version\":\"1.2\",\"knobs\":{\"A\":\"b\"}}}}\n"},
		{"dispatch_settled", hostop.SpindriftOp{Op: hostop.OpDispatchSettled, Settled: &hostop.DispatchSettled{RecordID: "r1", State: "complete", Reason: "merged", Note: "n", PRURL: "http://x/pr/1", MergeCommit: "dead", HostToken: "tok"}},
			"{\"type\":\"spindrift_op\",\"spindrift_op\":{\"op\":\"dispatch_settled\",\"dispatch_settled\":{\"record_id\":\"r1\",\"state\":\"complete\",\"reason\":\"merged\",\"note\":\"n\",\"pr_url\":\"http://x/pr/1\",\"merge_commit\":\"dead\",\"host_token\":\"tok\"}}}\n"},
		{"pass_usage", hostop.SpindriftOp{Op: "pass_usage", Pass: 2, Usage: &hostop.PassUsage{APICalls: 3, UncachedInputTokens: 4, OutputTokens: 5, CacheReadInputTokens: 6, CacheCreationInputTokens: 7, Agents: []usage.AgentUsage{{}}, OutputIsMainLoopOnly: true}},
			"{\"type\":\"spindrift_op\",\"spindrift_op\":{\"op\":\"pass_usage\",\"pass\":2,\"usage\":{\"api_calls\":3,\"uncached_input_tokens\":4,\"output_tokens\":5,\"cache_read_input_tokens\":6,\"cache_creation_input_tokens\":7,\"agents\":[{\"Agent\":\"\",\"APICalls\":0,\"UncachedInputTokens\":0,\"OutputTokens\":0,\"CacheReadInputTokens\":0,\"CacheCreationInputTokens\":0}],\"output_is_main_loop_only\":true}}}\n"},
		{"prompt_hashes", hostop.SpindriftOp{Op: "prompt_hashes", PromptHashes: &hostop.PromptHashes{RecordID: "r1", Roles: map[string]string{"implement": "h"}}},
			"{\"type\":\"spindrift_op\",\"spindrift_op\":{\"op\":\"prompt_hashes\",\"prompt_hashes\":{\"record_id\":\"r1\",\"roles\":{\"implement\":\"h\"}}}}\n"},
		{"land_delta", hostop.SpindriftOp{Op: "land_delta", Delta: &landdelta.Delta{}},
			"{\"type\":\"spindrift_op\",\"spindrift_op\":{\"op\":\"land_delta\",\"delta\":{\"known\":false}}}\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := hostop.EncodeSpindriftOp(tc.op); got != tc.want {
				t.Errorf("EncodeSpindriftOp() =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}
