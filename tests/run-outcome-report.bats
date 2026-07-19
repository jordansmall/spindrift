#!/usr/bin/env bats
# Outcome report parsing (issue #41) and PR adoption when the outcome line is absent (issue #122).

load helper

setup() {
  setup_run_env
}

# --- Outcome report (issue #41) --------------------------------------------

@test "outcome report lists every dispatched issue with number pr and status" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 landing=https://github.com/owner/repo/pull/1 status=merged note=ok"
  export FAKE_PODMAN_OUTCOME_2="SPINDRIFT_OUTCOME issue=2 landing=https://github.com/owner/repo/pull/2 status=merged note=ok"
  export FAKE_GH_PR_STATE_1="MERGED"
  export FAKE_GH_PR_STATE_2="MERGED"
  export FAKE_GH_ISSUE_LABELS_1="agent-complete"
  export FAKE_GH_ISSUE_LABELS_2="agent-complete"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  [[ "$output" == *"#1"* ]]
  [[ "$output" == *"#2"* ]]
  [[ "$output" == *"status=verified-merged"* ]]
}

@test "outcome report flags blocked issue distinctly with its note" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tBlocker'
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 landing=https://github.com/owner/repo/pull/1 status=blocked note=stalled"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  [[ "$output" == *"!!"* ]]
  [[ "$output" == *"stalled"* ]]
}

@test "outcome report reports missing outcome line when log has none" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tOrphan'
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  [[ "$output" == *"missing"* ]]
}

@test "malformed outcome line renders as malformed; subsequent issue is verified independently" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  # Issue 1: outcome line present but missing required landing= and status= tokens.
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 note=missing-required-tokens"
  # Issue 2: well-formed outcome already merged.
  export FAKE_PODMAN_OUTCOME_2="SPINDRIFT_OUTCOME issue=2 landing=https://github.com/owner/repo/pull/2 status=merged note=ok"
  export FAKE_GH_PR_STATE_2="MERGED"
  export FAKE_GH_ISSUE_LABELS_2="agent-complete"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  [[ "$output" == *"#1"* ]]
  [[ "$output" == *"status=malformed"* ]]
  [[ "$output" == *"status=verified-merged"* ]]
}

# --- No-outcome PR handling (issue #122, inverted off draft-ness by #1654) --

@test "missing outcome line + open non-draft PR → not adopted, reported as blocked" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  # No FAKE_PODMAN_OUTCOME_1 → no SPINDRIFT_OUTCOME in log
  export FAKE_GH_PR_LIST_1="https://github.com/owner/repo/pull/1"
  # FAKE_GH_PR_DRAFT_1 not set → defaults to "false" (non-draft)
  run "$RUN_CMD"
  # A no-outcome run is never adopted off draft-ness (issue #1654) — a
  # non-draft PR is reported blocked exactly like a draft one, same as the
  # test below. All agents finish → exit 0.
  [ "$status" -eq 0 ]
  ! grep -q 'pr merge' "$GH_LOG"
  [[ "$output" == *"status=blocked"* ]]
  [[ "$output" != *"status=adopted"* ]]
}

@test "missing outcome line + draft PR → not adopted, reported as blocked" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  # No FAKE_PODMAN_OUTCOME_1 → no SPINDRIFT_OUTCOME in log
  export FAKE_GH_PR_LIST_1="https://github.com/owner/repo/pull/1"
  export FAKE_GH_PR_DRAFT_1="true"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q 'pr merge' "$GH_LOG"
  [[ "$output" == *"status=blocked"* ]]
  [[ "$output" != *"status=adopted"* ]]
}

@test "missing outcome line + no open PR → status=missing unchanged" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  # No FAKE_PODMAN_OUTCOME_1, no FAKE_GH_PR_LIST_1 → no PR found
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  [[ "$output" == *"status=missing"* ]]
  ! grep -q 'pr merge' "$GH_LOG"
}

