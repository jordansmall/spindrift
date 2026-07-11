#!/usr/bin/env bats
# SPINDRIFT_OUTCOME contract injection into runtime prompt-dir overrides (issue #420).

load helper

setup() {
  setup_entrypoint_env
}

# A SPINDRIFT_PROMPT_DIR mount (simulated here by pointing PROMPTS_DIR straight
# at a host dir, exactly what the mount leaves the entrypoint seeing) whose
# issue-prompt.md drops the SPINDRIFT_OUTCOME contract must still reach the
# driver with it appended (issue #420) -- otherwise the agent never emits the
# outcome line and the launcher never learns the PR.
@test "runtime prompt-dir override lacking the outcome contract gets it appended" {
  local prompt_dir="$BATS_TEST_TMPDIR/prompts"
  mkdir -p "$prompt_dir"
  printf 'issue stub, no contract here\n' >"$prompt_dir/issue-prompt.md"
  printf 'scout stub\n' >"$prompt_dir/scout-prompt.md"
  printf 'reviewer stub\n' >"$prompt_dir/review-prompt.md"
  export PROMPTS_DIR="$prompt_dir"
  export OUTCOME_CONTRACT_FILE="$BATS_TEST_TMPDIR/outcome-contract.md"
  printf '# LAND THE CHANGE\n\ncanonical contract for %s\n' '${BRANCH}' >"$OUTCOME_CONTRACT_FILE"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '# LAND THE CHANGE' "$CLAUDE_PROMPT_FILE")" -eq 1 ]
  grep -q 'canonical contract for agent/issue-7' "$CLAUDE_PROMPT_FILE"
}

# A mounted prompt that already carries the contract (e.g. copied from a
# #419-baked prompt) must be passed through unchanged -- no duplication.
@test "runtime prompt-dir override already containing the outcome contract is unchanged" {
  local prompt_dir="$BATS_TEST_TMPDIR/prompts"
  mkdir -p "$prompt_dir"
  printf 'issue stub\n\n# LAND THE CHANGE\n\nalready has its own contract\n' \
    >"$prompt_dir/issue-prompt.md"
  printf 'scout stub\n' >"$prompt_dir/scout-prompt.md"
  printf 'reviewer stub\n' >"$prompt_dir/review-prompt.md"
  export PROMPTS_DIR="$prompt_dir"
  export OUTCOME_CONTRACT_FILE="$BATS_TEST_TMPDIR/outcome-contract.md"
  printf '# LAND THE CHANGE\n\nshould not appear\n' >"$OUTCOME_CONTRACT_FILE"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '# LAND THE CHANGE' "$CLAUDE_PROMPT_FILE")" -eq 1 ]
  grep -q 'already has its own contract' "$CLAUDE_PROMPT_FILE"
  ! grep -q 'should not appear' "$CLAUDE_PROMPT_FILE"
}

# The fix prompt shares the same COMMS, CHECK/COMMIT, and outcome-contract
# blocks the issue prompt bakes (issue #455 extends #419/#420's slice
# mechanism to fix-prompt.md): a runtime SPINDRIFT_PROMPT_DIR override whose
# fix-prompt.md carries only the fix-specific preamble must still reach the
# driver with all three shared blocks appended, in order.
@test "runtime prompt-dir override of the fix prompt gets COMMS/CHECK/outcome appended" {
  export FIX_PASS="2"
  local prompt_dir="$BATS_TEST_TMPDIR/prompts"
  mkdir -p "$prompt_dir"
  printf 'fix stub, no shared blocks here\n' >"$prompt_dir/fix-prompt.md"
  printf 'issue stub\n' >"$prompt_dir/issue-prompt.md"
  printf 'scout stub\n' >"$prompt_dir/scout-prompt.md"
  printf 'reviewer stub\n' >"$prompt_dir/review-prompt.md"
  export PROMPTS_DIR="$prompt_dir"
  export COMMS_CONTRACT_FILE="$BATS_TEST_TMPDIR/comms-contract.md"
  printf '# COMMS\n\ncanonical comms contract\n' >"$COMMS_CONTRACT_FILE"
  export CHECK_CONTRACT_FILE="$BATS_TEST_TMPDIR/check-contract.md"
  printf '# CHECK\n\ncanonical check contract\n' >"$CHECK_CONTRACT_FILE"
  export OUTCOME_CONTRACT_FILE="$BATS_TEST_TMPDIR/outcome-contract.md"
  printf '# LAND THE CHANGE\n\ncanonical outcome contract\n' >"$OUTCOME_CONTRACT_FILE"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '# COMMS' "$CLAUDE_PROMPT_FILE")" -eq 1 ]
  [ "$(grep -c '# CHECK' "$CLAUDE_PROMPT_FILE")" -eq 1 ]
  [ "$(grep -c '# LAND THE CHANGE' "$CLAUDE_PROMPT_FILE")" -eq 1 ]
  grep -q 'canonical comms contract' "$CLAUDE_PROMPT_FILE"
  grep -q 'canonical check contract' "$CLAUDE_PROMPT_FILE"
  grep -q 'canonical outcome contract' "$CLAUDE_PROMPT_FILE"
  # Order: fix stub, then COMMS, then CHECK, then the outcome contract.
  local stub_line comms_line check_line outcome_line
  stub_line="$(grep -n 'fix stub' "$CLAUDE_PROMPT_FILE" | head -1 | cut -d: -f1)"
  comms_line="$(grep -n '# COMMS' "$CLAUDE_PROMPT_FILE" | head -1 | cut -d: -f1)"
  check_line="$(grep -n '# CHECK' "$CLAUDE_PROMPT_FILE" | head -1 | cut -d: -f1)"
  outcome_line="$(grep -n '# LAND THE CHANGE' "$CLAUDE_PROMPT_FILE" | head -1 | cut -d: -f1)"
  [ "$stub_line" -lt "$comms_line" ]
  [ "$comms_line" -lt "$check_line" ]
  [ "$check_line" -lt "$outcome_line" ]
}

# A mounted fix prompt that already carries all three shared blocks (e.g.
# copied from a baked prompt) must pass through unchanged -- no duplication.
@test "runtime prompt-dir override of the fix prompt already containing shared blocks is unchanged" {
  export FIX_PASS="2"
  local prompt_dir="$BATS_TEST_TMPDIR/prompts"
  mkdir -p "$prompt_dir"
  printf 'fix stub\n\n# COMMS\n\nown comms\n\n# CHECK\n\nown check\n\n# LAND THE CHANGE\n\nown contract\n' \
    >"$prompt_dir/fix-prompt.md"
  printf 'issue stub\n' >"$prompt_dir/issue-prompt.md"
  printf 'scout stub\n' >"$prompt_dir/scout-prompt.md"
  printf 'reviewer stub\n' >"$prompt_dir/review-prompt.md"
  export PROMPTS_DIR="$prompt_dir"
  export COMMS_CONTRACT_FILE="$BATS_TEST_TMPDIR/comms-contract.md"
  printf '# COMMS\n\nshould not appear\n' >"$COMMS_CONTRACT_FILE"
  export CHECK_CONTRACT_FILE="$BATS_TEST_TMPDIR/check-contract.md"
  printf '# CHECK\n\nshould not appear\n' >"$CHECK_CONTRACT_FILE"
  export OUTCOME_CONTRACT_FILE="$BATS_TEST_TMPDIR/outcome-contract.md"
  printf '# LAND THE CHANGE\n\nshould not appear\n' >"$OUTCOME_CONTRACT_FILE"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '# COMMS' "$CLAUDE_PROMPT_FILE")" -eq 1 ]
  [ "$(grep -c '# CHECK' "$CLAUDE_PROMPT_FILE")" -eq 1 ]
  [ "$(grep -c '# LAND THE CHANGE' "$CLAUDE_PROMPT_FILE")" -eq 1 ]
  grep -q 'own comms' "$CLAUDE_PROMPT_FILE"
  grep -q 'own check' "$CLAUDE_PROMPT_FILE"
  grep -q 'own contract' "$CLAUDE_PROMPT_FILE"
  ! grep -q 'should not appear' "$CLAUDE_PROMPT_FILE"
}

# A missing/unreadable OUTCOME_CONTRACT_FILE must fail the entrypoint loudly
# rather than silently proceeding without the contract -- the exact failure
# mode #420 exists to prevent.
@test "entrypoint fails loudly when OUTCOME_CONTRACT_FILE is missing" {
  local prompt_dir="$BATS_TEST_TMPDIR/prompts"
  mkdir -p "$prompt_dir"
  printf 'issue stub, no contract here\n' >"$prompt_dir/issue-prompt.md"
  printf 'scout stub\n' >"$prompt_dir/scout-prompt.md"
  printf 'reviewer stub\n' >"$prompt_dir/review-prompt.md"
  export PROMPTS_DIR="$prompt_dir"
  export OUTCOME_CONTRACT_FILE="$BATS_TEST_TMPDIR/does-not-exist.md"
  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
}

