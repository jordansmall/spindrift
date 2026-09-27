#!/usr/bin/env bats
# Butler dispatch kind (ADR 0056, issue #3875): DISPATCH_KIND=butler carries one
# Ledger Chore (CHORE_NAME) instead of a tracker issue, and selects
# butler-prompt.md instead of issue-prompt.md, so a butler Box must start
# without ISSUE_NUMBER.

load helper

setup() {
  setup_entrypoint_env
}

# Switches from the ISSUE_NUMBER/ISSUE_TITLE cell setup_entrypoint_env sets by
# default to the butler's own CHORE_* cell. "bugs" is a real chore under
# templates/default/prompts/chores/ (choreSection reads it by name), not a
# fixture stand-in.
set_butler_env() {
  unset ISSUE_NUMBER ISSUE_TITLE
  export DISPATCH_KIND="butler"
  export ADVISE_ONLY=1
  export CHORE_NAME="bugs"
  export CHORE_HEAD="deadbeef"
  export CHORE_DIFF_RANGE=""
  export CHORE_SLICE="agent/entrypoint.sh"
}

@test "DISPATCH_KIND=butler with no ISSUE_NUMBER/ISSUE_TITLE drives butler-prompt.md" {
  set_butler_env
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "One-shot sweep of the" "$DRIVER_PROMPT_FILE"
  grep -q "chore (ADR 0056)" "$DRIVER_PROMPT_FILE"
  # The chore name reaches the rendered prompt via CHORE_NAME substitution.
  grep -q "SPINDRIFT_OUTCOME issue=butler-bugs landing=none status=ready" "$DRIVER_PROMPT_FILE"
  ! grep -q "Fresh clone, new branch" "$DRIVER_PROMPT_FILE"
}

@test "DISPATCH_KIND=butler without CHORE_NAME fails naming CHORE_NAME" {
  set_butler_env
  unset CHORE_NAME
  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  grep -q "CHORE_NAME is required" <<<"$output"
}

@test "non-butler dispatch without ISSUE_NUMBER fails naming ISSUE_NUMBER" {
  unset ISSUE_NUMBER
  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  grep -q "ISSUE_NUMBER is required" <<<"$output"
}

@test "butler kind logs sweeping the chore and completes as butler-<name>, never checks out or pushes an agent branch" {
  set_butler_env
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "==> claude sweeping chore bugs" <<<"$output"
  grep -q "==> entrypoint complete for butler-bugs" <<<"$output"
  ! grep -q "claude implementing issue" <<<"$output"
  ! grep -q "claude researching issue" <<<"$output"

  run git -C "$WORK_DIR" rev-parse --abbrev-ref HEAD
  [ "$output" = "main" ]
  run git -C "$BATS_TEST_TMPDIR" ls-remote "https://github.com/owner/repo.git" "butler-bugs"
  [ -z "$output" ]
}
