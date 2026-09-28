#!/usr/bin/env bats
# In-box prompt-contract validator (issue #2249): it rejects or warns on the
# assembled prompt using lib/prompt-contract.nix's validateMarkers registry,
# read from PROMPT_CONTRACT_REGISTRY_FILE. Issue #2513 moved forbidden-marker
# enforcement to lib/prompt-contract.nix (build time) and readonlyguards.go.

# The hand-written PROMPTS_DIR stub, rather than the real templates, keeps
# each case's marker set unambiguous.

load helper

setup() {
  setup_entrypoint_env
}

# Assemble hard-fails on this path if either review-prompt.md or
# worker-prompt.md is missing (issues #2059, #2058). The review-prompt.md
# stub carries a VERDICT: line so a test that enables the orchestrator for an
# unrelated reason does not also trip the reviewer-verdict reject; only the
# cases exercising that row override this file to omit the marker.
_stub_prompt_dir() {
  local dir="$BATS_TEST_TMPDIR/prompts"
  mkdir -p "$dir"
  printf 'issue stub\n' >"$dir/issue-prompt.md"
  printf 'scout stub\n' >"$dir/scout-prompt.md"
  printf 'reviewer stub\n\nVERDICT: APPROVE or BLOCK\n' >"$dir/review-prompt.md"
  printf 'worker stub\n' >"$dir/worker-prompt.md"
  printf '%s' "$dir"
}

@test "pass: read-write, non-research, no filer, orchestrator off -- no reject/warn, driver invoked" {
  export PROMPTS_DIR="$(_stub_prompt_dir)"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -s "$DRIVER_LOG" ]
  [ -s "$DRIVER_PROMPT_FILE" ]
}

# Row "verdict-comment-relay": under research + read-only the SPINDRIFT_COMMENT
# relay is the run's only way to hand a verdict to the launcher, so a
# research-prompt.md missing the marker is fatal before the Driver runs.
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

# Row "reviewer-verdict": with the orchestrator on, the review pass gates its
# multi-pass loop on review-prompt.md's VERDICT: line, so a missing line
# leaves that loop nothing to gate on.
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

# Issue #2249 acceptance criterion #3: with the orchestrator off (inline
# mode) phase_prompt_assembly never populates review_prompt_rendered, so the
# reviewer-verdict condition is false whatever review-prompt.md contains.
@test "no false positive: ORCHESTRATOR_ENABLED unset, review prompt missing VERDICT: -> exit 0, Driver invoked" {
  local prompt_dir
  prompt_dir="$(_stub_prompt_dir)"
  printf 'reviewer stub, no verdict line here\n' >"$prompt_dir/review-prompt.md"
  export PROMPTS_DIR="$prompt_dir"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -s "$DRIVER_LOG" ]
}

# Row "pr-intent": under read-only, non-research the SPINDRIFT_PR_INTENT
# relay carries the finished branch to the launcher, but the post-driver
# marker-gate nudge and settle's bundle-adopt salvage already back it up, so
# a missing marker is advisory and the run proceeds.
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

# Row "issue-intent": a filer-relay dispatch (filer configured, orchestrator
# on, read-only) relays filed issues via SPINDRIFT_ISSUE_INTENT, checked
# against agents_json's .filer.prompt rather than $prompt. The filer's
# best-effort PR-body fallback backs it up, so a missing marker is advisory.
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

# Issues #2318 and #2356: Validate must read each registry row's severity and
# when fields, not a hardcoded per-id switch. This reuses the warn pr-intent
# fixture above but patches that row's severity to "reject", so a hardcoded
# switch ignores the patch and still exits 0, while a real row lookup rejects.
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
  # A future rename or reshape of the row would turn the jq filter into a
  # no-op and leave this test green for the wrong reason.
  [ "$(jq -r '.[] | select(.id == "pr-intent") | .severity' "$patched")" = "reject" ]

  export PROMPT_CONTRACT_REGISTRY_FILE="$patched"
  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  [ ! -s "$DRIVER_LOG" ]
  [ ! -s "$DRIVER_PROMPT_FILE" ]
}

# Issue #2249 acceptance criterion: the reject fires on the literal marker
# string only, so rewording the prose around it must not trip the reject.
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
