#!/usr/bin/env bats
# Butler dispatch kind (ADR 0056, issue #3875): DISPATCH_KIND=butler carries one
# Ledger Chore (CHORE_NAME) instead of a tracker issue, and selects
# butler-prompt.md instead of issue-prompt.md, so a butler Box must start
# without ISSUE_NUMBER. Also pins the butler's forced read-only posture
# (issue #3906): no gh shim escape hatch, and no PR-intent nudge either,
# since an advise-only kind never opens a PR.

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
  export CHORE_NAME="bugs"
  set_dispatch_kind butler
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

@test "butler Box with BOX_WRITE_ENABLED unset installs the read-only gh shim, rejecting gh pr create (#3906)" {
  set_butler_env
  unset BOX_WRITE_ENABLED
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  local shim_dir
  shim_dir="$HOME/.spindrift/readonly-gh-shim"
  [ -d "$shim_dir" ]

  PATH="$shim_dir:$PATH" run gh pr create --title "x" --body "y"
  [ "$status" -ne 0 ]
  [[ "$output" == *"gh pr create"* ]]
}

@test "butler Box missing PR-intent never gets a PR-intent nudge, advise-only never opens a PR (#3906)" {
  set_butler_env
  unset BOX_WRITE_ENABLED
  export BOX_OUTBOX_RELAY_CAPABLE=1
  export RUN_NONCE="deadbeefcafe1234"
  export FAKE_DRIVER_NO_PR_INTENT=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -q "PR-intent marker missing" <<<"$output"
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 1 ]
}
