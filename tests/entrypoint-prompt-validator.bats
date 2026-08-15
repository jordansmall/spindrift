#!/usr/bin/env bats
# In-box prompt-contract validator (issue #2249): a reject/warn matrix run at
# the tail of phase_prompt_assembly, after fragment rendering/injection and
# before the Driver call, that scans the fully-assembled prompt/agents_json
# for the markers named in lib/prompt-contract.nix's validateMarkers and
# forbiddenMarkers registries. The Go validator
# (cmd/launcher/internal/promptassembly/validate.go) reads those registries
# from JSON (promptContractRegistryJson, forbiddenMarkersRegistryJson) --
# baked into the real image by lib/image.nix, but supplied here as plain
# files via nix/checks/bats.nix's PROMPT_CONTRACT_REGISTRY_FILE/
# FORBIDDEN_MARKERS_REGISTRY_FILE env vars -- not a bash array, gated on the
# same condition that gated the fragment/step that's supposed to carry each
# one.
#
# Full-script style (mirrors tests/entrypoint-outcome-contract.bats): a
# minimal hand-written PROMPTS_DIR stub, not the real templates, gives exact
# control over which markers are present so each case is unambiguous.

load helper

setup() {
  setup_entrypoint_env
}

# A stub prompt dir with just the files phase_prompt_assembly's default
# (non-research, non-fix) path reads: issue-prompt.md (the main prompt),
# scout/review-prompt.md (read whenever a scout/reviewer subagent is
# provisioned or the orchestrator's own review pass renders), and
# worker-prompt.md (issue #2059, #2058 -- read unconditionally alongside
# review-prompt.md, same gate: Assemble hard-fails if either is missing on
# this path).
#
# review-prompt.md's stub carries a VERDICT: line by default so a test that
# turns ORCHESTRATOR_ENABLED on for an unrelated reason (e.g. the
# issue-intent warn case below) doesn't incidentally trip the
# reviewer-verdict reject too -- only the tests exercising that row itself
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
# non-research, no filer configured, orchestrator off) -- the validator must
# be a no-op and the Driver must still run. Establishes the stub-fixture
# convention the rest of this suite's cases build on.
@test "pass: read-write, non-research, no filer, orchestrator off -- no reject/warn, driver invoked" {
  export PROMPTS_DIR="$(_stub_prompt_dir)"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -s "$DRIVER_LOG" ]
  [ -s "$DRIVER_PROMPT_FILE" ]
}

# reject verdict-comment-relay (lib/prompt-contract.nix validateMarkers row
# "verdict-comment-relay"): research + read-only means the run's only way to
# post its verdict is the SPINDRIFT_COMMENT relay -- a research-prompt.md
# missing that marker can never hand its verdict to the launcher, so this is
# fatal, before the Driver ever runs.
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

# reject reviewer-verdict (validateMarkers row "reviewer-verdict"): with the
# orchestrator on, the code-owned review pass gates its multi-pass loop on
# review-prompt.md's own VERDICT: line -- missing it here means that loop has
# nothing to gate on.
@test "reject: ORCHESTRATOR_ENABLED set, review prompt missing VERDICT: -> non-zero exit, Driver never invoked" {
  local prompt_dir
  prompt_dir="$(_stub_prompt_dir)"
  printf 'reviewer stub, no verdict line here\n' >"$prompt_dir/review-prompt.md"
  export PROMPTS_DIR="$prompt_dir"
  export ORCHESTRATOR_ENABLED=1
  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  grep -q 'VERDICT:' <<<"$output"
  [ ! -s "$DRIVER_LOG" ]
  [ ! -s "$DRIVER_PROMPT_FILE" ]
}

# No false positive (issue #2249 acceptance criterion #3): with the
# orchestrator OFF (inline mode), review_prompt_rendered is never populated
# (phase_prompt_assembly's if/elif/else), so the reviewer-verdict reject
# condition is naturally false regardless of what review-prompt.md contains
# -- the inline reviewer subagent loop this run actually uses is unaffected.
@test "no false positive: ORCHESTRATOR_ENABLED unset, review prompt missing VERDICT: -> exit 0, Driver invoked" {
  local prompt_dir
  prompt_dir="$(_stub_prompt_dir)"
  printf 'reviewer stub, no verdict line here\n' >"$prompt_dir/review-prompt.md"
  export PROMPTS_DIR="$prompt_dir"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -s "$DRIVER_LOG" ]
}

# warn pr-intent (validateMarkers row "pr-intent"): read-only, non-research
# means the finished branch's only path to the launcher is the
# SPINDRIFT_PR_INTENT relay -- already has a working non-fatal backstop (the
# post-driver required-marker-gate nudge, plus settle's bundle-adopt salvage
# path), so a missing marker here is advisory, not fatal: the run proceeds.
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

# warn issue-intent (validateMarkers row "issue-intent"): a filer-relay
# dispatch (filer configured + orchestrator on + read-only) hands its filed
# issues to the launcher via the filer's own SPINDRIFT_ISSUE_INTENT relay,
# checked against agents_json's .filer.prompt, not $prompt. Already has a
# working non-fatal backstop (the filer's best-effort PR-body fallback), so
# a missing marker here is advisory, not fatal.
@test "warn: filer configured, ORCHESTRATOR_ENABLED set, read-only, filer prompt missing SPINDRIFT_ISSUE_INTENT -> exit 0, Driver invoked, stderr advisory" {
  local prompt_dir
  prompt_dir="$(_stub_prompt_dir)"
  printf 'filer stub, no issue-intent marker here\n' >"$prompt_dir/filer-prompt.md"
  export PROMPTS_DIR="$prompt_dir"
  export AGENTS_JSON_TEMPLATE='{"filer":{"description":"filer","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]}}'
  export ORCHESTRATOR_ENABLED=1
  unset BOX_WRITE_ENABLED
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -s "$DRIVER_LOG" ]
  grep -q 'SPINDRIFT_ISSUE_INTENT' <<<"$output"
}

# Data-driven proof (issue #2318, re-pointed at the Go validator by issue
# #2356): promptassembly.Validate must decide reject-vs-warn (and its gating
# condition) from the `severity`/`when` fields it reads off each row in
# PROMPT_CONTRACT_REGISTRY_FILE -- lib/prompt-contract.nix's validateMarkers
# registry, rendered to JSON for the `driver-exec assemble-prompt` verb's
# `--validate-markers-registry` flag -- not from a hardcoded per-id switch.
# This case takes the exact "warn pr-intent" fixture from the case above
# (read-only, non-research, issue prompt missing SPINDRIFT_PR_INTENT), but
# patches the *rendered registry's* pr-intent row severity field from "warn"
# to "reject" before invoking the entrypoint. A row lookup that actually
# reads the row's severity field must then treat this as a reject row: exit
# non-zero, Driver never invoked -- exactly like the verdict-comment-relay/
# reviewer-verdict reject cases above. A hardcoded per-id switch ignores the
# patched field entirely and keeps behaving like the unpatched
# "warn: ... -> exit 0" case, so this proves the data-driven dispatch rather
# than a setup mistake.
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
  # Guard the patch actually landed, so a future rename/reshape of the row
  # fails this test loudly instead of silently degrading to a no-op jq
  # filter and a green test for the wrong reason.
  [ "$(jq -r '.[] | select(.id == "pr-intent") | .severity' "$patched")" = "reject" ]

  export PROMPT_CONTRACT_REGISTRY_FILE="$patched"
  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  [ ! -s "$DRIVER_LOG" ]
  [ ! -s "$DRIVER_PROMPT_FILE" ]
}

# A reworded-but-marker-present section is respected (issue #2249 acceptance
# criterion): the reject fires on the literal marker string only, not on any
# particular surrounding prose/heading -- rewording the section around the
# marker, while keeping the marker itself, must not trip the reject.
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
