#!/usr/bin/env bats
# Self-contained research (ADR 0022, issue #2202): SELF_CONTAINED=1 selects the
# no-repo research sub-mode -- no clone, no explore, distinct baked prompt.

load helper

setup() {
  setup_entrypoint_env
}

@test "SELF_CONTAINED=1 drives research-self-contained-prompt.md, not research-prompt.md" {
  export DISPATCH_KIND="research"
  export SELF_CONTAINED="1"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qi "self-contained" "$CLAUDE_PROMPT_FILE"
  ! grep -q "Fresh clone" "$CLAUDE_PROMPT_FILE"
  ! grep -qi "explore the actual repo" "$CLAUDE_PROMPT_FILE"
  ! grep -q "^# EXPLORE$" "$CLAUDE_PROMPT_FILE"
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
  [ "$(grep -c '# POST THE VERDICT' "$CLAUDE_PROMPT_FILE")" -eq 1 ]
}

@test "SELF_CONTAINED=1 with a local issue tracker starts with no REPO_SLUG/GH_TOKEN" {
  export DISPATCH_KIND="research"
  export SELF_CONTAINED="1"
  export ISSUE_TRACKER="local"
  unset REPO_SLUG
  unset GH_TOKEN
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -d "$WORK_DIR/.git" ]
  grep -qi "self-contained" "$CLAUDE_PROMPT_FILE"
}

@test "SELF_CONTAINED=1 with a github tracker but no REPO_SLUG still fails loudly at startup" {
  export DISPATCH_KIND="research"
  export SELF_CONTAINED="1"
  unset REPO_SLUG
  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  grep -q "REPO_SLUG" <<<"$output"
}

# --- regression: research without SELF_CONTAINED is unaffected --------------

@test "DISPATCH_KIND=research without SELF_CONTAINED still drives research-prompt.md and clones" {
  export DISPATCH_KIND="research"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "Research GitHub issue #7" "$CLAUDE_PROMPT_FILE"
  ! grep -qi "self-contained" "$CLAUDE_PROMPT_FILE"
  [ -d "$WORK_DIR/.git" ]
}
