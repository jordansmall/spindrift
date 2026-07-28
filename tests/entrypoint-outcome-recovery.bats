#!/usr/bin/env bats
# Resume-once recovery (issue #1607): a Driver that exits 0 with no
# SPINDRIFT_OUTCOME line most often just ended its turn early rather than
# actually failing (issue #1542: ~15 minutes of scouting thrown away because
# the run ended while "waiting" on a backgrounded task). Before falling back
# to the synthetic status=blocked backstop (issue #593), the entrypoint
# resumes the same pinned session exactly once with a corrective nudge.

load helper

setup() {
  setup_entrypoint_env
}

# Same deterministic id formula as lib/drivers/claude.nix's
# sessionFlagsFnBody, so a test can pre-seed the transcript the initial run's
# --session-id pins without needing two separate ENTRYPOINT invocations.
pinned_session_id() {
  local h
  h="$(printf '%s' "spindrift-session:${REPO_SLUG:-}:${ISSUE_NUMBER:-}" | sha256sum | cut -c1-32)"
  printf '%s-%s-%s-%s-%s' "${h:0:8}" "${h:8:4}" "${h:12:4}" "${h:16:4}" "${h:20:12}"
}

# The fake claude forgets its outcome on the first call only, then reports a
# real one on the resumed call -- the run must settle on that outcome, with
# no synthetic backstop line appended.
@test "driver exits with no outcome -> resume pass emits an outcome, no synthetic backstop" {
  export FAKE_CLAUDE_NO_OUTCOME_FIRST_CALL_ONLY=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=https://github.com/owner/repo/pull/1 status=ready note=fake$' <<<"$output"
  # Exactly two Driver invocations: the initial pass and the one resume pass.
  [ "$(grep -c '^claude invoked for issue' "$CLAUDE_LOG")" -eq 2 ]
}

# The fake claude forgets its outcome on every call -- the resume pass also
# produces nothing, so the synthetic backstop fires as it did before this
# issue, but the note now says so a recovery pass was attempted.
@test "driver exits with no outcome, resume also emits none -> synthetic backstop notes the recovery attempt" {
  export FAKE_CLAUDE_NO_OUTCOME=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=blocked note=.*driver exited without emitting an outcome.*resume attempt also produced no outcome' <<<"$output"
  # Exactly two Driver invocations: the initial pass and the one resume pass.
  [ "$(grep -c '^claude invoked for issue' "$CLAUDE_LOG")" -eq 2 ]
}

# The fake commits identical fixture content on every call (issue #1607's
# recovery pass is a second invocation of the same fake), so the second call
# has nothing new staged -- it must skip its commit/push instead of `git
# commit` erroring on an empty index, and the single commit from the first
# call still reaches the remote branch.
@test "driver commits then forgets its outcome twice -> commit survives both calls, backstop fires once" {
  export FAKE_CLAUDE_COMMIT=1
  export FAKE_CLAUDE_NO_OUTCOME=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=blocked' <<<"$output"
  [ "$(grep -c '^claude invoked for issue' "$CLAUDE_LOG")" -eq 2 ]
  git -C "$BATS_TEST_TMPDIR" ls-remote "https://github.com/owner/repo.git" "agent/issue-7" | grep -q .
}

# The resume pass resumes the same pinned session (mode="resume"), not a
# cold one -- with the transcript a real Driver would already have written
# present, the resumed call's argv carries --resume <id>, never a second
# --session-id.
@test "the resume pass targets the pinned session via --resume" {
  local id
  id="$(pinned_session_id)"
  mkdir -p "$HOME/.claude/projects/fake-project"
  touch "$HOME/.claude/projects/fake-project/${id}.jsonl"

  export FAKE_CLAUDE_NO_OUTCOME_FIRST_CALL_ONLY=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c -- "--session-id ${id}" "$CLAUDE_LOG")" -eq 1 ]
  [ "$(grep -c -- "--resume ${id}" "$CLAUDE_LOG")" -eq 1 ]
}

# A driver killed by a transient infrastructure failure exits non-zero with
# no outcome line -- that's the launcher's ClassifyTransient/retry path to
# handle (issue #593), not this recovery. No resume pass should run.
@test "driver crashes non-zero with no outcome -> no resume attempted" {
  export FAKE_CLAUDE_NO_OUTCOME=1
  export FAKE_CLAUDE_CRASH_EXIT=17
  run bash "$ENTRYPOINT"
  [ "$status" -eq 17 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 0 ]
  [ "$(grep -c '^claude invoked for issue' "$CLAUDE_LOG")" -eq 1 ]
}

# A near-miss outcome (the SPINDRIFT_OUTCOME token present but the line
# unparseable, e.g. "SPINDRIFT_OUTCOME: SUCCESS") on the first call must make
# the single corrective resume quote that offending text back and restate the
# grammar/allowed status values (issue #1900), rather than the generic nudge.
# The resumed call then emits a real outcome, so the run settles on it.
@test "near-miss outcome -> resume prompt quotes the offending line and restates the grammar" {
  export FAKE_CLAUDE_NEAR_MISS_FIRST_CALL_ONLY=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=https://github.com/owner/repo/pull/1 status=ready note=fake$' <<<"$output"
  # Exactly two Driver invocations: the initial near-miss pass and one resume.
  [ "$(grep -c '^claude invoked for issue' "$CLAUDE_LOG")" -eq 2 ]
  # The resume pass's rendered prompt (the last one written) quotes the
  # offending near-miss text verbatim and restates the grammar + status values.
  grep -q 'SPINDRIFT_OUTCOME: SUCCESS' "$CLAUDE_PROMPT_FILE"
  grep -q 'valid status values are ready and blocked' "$CLAUDE_PROMPT_FILE"
}

# A near-miss on every call: the single corrective resume still fires exactly
# once (quoting the offending line), and because the resumed pass also leaves
# only a near-miss, the run falls through to the synthetic status=blocked
# backstop unchanged -- proving the near-miss path loops no more than the bare
# absence path does (issue #1900).
@test "near-miss outcome on every call -> resumes once then falls through to the backstop" {
  export FAKE_CLAUDE_NEAR_MISS=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=blocked' <<<"$output"
  # Exactly two Driver invocations: the initial pass and the one resume pass.
  [ "$(grep -c '^claude invoked for issue' "$CLAUDE_LOG")" -eq 2 ]
  # The one resume it did fire quoted the offending near-miss text.
  grep -q 'SPINDRIFT_OUTCOME: SUCCESS' "$CLAUDE_PROMPT_FILE"
}
