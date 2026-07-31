#!/usr/bin/env bats
# Driver session id pin/resume across cold and fix passes (issue #427).

load helper

setup() {
  setup_entrypoint_env
}

# Driver session pin/resume (issue #427): the fix Box resumes the same
# Driver session the initial run pinned, so a warm fix pass continues the
# agent's own conversation instead of relearning the changes it already made.
# The id is deterministic (REPO_SLUG + ISSUE_NUMBER only) so no state beyond
# those two env vars is needed to recompute it on either side.

@test "cold run pins a deterministic session id via --session-id" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qE -- '--session-id [0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}' "$CLAUDE_LOG"
  ! grep -q -- '--resume' "$CLAUDE_LOG"
}

@test "fix pass with no prior session data falls back with no --resume flag" {
  export FIX_PASS="2"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -q -- '--resume' "$CLAUDE_LOG"
}

@test "fix pass resumes the exact session id the initial run pinned" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  local pinned_id
  pinned_id="$(grep -oE -- '--session-id [0-9a-f-]+' "$CLAUDE_LOG")"
  pinned_id="${pinned_id#--session-id }"
  [ -n "$pinned_id" ]

  # Simulate the persisted session transcript a writable /home/agent/.claude
  # mount would carry over from the initial run into the fix box.
  mkdir -p "$HOME/.claude/projects/fake-project"
  touch "$HOME/.claude/projects/fake-project/${pinned_id}.jsonl"

  # The fix box is a fresh container with its own empty clone target -- only
  # the $HOME/.claude session cache mount carries over. Reusing WORK_DIR here
  # would have this second run try to clone into the first run's non-empty
  # checkout, which no real Box ever does.
  : >"$CLAUDE_LOG"
  export FIX_PASS="2"
  export WORK_DIR="$BATS_TEST_TMPDIR/work-fix"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q -- "--resume ${pinned_id}" "$CLAUDE_LOG"
}

@test "RESUME_AFTER_HOLD resumes the pinned session on the work path" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  local pinned_id
  pinned_id="$(grep -oE -- '--session-id [0-9a-f-]+' "$CLAUDE_LOG")"
  pinned_id="${pinned_id#--session-id }"
  [ -n "$pinned_id" ]

  # Simulate the persisted session transcript a writable /home/agent/.claude
  # mount would carry over from the initial (429-held) run into the
  # re-dispatched box.
  mkdir -p "$HOME/.claude/projects/fake-project"
  touch "$HOME/.claude/projects/fake-project/${pinned_id}.jsonl"

  # A re-dispatch after a hold is a fresh container with its own empty clone
  # target -- only the $HOME/.claude session cache mount carries over. Reusing
  # WORK_DIR here would have this second run try to clone into the first
  # run's non-empty checkout, which no real Box ever does.
  : >"$CLAUDE_LOG"
  export RESUME_AFTER_HOLD=1
  export WORK_DIR="$BATS_TEST_TMPDIR/work-resume-after-hold"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q -- "--resume ${pinned_id}" "$CLAUDE_LOG"

  # Still the cold-run issue-prompt.md (not fix-prompt.md): RESUME_AFTER_HOLD
  # is orthogonal to FIX_PASS and only changes the driver session mode.
  # "Fresh clone, new branch" is issue-prompt.md-only phrasing; fix-prompt.md
  # instead says the branch is "already checked out with prior work".
  grep -q "Fresh clone, new branch" "$CLAUDE_PROMPT_FILE"
}

@test "session id is stable across independent cold runs of the same issue" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  local first_id
  first_id="$(grep -oE -- '--session-id [0-9a-f-]+' "$CLAUDE_LOG")"
  first_id="${first_id#--session-id }"

  # Each cold run is its own fresh container/clone target; only ISSUE_NUMBER
  # and REPO_SLUG carry over to make the session id deterministic.
  : >"$CLAUDE_LOG"
  export WORK_DIR="$BATS_TEST_TMPDIR/work-2"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  local second_id
  second_id="$(grep -oE -- '--session-id [0-9a-f-]+' "$CLAUDE_LOG")"
  second_id="${second_id#--session-id }"

  [ -n "$first_id" ]
  [ "$first_id" = "$second_id" ]
}

