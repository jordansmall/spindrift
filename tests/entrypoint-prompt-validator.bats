#!/usr/bin/env bats
# In-box prompt-contract validator (issue #2249): a reject/warn matrix run at
# the tail of phase_prompt_assembly, after fragment rendering and before the
# Driver call, that scans the fully-assembled prompt/agents_json for the markers
# named in lib/prompt-contract.nix's validateMarkers registry. The Go validator
# reads that registry from JSON, baked into the real image by lib/image.nix but
# supplied here as a plain file via nix/checks/bats.nix's
# PROMPT_CONTRACT_REGISTRY_FILE. Forbidden-marker enforcement is not this
# validator's job (issue #2513): it lives build-time in
# buildTimeForbiddenMarkerViolations and runtime in readonlyguards.go.
#
# Full-script style: a minimal hand-written PROMPTS_DIR stub, not the real
# templates, gives exact control over which markers are present.

load helper

setup() {
  setup_entrypoint_env
}

# A stub prompt dir with just the files phase_prompt_assembly's default
# (non-research, non-fix) path reads: issue-prompt.md, scout/review-prompt.md,
# and worker-prompt.md -- Assemble hard-fails if any is missing on this path.
#
# review-prompt.md's stub carries a VERDICT: line by default so a test that
# turns ORCHESTRATOR_ENABLED on for an unrelated reason doesn't incidentally
# trip the reviewer-verdict reject too; only the tests exercising that row
# override this file to omit the marker.
_stub_prompt_dir() {
  local dir="$BATS_TEST_TMPDIR/prompts"
  mkdir -p "$dir"
  printf 'issue stub\n' >"$dir/issue-prompt.md"
  printf 'scout stub\n' >"$dir/scout-prompt.md"
  printf 'reviewer stub\n\nVERDICT: APPROVE or BLOCK\n' >"$dir/review-prompt.md"
  printf 'worker stub\n' >"$dir/worker-prompt.md"
  printf '%s' "$dir"
}

# Baseline: every reject/warn condition in the matrix is false (read-write,
# non-research, no filer configured, orchestrator off) -- the validator must be
# a no-op and the Driver must still run.
@test "pass: read-write, non-research, no filer, orchestrator off -- no reject/warn, driver invoked" {
  export PROMPTS_DIR="$(_stub_prompt_dir)"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -s "$DRIVER_LOG" ]
  [ -s "$DRIVER_PROMPT_FILE" ]
}

# reject verdict-comment-relay: research + read-only means the run's only way to
# post its verdict is the SPINDRIFT_COMMENT relay, so a research-prompt.md
# missing that marker can never hand its verdict to the launcher -- fatal,
# before the Driver ever runs.
@test "reject: research + read-only, research prompt missing SPINDRIFT_COMMENT -> non-zero exit, Driver never invoked" {
  local prompt_dir
  prompt_dir="$(_stub_prompt_dir)"
  printf 'research stub, no verdict-comment marker here\n' >"$prompt_dir/research-prompt.md"
  export PROMPTS_DIR="$prompt_dir"
  export DISPATCH_KIND="research"
  unset BOX_WRITE_ENABLED
  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  grep -q 'SPINDRIFT_COMMENT' <<<"$output"
  [ ! -s "$DRIVER_LOG" ]
  [ ! -s "$DRIVER_PROMPT_FILE" ]
}

@test "pass: research + read-only, research prompt contains SPINDRIFT_COMMENT -> exit 0, Driver invoked" {
  local prompt_dir
  prompt_dir="$(_stub_prompt_dir)"
  printf 'research stub\n\nPost your verdict with SPINDRIFT_COMMENT here\n' >"$prompt_dir/research-prompt.md"
  export PROMPTS_DIR="$prompt_dir"
  export DISPATCH_KIND="research"
  unset BOX_WRITE_ENABLED
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -s "$DRIVER_LOG" ]
  grep -q 'SPINDRIFT_COMMENT' "$DRIVER_PROMPT_FILE"
}

# reject reviewer-verdict: with the orchestrator on, the code-owned review pass
# gates its multi-pass loop on review-prompt.md's own VERDICT: line -- missing
# it means that loop has nothing to gate on.
@test "reject: ORCHESTRATOR_ENABLED set, review prompt missing VERDICT: -> non-zero exit, Driver never invoked" {
  local prompt_dir
  prompt_dir="$(_stub_prompt_dir)"
  printf 'reviewer stub, no verdict line here\n' >"$prompt_dir/review-prompt.md"
  export PROMPTS_DIR="$prompt_dir"
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  grep -q 'VERDICT:' <<<"$output"
  [ ! -s "$DRIVER_LOG" ]
  [ ! -s "$DRIVER_PROMPT_FILE" ]
}

# No false positive (issue #2249 AC3): with the orchestrator OFF,
# review_prompt_rendered is never populated, so the reviewer-verdict condition
# is naturally false regardless of what review-prompt.md contains -- the inline
# reviewer subagent loop this run actually uses is unaffected.
@test "no false positive: ORCHESTRATOR_ENABLED unset, review prompt missing VERDICT: -> exit 0, Driver invoked" {
  local prompt_dir
  prompt_dir="$(_stub_prompt_dir)"
  printf 'reviewer stub, no verdict line here\n' >"$prompt_dir/review-prompt.md"
  export PROMPTS_DIR="$prompt_dir"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -s "$DRIVER_LOG" ]
}

# warn pr-intent: read-only, non-research means the finished branch's only path
# to the launcher is the SPINDRIFT_PR_INTENT relay -- but that already has a
# working non-fatal backstop (the post-driver nudge, plus settle's bundle-adopt
# salvage), so a missing marker is advisory and the run proceeds.
@test "warn: read-only, non-research, issue prompt missing SPINDRIFT_PR_INTENT -> exit 0, Driver invoked, stderr advisory" {
  local prompt_dir
  prompt_dir="$(_stub_prompt_dir)"
  printf 'issue stub, no PR-intent marker here\n' >"$prompt_dir/issue-prompt.md"
  export PROMPTS_DIR="$prompt_dir"
  unset BOX_WRITE_ENABLED
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -s "$DRIVER_LOG" ]
  grep -q 'SPINDRIFT_PR_INTENT' <<<"$output"
}

# warn issue-intent: a filer-relay dispatch (filer configured + orchestrator on
# + read-only) hands its filed issues to the launcher via the filer's own
# SPINDRIFT_ISSUE_INTENT relay, checked against agents_json's .filer.prompt, not
# $prompt. Its non-fatal backstop is the filer's PR-body fallback.
@test "warn: filer configured, ORCHESTRATOR_ENABLED set, read-only, filer prompt missing SPINDRIFT_ISSUE_INTENT -> exit 0, Driver invoked, stderr advisory" {
  local prompt_dir
  prompt_dir="$(_stub_prompt_dir)"
  printf 'filer stub, no issue-intent marker here\n' >"$prompt_dir/filer-prompt.md"
  export PROMPTS_DIR="$prompt_dir"
  export AGENTS_JSON_TEMPLATE='{"filer":{"description":"filer","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]}}'
  export BOX_FILER_ENABLED=1
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  unset BOX_WRITE_ENABLED
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -s "$DRIVER_LOG" ]
  grep -q 'SPINDRIFT_ISSUE_INTENT' <<<"$output"
}

# Data-driven proof (issue #2318): promptassembly.Validate must decide
# reject-vs-warn (and its gating condition) from the `severity`/`when` fields it
# reads off each registry row, not from a hardcoded per-id switch. This case
# takes the exact "warn pr-intent" fixture above but patches the *rendered
# registry's* pr-intent row severity from "warn" to "reject" first. A row lookup
# that really reads the field must then reject: exit non-zero, Driver never
# invoked. A hardcoded switch would ignore the patch and still exit 0.
@test "data-driven: pr-intent row patched to severity=reject -> non-zero exit, Driver never invoked" {
  : "${PROMPT_CONTRACT_REGISTRY_FILE:?PROMPT_CONTRACT_REGISTRY_FILE must be set (lib/prompt-contract.nix validateMarkers rendered to JSON)}"

  local prompt_dir
  prompt_dir="$(_stub_prompt_dir)"
  printf 'issue stub, no PR-intent marker here\n' >"$prompt_dir/issue-prompt.md"
  export PROMPTS_DIR="$prompt_dir"
  unset BOX_WRITE_ENABLED

  local patched="$BATS_TEST_TMPDIR/prompt-contract-registry-severity-patched.json"
  jq '(.[] | select(.id == "pr-intent") | .severity) = "reject"' \
    "$PROMPT_CONTRACT_REGISTRY_FILE" >"$patched"
  # Guard the patch actually landed, so a future rename/reshape of the row fails
  # loudly instead of degrading to a no-op jq filter and a green test.
  [ "$(jq -r '.[] | select(.id == "pr-intent") | .severity' "$patched")" = "reject" ]

  export PROMPT_CONTRACT_REGISTRY_FILE="$patched"
  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  [ ! -s "$DRIVER_LOG" ]
  [ ! -s "$DRIVER_PROMPT_FILE" ]
}

# A reworded-but-marker-present section is respected: the reject fires on the
# literal marker string only, not on any particular surrounding prose or
# heading.
@test "reworded-but-marker-present: research prompt's verdict section reworded but keeps SPINDRIFT_COMMENT -> exit 0, Driver invoked" {
  local prompt_dir
  prompt_dir="$(_stub_prompt_dir)"
  printf '# WRAP UP\n\nWhen you are all done, drop a comment carrying SPINDRIFT_COMMENT so the launcher hears your call.\n' \
    >"$prompt_dir/research-prompt.md"
  export PROMPTS_DIR="$prompt_dir"
  export DISPATCH_KIND="research"
  unset BOX_WRITE_ENABLED
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -s "$DRIVER_LOG" ]
  grep -q 'SPINDRIFT_COMMENT' "$DRIVER_PROMPT_FILE"
}
