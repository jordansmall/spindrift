// Package hostop is spindrift's own host-written "spindrift_op" line schema.
// It is Driver-neutral so driverkit and every Driver can read it (ADR 0065,
// issue #5179).
package hostop

import (
	"encoding/json"
	"time"

	"spindrift.dev/launcher/internal/landdelta"
	"spindrift.dev/launcher/internal/usage"
)

// Event is the subset of a Pass-log line a host op needs; a Driver's own line
// type carries the same two fields.
type Event struct {
	Type        string       `json:"type"`
	SpindriftOp *SpindriftOp `json:"spindrift_op,omitempty"`
}

// SpindriftOp is the payload of a synthetic "spindrift_op" stream-json event
// (issue #2027). The orchestrator prints one per discrete operation onto the
// same stdout that carries driver-exec's forwarded lines, so the heartbeat
// Writer can show it live.
type SpindriftOp struct {
	// Op names the operation kind: "pass_start", "verdict", "pass_no_outcome",
	// "decision", "run_state_error", "pass_usage", "land_delta",
	// "delta_review_trigger", "signal", "dispatch_start", "prompt_hashes", or
	// OpDispatchSettled (Settled carries its payload).
	Op   string `json:"op"`
	Pass int    `json:"pass,omitempty"`
	// Role names the pass's own role on a pass_start op (issue #2037):
	// "implement", "review", "fix", "land", or "delta-review". The land pass
	// (issue #2457, #2654) runs exactly once per run and cannot re-enter the
	// review cycle, but a run-once delta-review pass (issue #3246) can follow
	// it. Empty on every other op kind, and on a pass_start from the legacy
	// single-loop path that never distinguishes roles.
	Role    string `json:"role,omitempty"`
	Verdict string `json:"verdict,omitempty"`
	// Kind names the signal kind on a "signal" op (issue #3724): "comment",
	// "pr-intent", "issue-intent", "status", or "unknown" (the sentinel for
	// a path outside the four routes). Empty on every other op kind.
	Kind string `json:"kind,omitempty"`
	// Size is the signal's content byte count and Hash its content hash,
	// "sha256:"-prefixed hex, on a "signal" op.
	Size int    `json:"size,omitempty"`
	Hash string `json:"hash,omitempty"`
	// Decision is "continue" or "stop" on a "decision" op, "fire" or
	// "skip" on a "delta_review_trigger" op (issue #3246), or "accept",
	// "reject", "read", or "usage" on a "signal" op (issue #3724, #3867)
	// -- "read" for the status route, which never accepts or rejects
	// content and so carries no Reason; "usage" for the diagnostics-only
	// route a Box-side client usage failure reports on, Reason carrying
	// the client's error, never buffered. The op kinds share the field
	// because each names a small, fixed set of outcomes, normally paired
	// with a Reason.
	Decision string `json:"decision,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Phase    string `json:"phase,omitempty"` // "read", "write", "findings_log", "dispositions_log", "dispositions_budget", "decisions_log", or "decisions_budget", for run_state_error
	Error    string `json:"error,omitempty"`
	// Usage carries a pass's own end-of-pass token accounting on a
	// "pass_usage" op; nil on every other op kind.
	Usage *PassUsage `json:"usage,omitempty"`
	// Delta carries the land pass's post-approval tree delta on a
	// "land_delta" op (issue #3244); nil on every other op kind. It is
	// landdelta.Delta itself, not a package-local copy, so the orchestrator's
	// emission and settle's PR body agree on Summary()'s wording and on the
	// counted/zero/unknown split.
	Delta *landdelta.Delta `json:"delta,omitempty"`
	// Start carries a Dispatch's identity on a "dispatch_start" op; nil on
	// every other op kind.
	Start *DispatchStart `json:"dispatch_start,omitempty"`
	// PromptHashes carries the Box's per-role prompt template hashes on a
	// "prompt_hashes" op; nil on every other op kind.
	PromptHashes *PromptHashes `json:"prompt_hashes,omitempty"`
	// Settled carries a Dispatch's terminal state on a "dispatch_settled" op;
	// nil on every other op kind.
	Settled *DispatchSettled `json:"dispatch_settled,omitempty"`
}

// PromptHashes is the payload of a "prompt_hashes" SpindriftOp (issue #4786),
// written by the Box once after prompt assembly. Unlike the host-written
// DispatchStart it is Box-reported, so a consumer must treat it as untrusted;
// RecordID is the one the host forwarded, which keys it to its Dispatch's
// Record. Roles maps pass name to the template hash from
// promptassembly.TemplateHashes.
type PromptHashes struct {
	RecordID string            `json:"record_id"`
	Roles    map[string]string `json:"roles"`
}

// OpDispatchSettled is the SpindriftOp.Op of the host's terminal-state record.
const OpDispatchSettled = "dispatch_settled"

// OpDispatchStart is the SpindriftOp.Op of the Dispatch identity stamp.
const OpDispatchStart = "dispatch_start"

// DispatchSettled is the payload of a "dispatch_settled" SpindriftOp (issue
// #4785), appended by the host to the primary Pass log when settle flushes the
// Dispatch's terminal state. It is the only source a Dispatch Record takes its
// outcome from; RecordID ties it to the dispatch_start stamp of that log.
type DispatchSettled struct {
	RecordID string `json:"record_id"`
	// State is forge.DispatchState.String(): complete, failed, recoverable, or
	// ambiguous.
	State string `json:"state"`
	// Reason is settle's status vocabulary, e.g. fix-exhausted, ci-red,
	// merge-guard-hit, budget-exhausted, merged.
	Reason string `json:"reason,omitempty"`
	Note   string `json:"note,omitempty"`
	PRURL  string `json:"pr_url,omitempty"`
	// MergeCommit is the commit the forge reports for the merged PR (GitHub
	// mergeCommit, Forgejo merge_commit_sha); set only when Reason is merged and
	// the forge could say.
	MergeCommit string `json:"merge_commit,omitempty"`
	// HostToken echoes the log's DispatchStart.HostToken.
	HostToken string `json:"host_token,omitempty"`
}

// DispatchStart is the payload of a "dispatch_start" SpindriftOp (issue
// #4783), written by the host at the top of every Pass log of a Dispatch.
// Started is when this particular log was created, so it tells the logs of
// one Dispatch apart.
type DispatchStart struct {
	RecordID string `json:"record_id"`
	// HostToken is a random value the host mints per log and never gives the
	// Box; a dispatch_settled op counts only when it echoes it (see
	// dispatchrecord parseLog).
	HostToken     string            `json:"host_token,omitempty"`
	Kind          string            `json:"kind"`
	DispatchKey   string            `json:"dispatch_key"`
	ClaimTime     time.Time         `json:"claim_time"`
	Started       time.Time         `json:"started"`
	Revision      string            `json:"revision,omitempty"`
	RoleModels    map[string]string `json:"role_models,omitempty"`
	Driver        string            `json:"driver,omitempty"`
	DriverVersion string            `json:"driver_version,omitempty"`
	Knobs         map[string]string `json:"knobs,omitempty"`
}

// PassUsage is the orchestrator's end-of-pass token accounting, the payload of
// a "pass_usage" SpindriftOp (issue #3156).
type PassUsage struct {
	APICalls                 int                `json:"api_calls,omitempty"`
	UncachedInputTokens      int                `json:"uncached_input_tokens,omitempty"`
	OutputTokens             int                `json:"output_tokens,omitempty"`
	CacheReadInputTokens     int                `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int                `json:"cache_creation_input_tokens,omitempty"`
	Agents                   []usage.AgentUsage `json:"agents,omitempty"`
	// OutputIsMainLoopOnly carries usage.Report's flag of the same name onto
	// the wire so the renderer does not have to assume it. A driver whose
	// report leaves it false reports whole-pass output.
	OutputIsMainLoopOnly bool `json:"output_is_main_loop_only,omitempty"`
}

// EncodeSpindriftOp returns one newline-terminated stream-json line encoding op
// as a synthetic "spindrift_op" event (issue #2027), ready to write onto the
// same stdout driver-exec's raw output flows through.
func EncodeSpindriftOp(op SpindriftOp) string {
	b, err := json.Marshal(Event{Type: "spindrift_op", SpindriftOp: &op})
	if err != nil {
		// Every field is a string, number, time, or a map/struct of those,
		// so json.Marshal cannot practically fail. This is an observability
		// path, so a failure degrades to "no marker emitted" rather than
		// crashing the orchestrator's loop.
		return ""
	}
	return string(b) + "\n"
}
