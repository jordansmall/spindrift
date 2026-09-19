#!/usr/bin/env bats
# Resume-once recovery (issue #1607): a Driver that exits 0 with no
# SPINDRIFT_OUTCOME line usually ended its turn early rather than failing
# (issue #1542). Before the synthetic status=blocked backstop (issue #593),
# the entrypoint resumes the same pinned session exactly once with a nudge.

load helper

setup() {
  setup_entrypoint_env
}

# This repeats the deterministic id formula in lib/drivers/claude.nix's
# sessionFlagsFnBody, so a test can pre-seed the transcript that the initial
# run's --session-id pins without two separate ENTRYPOINT invocations.
pinned_session_id() {
  local h
  h="$(printf '%s' "spindrift-session:${REPO_SLUG:-}:${ISSUE_NUMBER:-}" | sha256sum | cut -c1-32)"
  printf '%s-%s-%s-%s-%s' "${h:0:8}" "${h:8:4}" "${h:12:4}" "${h:16:4}" "${h:20:12}"
}

@test "driver exits with no outcome -> resume pass emits an outcome, no synthetic backstop" {
  export FAKE_DRIVER_NO_OUTCOME_FIRST_CALL_ONLY=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=https://github.com/owner/repo/pull/1 status=ready note=fake$' <<<"$output"
  # Two invocations mean the initial pass and exactly one resume pass.
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 2 ]
}

@test "driver exits with no outcome, resume also emits none -> synthetic backstop notes the recovery attempt" {
  export FAKE_DRIVER_NO_OUTCOME=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=blocked note=.*driver exited without emitting an outcome.*resume attempt also produced no outcome' <<<"$output"
  # Two invocations mean the initial pass and exactly one resume pass.
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 2 ]
}

# The fake commits identical fixture content on every call, so the resume pass
# has nothing new staged. It must skip its commit and push instead of letting
# `git commit` error on an empty index, and the first call's commit must still
# reach the remote branch.
@test "driver commits then forgets its outcome twice -> commit survives both calls, backstop fires once" {
  export FAKE_DRIVER_COMMIT=1
  export FAKE_DRIVER_NO_OUTCOME=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready' <<<"$output"
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 2 ]
  git -C "$BATS_TEST_TMPDIR" ls-remote "https://github.com/owner/repo.git" "agent/issue-7" | grep -q .
}

# The resume pass resumes the same pinned session rather than starting a cold
# one, so with the transcript present its argv carries --resume <id> and never
# a second --session-id.
@test "the resume pass targets the pinned session via --resume" {
  if [ -z "${DRIVER_SESSION_RESUMABLE:-}" ]; then
    skip "driver has no resumable session state"
  fi

  local id
  id="$(pinned_session_id)"
  mkdir -p "$HOME/.claude/projects/fake-project"
  touch "$HOME/.claude/projects/fake-project/${id}.jsonl"

  export FAKE_DRIVER_NO_OUTCOME_FIRST_CALL_ONLY=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c -- "--session-id ${id}" "$DRIVER_LOG")" -eq 1 ]
  [ "$(grep -c -- "--resume ${id}" "$DRIVER_LOG")" -eq 1 ]
}

# A non-zero exit with no outcome line is the launcher's ClassifyTransient and
# retry path (issue #593), not this recovery, so no resume pass should run.
@test "driver crashes non-zero with no outcome -> no resume attempted" {
  export FAKE_DRIVER_NO_OUTCOME=1
  export FAKE_DRIVER_CRASH_EXIT=17
  run bash "$ENTRYPOINT"
  [ "$status" -eq 17 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 0 ]
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 1 ]
}

# A near-miss is the SPINDRIFT_OUTCOME token present on an unparseable line,
# e.g. "SPINDRIFT_OUTCOME: SUCCESS". The single corrective resume must quote
# that offending text back and restate the grammar and allowed status values
# (issue #1900) rather than send the generic nudge.
@test "near-miss outcome -> resume prompt quotes the offending line and restates the grammar" {
  export FAKE_DRIVER_NEAR_MISS_FIRST_CALL_ONLY=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=https://github.com/owner/repo/pull/1 status=ready note=fake$' <<<"$output"
  # Two invocations mean the initial near-miss pass and one resume pass.
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 2 ]
  # DRIVER_PROMPT_FILE holds the last prompt written, which is the resume pass's.
  grep -q 'SPINDRIFT_OUTCOME: SUCCESS' "$DRIVER_PROMPT_FILE"
  grep -q 'valid status values are ready, blocked, or ambiguous' "$DRIVER_PROMPT_FILE"
  # The example line substitutes the real issue and landing values, leaving
  # only status and note as placeholders (issue #2449).
  grep -q 'SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=' "$DRIVER_PROMPT_FILE"
}

# A near-miss on every call still fires exactly one corrective resume and then
# falls through to the synthetic status=blocked backstop, so the near-miss path
# loops no more than the bare absence path does (issue #1900).
@test "near-miss outcome on every call -> resumes once then falls through to the backstop" {
  export FAKE_DRIVER_NEAR_MISS=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=blocked' <<<"$output"
  # Two invocations mean the initial pass and exactly one resume pass.
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 2 ]
  grep -q 'SPINDRIFT_OUTCOME: SUCCESS' "$DRIVER_PROMPT_FILE"
  # The example line substitutes the real issue and landing values, leaving
  # only status and note as placeholders (issue #2449).
  grep -q 'SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=' "$DRIVER_PROMPT_FILE"
}
