#!/usr/bin/env bats
# Entrypoint backstop (issue #593): a driver that exits without a parseable
# SPINDRIFT_OUTCOME line must not leave the launcher with a silent gap. The
# entrypoint pushes whatever is committed on the branch best-effort, then emits
# exactly one synthetic outcome line.

load helper

setup() {
  setup_entrypoint_env
}

@test "driver exits with no outcome line -> entrypoint emits a synthetic ready outcome" {
  export FAKE_DRIVER_COMMIT=1
  export FAKE_DRIVER_NO_OUTCOME=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=.*driver exited without emitting an outcome' <<<"$output"
  git -C "$BATS_TEST_TMPDIR" ls-remote "https://github.com/owner/repo.git" "agent/issue-7" | grep -q .
}

# The Driver died before its first commit (#1606): the backstop must not
# force-push a branch byte-identical to main, and the note must say so rather
# than claim a push happened.
@test "driver exits with no commits and no outcome line -> no push, note says no work to preserve" {
  export FAKE_DRIVER_NO_OUTCOME=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=blocked note=.*no work to preserve' <<<"$output"
  # No commits beyond main means nothing to push, so the branch must never
  # reach the remote.
  run git -C "$BATS_TEST_TMPDIR" ls-remote "https://github.com/owner/repo.git" "agent/issue-7"
  [ -z "$output" ]
}

# The Driver staged real work but never committed it (issue #2012, the #1998
# dogfood shape). commit_count alone reads 0 here, same as the genuinely-empty
# case above, but a dirty index is not "no work to preserve": the backstop must
# salvage it into a commit and push it.
@test "driver stages work but never commits + no outcome line -> backstop salvages a commit and pushes" {
  export FAKE_DRIVER_STAGE_ONLY=1
  export FAKE_DRIVER_NO_OUTCOME=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=.*salvaged uncommitted work' <<<"$output"
  ! grep -q 'no work to preserve' <<<"$output"
  git -C "$BATS_TEST_TMPDIR" ls-remote "https://github.com/owner/repo.git" "agent/issue-7" | grep -q .
}

# A misconfigured negative backoff/jitter must not make the retry loop's
# `sleep` reject its argument and abort emit_outcome_backstop under `set -e`
# before the always-emit outcome line (#593). Both are clamped to zero, so the
# bounded retry loop still runs to exhaustion.
@test "negative backoff/jitter is clamped so the backstop still emits an outcome" {
  local real_git
  real_git="$(command -v git)"
  local shim="$BATS_TEST_TMPDIR/gitshim"
  mkdir -p "$shim"
  cat >"$shim/git" <<EOF
#!$BASH
if [ "\$1" = "push" ] && [ "\$2" = "--force-with-lease" ] && [ "\$3" = "origin" ]; then
  echo "! [rejected] simulated push failure" >&2
  exit 1
fi
exec "$real_git" "\$@"
EOF
  chmod +x "$shim/git"
  export PATH="$shim:$PATH"

  export FAKE_DRIVER_COMMIT=1
  export FAKE_DRIVER_NO_OUTCOME=1
  # Without the clamp these reach `sleep -7` on the first retry and, under
  # `set -e`, abort before the synthetic outcome line ever prints.
  export TRANSIENT_BACKOFF_SECS=-5
  export HOLD_JITTER_SECS=-2
  export MAX_REBASE_ATTEMPTS=2
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=blocked note=.*push failed' <<<"$output"
}

@test "driver exits with its own outcome line -> passed through, no synthetic line appended" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=https://github.com/owner/repo/pull/1 status=ready note=fake$' <<<"$output"
}

# The shim fails the backstop's exact call, `git push --force-with-lease
# origin agent/issue-7`, and passes every other git invocation through.
@test "push failure during the backstop is reflected in the outcome note" {
  local real_git
  real_git="$(command -v git)"
  local shim="$BATS_TEST_TMPDIR/gitshim"
  mkdir -p "$shim"
  # The shebang is this bash's own absolute path ($BASH), not /usr/bin/env: a
  # sandboxed nix build has no /usr/bin/env (the same reason bats.nix rewrites
  # tests/fakes/* shebangs at build time), and nix substitution never sees this
  # shim because it is generated at test run time.
  cat >"$shim/git" <<EOF
#!$BASH
if [ "\$1" = "push" ] && [ "\$2" = "--force-with-lease" ] && [ "\$3" = "origin" ]; then
  echo "! [rejected] simulated push failure" >&2
  exit 1
fi
exec "$real_git" "\$@"
EOF
  chmod +x "$shim/git"
  export PATH="$shim:$PATH"

  # A commit must exist on the branch, or the no-work skip (#1606) would
  # short-circuit before ever reaching this shimmed push.
  export FAKE_DRIVER_COMMIT=1
  export FAKE_DRIVER_NO_OUTCOME=1
  # The shim fails every push attempt, so the backstop's bounded retry loop
  # (issue #2095) runs to exhaustion. Zero these out so the test does not
  # sleep through the linear backoff.
  export TRANSIENT_BACKOFF_SECS=0
  export HOLD_JITTER_SECS=0
  export MAX_REBASE_ATTEMPTS=2
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=blocked note=.*push failed.*simulated push failure' <<<"$output"
}

# A push that fails once but succeeds on retry (a transient 403/5xx/network
# blip) must not be treated as a terminal failure (issue #2095): the backstop
# retries within its bounded attempt cap, so the retried push reaches the
# remote and the note carries no "push failed" text.
@test "transient push failure recovers on retry during the backstop -> pushed, no failure note" {
  local real_git
  real_git="$(command -v git)"
  local shim="$BATS_TEST_TMPDIR/gitshim"
  mkdir -p "$shim"
  local counter="$BATS_TEST_TMPDIR/push-attempts"
  # The shebang is $BASH, not /usr/bin/env, for the reason the shim above
  # gives: a sandboxed nix build has no /usr/bin/env.
  cat >"$shim/git" <<EOF
#!$BASH
if [ "\$1" = "push" ] && [ "\$2" = "--force-with-lease" ] && [ "\$3" = "origin" ]; then
  count=0
  [ -f "$counter" ] && count="\$(cat "$counter")"
  count=\$(( count + 1 ))
  echo "\$count" >"$counter"
  if [ "\$count" -eq 1 ]; then
    echo "! [rejected] simulated transient push failure" >&2
    exit 1
  fi
fi
exec "$real_git" "\$@"
EOF
  chmod +x "$shim/git"
  export PATH="$shim:$PATH"

  export FAKE_DRIVER_COMMIT=1
  export FAKE_DRIVER_NO_OUTCOME=1
  export TRANSIENT_BACKOFF_SECS=0
  export HOLD_JITTER_SECS=0
  export MAX_REBASE_ATTEMPTS=3
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=.*driver exited without emitting an outcome' <<<"$output"
  ! grep -q 'push failed' <<<"$output"
  git -C "$BATS_TEST_TMPDIR" ls-remote "https://github.com/owner/repo.git" "agent/issue-7" | grep -q .
}

# A driver killed by a transient infrastructure failure also exits non-zero
# with no outcome line, but the launcher's own ClassifyTransient/retry path
# (cmd/launcher/internal/dispatch) owns that case and only runs on a non-zero
# container exit. The backstop must not swallow that exit under a synthetic
# status=blocked, turning a retryable failure into a terminal one.
@test "driver crashes non-zero with no outcome -> non-zero exit propagates, no synthetic line" {
  export FAKE_DRIVER_NO_OUTCOME=1
  export FAKE_DRIVER_CRASH_EXIT=17
  run bash "$ENTRYPOINT"
  [ "$status" -eq 17 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 0 ]
}

# The no-outcome backstop no longer branches on draft-ness (issue #1654): a
# non-draft PR on BRANCH is not a salvage signal that the Driver reached
# status=ready and merely lost the line, and the launcher's own no-outcome
# path never reads draft-ness either, so both sides agree.
@test "no outcome line + open non-draft PR on branch -> synthetic ready" {
  export FAKE_DRIVER_COMMIT=1
  export FAKE_DRIVER_NO_OUTCOME=1
  export FAKE_GH_PR_LIST_7='[{"isDraft":false}]'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=.*driver exited without emitting an outcome' <<<"$output"
}

# A non-draft PR is not a salvage signal (issue #1654): the backstop still
# synthesizes status=blocked exactly as when no PR exists, even with zero local
# commits ahead of base (this Box resumed a session whose transcript is gone
# but whose branch another process advanced). The no-work-to-preserve early
# return (#1606) skips the push, not the synthesized outcome line.
@test "no outcome line + no commits + open non-draft PR on branch -> synthetic blocked" {
  export FAKE_DRIVER_NO_OUTCOME=1
  export FAKE_GH_PR_LIST_7='[{"isDraft":false}]'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=blocked note=.*no work to preserve' <<<"$output"
}

@test "no outcome line + draft PR on branch -> synthetic ready, same as no PR" {
  export FAKE_DRIVER_COMMIT=1
  export FAKE_DRIVER_NO_OUTCOME=1
  export FAKE_GH_PR_LIST_7='[{"isDraft":true}]'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=.*driver exited without emitting an outcome' <<<"$output"
}

# End-to-end regression for the #1582 shape: the driver's own outcome line was
# backtick-wrapped (issue #1611's repro of the same dogfood run) and there is a
# ready PR on the branch. #1611 made the extractor tolerate the wrapping, so
# the real status=ready line surfaces and the backstop never runs. The
# guarantee: no synthetic status=blocked line appears alongside a ready PR.
@test "markdown-mangled outcome line (#1582) + open non-draft PR -> no synthetic blocked line" {
  export FAKE_DRIVER_WRAP_OUTCOME=backticks
  export FAKE_GH_PR_LIST_7='[{"isDraft":false}]'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  ! grep -q 'status=blocked' <<<"$output"
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=https://github.com/owner/repo/pull/1 status=ready' <<<"$output"
}

# Regression for the #1998 dogfood shape (issue #2012): the driver's outcome
# line was well-formed but used a colon instead of the required space after the
# token. That is machine-recoverable, so the extractor normalizes the delimiter
# and the backstop never runs.
@test "colon-delimited outcome line (#2012) -> extractor salvages it, no synthetic blocked line" {
  export FAKE_DRIVER_WRAP_OUTCOME=colon
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  ! grep -q 'status=blocked' <<<"$output"
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=https://github.com/owner/repo/pull/1 status=ready note=fake$' <<<"$output"
}

# End-to-end regression for the #1998 dogfood shape that opened issue #2012: a
# sign-off with no issue=/landing=/status= fields at all is not
# machine-recoverable, so the extractor leaves it unmatched. The Driver's
# staged implementation must still survive: the backstop salvages the dirty
# index into a commit and pushes it instead of discarding it.
@test "prose-only outcome sign-off (#2012, #1998 shape) + staged work -> backstop salvages the work" {
  export FAKE_DRIVER_STAGE_ONLY=1
  export FAKE_DRIVER_WRAP_OUTCOME=prose
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=.*salvaged uncommitted work' <<<"$output"
  ! grep -q 'no work to preserve' <<<"$output"
  git -C "$BATS_TEST_TMPDIR" ls-remote "https://github.com/owner/repo.git" "agent/issue-7" | grep -q .
}

# Issue #2011: a Driver that backgrounds work or parks its turn awaiting a
# subagent notification a headless `claude -p` session can never receive ends
# its turn with no SPINDRIFT_OUTCOME line and rc=0, so it must land on this
# same backstop whichever invoker ran it. CLAUDE_CODE_DISABLE_BACKGROUND_TASKS
# closes the parking path itself; this test is the last-resort net.
@test "orchestrator path: driver parks with no outcome line -> entrypoint still emits a synthetic ready outcome" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  export FAKE_DRIVER_COMMIT=1
  export FAKE_DRIVER_NO_OUTCOME=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=.*driver exited without emitting an outcome' <<<"$output"
  git -C "$BATS_TEST_TMPDIR" ls-remote "https://github.com/owner/repo.git" "agent/issue-7" | grep -q .
}

# A prose sign-off can carry one stray field-marker word mid-sentence without
# becoming a genuine key=value line (issue #2012): the extractor must require
# landing= and status= together, or this surfaces as a synthetic outcome and
# skips the backstop. With no commits or staged work, the plain "no work to
# preserve" backstop firing is itself proof the stray marker was rejected.
@test "prose with one stray field marker (#2012) -> extractor still leaves it unmatched" {
  export FAKE_DRIVER_WRAP_OUTCOME=stray-field
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=blocked note=.*no work to preserve' <<<"$output"
}

# Issue #2094: a read-only github Box (BOX_WRITE_ENABLED unset) holds no push
# token by design, so the backstop's `git push --force-with-lease` could only
# ever 403, a structural failure rather than a transient one. The branch must
# instead be relayed via the harness-owned bundle-out step (issues
# #1808/#2082), the same outbox seam.bundle a read-only hand-off uses.
@test "read-only github + no outcome line -> branch relayed via outbox bundle, no force-push" {
  unset BOX_WRITE_ENABLED     # read-only Box: no push token
  unset CODE_FORGE
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"
  export FAKE_DRIVER_COMMIT=1
  export FAKE_DRIVER_NO_OUTCOME=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # This fixture (read-only + github + backstop-derived status=ready) is also
  # the PR-intent nudge gate's trigger condition (issue #2448), and
  # FAKE_DRIVER_NO_OUTCOME supplies no PR-intent marker, so that nudge fires.
  # Its resumed pass carries no SPINDRIFT_OUTCOME token at all, so nothing
  # shadowed the original line and the gate's fallback must not reprint it.
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=.*relayed via outbox bundle' <<<"$output"
  ! grep -q 'push failed' <<<"$output"
  [ -f "$OUTBOX_DIR/seam.bundle" ]
  run git -C "$WORK_DIR" bundle verify "$OUTBOX_DIR/seam.bundle"
  [ "$status" -eq 0 ]
  # A read-only Box holds no push token, so the branch must never reach the
  # remote.
  run git -C "$BATS_TEST_TMPDIR" ls-remote "https://github.com/owner/repo.git" "agent/issue-7"
  [ -z "$output" ]
}

# CODE_FORGE=local's no-writable-remote note (issue #1808) now falls through to
# the same harness-owned bundle-out step every other status=blocked-with-commits
# path uses (ADR 0039 slice S1, issue #2252): a real bundle is written even
# though the note still says there is no writable remote to push to directly.
@test "CODE_FORGE=local + no outcome line -> bundle relayed via outbox, no-writable-remote note" {
  export CODE_FORGE=local
  export BOX_HOST_MEDIATED_REMOTE=1
  export REPO_MOUNT_DIR="$REMOTE_ROOT/owner/repo.git"
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"
  export FAKE_DRIVER_COMMIT=1
  export FAKE_DRIVER_NO_OUTCOME=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=.*branch relayed via outbox bundle.*no writable remote under CODE_FORGE=local' <<<"$output"
  [ -f "$OUTBOX_DIR/seam.bundle" ]
  run git -C "$WORK_DIR" bundle verify "$OUTBOX_DIR/seam.bundle"
  [ "$status" -eq 0 ]
}
