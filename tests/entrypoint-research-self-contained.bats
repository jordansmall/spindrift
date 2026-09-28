#!/usr/bin/env bats
# Self-contained research (ADR 0022, issue #2202): SELF_CONTAINED=1 selects the
# no-repo research sub-mode: it clones nothing, explores nothing, and uses a
# distinct baked prompt.

load helper

setup() {
  setup_entrypoint_env
}

@test "SELF_CONTAINED=1 drives research-self-contained-prompt.md, not research-prompt.md" {
  export DISPATCH_KIND="research"
  export SELF_CONTAINED="1"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qi "self-contained" "$DRIVER_PROMPT_FILE"
  ! grep -q "Fresh clone" "$DRIVER_PROMPT_FILE"
  ! grep -qi "explore the actual repo" "$DRIVER_PROMPT_FILE"
  ! grep -q "^# EXPLORE$" "$DRIVER_PROMPT_FILE"
}

@test "SELF_CONTAINED=1 clones no repo" {
  export DISPATCH_KIND="research"
  export SELF_CONTAINED="1"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -d "$WORK_DIR/.git" ]
  run git -C "$WORK_DIR" rev-parse --show-toplevel
  [ "$status" -ne 0 ]
}

@test "SELF_CONTAINED=1 still injects the research outcome contract exactly once" {
  export DISPATCH_KIND="research"
  export SELF_CONTAINED="1"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '# POST THE VERDICT' "$DRIVER_PROMPT_FILE")" -eq 1 ]
}

@test "SELF_CONTAINED=1 with a local issue tracker starts with no REPO_SLUG/GH_TOKEN" {
  export DISPATCH_KIND="research"
  export SELF_CONTAINED="1"
  export ISSUE_TRACKER="local"
  export BOX_TRACKER_AXIS_READ=LOCAL
  unset BOX_TRACKER_AXIS_WRITE
  # entrypoint.sh reads this launcher-forwarded signal rather than deriving
  # no_repo's local-tracker half from ISSUE_TRACKER (issue #2527).
  export BOX_IN_BOX_UNREACHABLE_TRACKER=1
  unset REPO_SLUG
  unset GH_TOKEN
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -d "$WORK_DIR/.git" ]
  grep -qi "self-contained" "$DRIVER_PROMPT_FILE"
}

@test "SELF_CONTAINED=1 with a github tracker but no REPO_SLUG still fails loudly at startup" {
  export DISPATCH_KIND="research"
  export SELF_CONTAINED="1"
  unset REPO_SLUG
  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  grep -q "REPO_SLUG" <<<"$output"
}

@test "DISPATCH_KIND=research without SELF_CONTAINED still drives research-prompt.md and clones" {
  export DISPATCH_KIND="research"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "Research GitHub issue #7" "$DRIVER_PROMPT_FILE"
  ! grep -qi "self-contained" "$DRIVER_PROMPT_FILE"
  [ -d "$WORK_DIR/.git" ]
}

# The status list comes from RESEARCH_STATUS_ENUM in the registry (issue #2504).
@test "SELF_CONTAINED=1's OUTCOME grammar line renders the registry status enum" {
  export DISPATCH_KIND="research"
  export SELF_CONTAINED="1"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'SPINDRIFT_OUTCOME issue=7 landing=<verdict-comment-url> status=<recommend|reject|unclear> note=<one-line rationale>' "$DRIVER_PROMPT_FILE"
}
