#!/usr/bin/env bats
# Entrypoint backstop (issue #593): a driver that exits without printing a
# parseable SPINDRIFT_OUTCOME line must not leave the launcher with a silent
# gap. The entrypoint pushes whatever work is committed on the branch
# best-effort, then emits exactly one synthetic status=blocked outcome line.

load helper

setup() {
  setup_entrypoint_env
}

# The fake claude commits work (so there is something to push) but is told to
# suppress its own outcome line, simulating a driver that forgot to emit one.
@test "driver exits with no outcome line -> entrypoint emits a synthetic blocked outcome" {
  export FAKE_CLAUDE_COMMIT=1
  export FAKE_CLAUDE_NO_OUTCOME=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=blocked note=.*driver exited without emitting an outcome' <<<"$output"
  # The commit the fake driver made must have reached the remote branch.
  git -C "$BATS_TEST_TMPDIR" ls-remote "https://github.com/owner/repo.git" "agent/issue-7" | grep -q .
}

# No commits landed on the branch (the Driver died before its first commit,
# #1606) -- the backstop must not force-push a branch byte-identical to
# main, and the note must say so rather than claim a push happened.
@test "driver exits with no commits and no outcome line -> no push, note says no work to preserve" {
  export FAKE_CLAUDE_NO_OUTCOME=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=blocked note=.*no work to preserve' <<<"$output"
  # No commits beyond main means nothing to push -- the branch must never
  # reach the remote.
  run git -C "$BATS_TEST_TMPDIR" ls-remote "https://github.com/owner/repo.git" "agent/issue-7"
  [ -z "$output" ]
}

# The Driver completed real work and staged it, but never committed (issue
# #2012's #1998 dogfood shape: the resume pass's implementation was left
# staged-but-uncommitted). commit_count alone would read 0 here, same as the
# genuinely-empty case above -- but a dirty index is not "no work to
# preserve": the backstop must salvage it into a commit and push it.
@test "driver stages work but never commits + no outcome line -> backstop salvages a commit and pushes" {
  export FAKE_CLAUDE_STAGE_ONLY=1
  export FAKE_CLAUDE_NO_OUTCOME=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=blocked note=.*salvaged uncommitted work' <<<"$output"
  ! grep -q 'no work to preserve' <<<"$output"
  git -C "$BATS_TEST_TMPDIR" ls-remote "https://github.com/owner/repo.git" "agent/issue-7" | grep -q .
}

# A misconfigured negative backoff/jitter must not make the retry loop's
# `sleep "$(( backoff * attempt + jitter ))"` reject its argument and abort
# emit_outcome_backstop under `set -e` before the always-emit outcome line
# (#593): both are clamped to zero, so the bounded retry loop still runs to
# exhaustion and exactly one synthetic outcome line is emitted.
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

  export FAKE_CLAUDE_COMMIT=1
  export FAKE_CLAUDE_NO_OUTCOME=1
  # Negative values would otherwise reach `sleep -7` on the first retry and,
  # under `set -e`, abort before the synthetic outcome line ever prints.
  export TRANSIENT_BACKOFF_SECS=-5
  export HOLD_JITTER_SECS=-2
  export MAX_REBASE_ATTEMPTS=2
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=blocked note=.*push failed' <<<"$output"
}

# A driver that already printed its own outcome is passed through unchanged --
# no second/synthetic line is appended.
@test "driver exits with its own outcome line -> passed through, no synthetic line appended" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=https://github.com/owner/repo/pull/1 status=ready note=fake$' <<<"$output"
}

# A best-effort push failure during the backstop must be surfaced in the
# outcome note, not swallowed. Shims `git push --force-with-lease origin
# agent/issue-7` (the backstop's exact call) to fail, while every other git
# invocation -- clone, checkout, rebase -- passes through to the real git
# untouched.
@test "push failure during the backstop is reflected in the outcome note" {
  local real_git
  real_git="$(command -v git)"
  local shim="$BATS_TEST_TMPDIR/gitshim"
  mkdir -p "$shim"
  # Shebang is this running bash's own absolute path ($BASH), not
  # /usr/bin/env -- a sandboxed nix build has no /usr/bin/env (same reason
  # bats.nix rewrites tests/fakes/* shebangs at build time), and this shim is
  # generated at test run time so nix substitution never sees it.
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

  # A commit must exist on the branch, or the new no-work skip (#1606) would
  # short-circuit before ever reaching this shimmed push.
  export FAKE_CLAUDE_COMMIT=1
  export FAKE_CLAUDE_NO_OUTCOME=1
  # This shim fails every push attempt, so the backstop's bounded retry loop
  # (issue #2095) runs to exhaustion -- zero these out so the test doesn't
  # actually sleep through the linear backoff.
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
# retries within its bounded attempt cap, the retried push reaches the
# remote, and the outcome note carries no "push failed" text at all.
@test "transient push failure recovers on retry during the backstop -> pushed, no failure note" {
  local real_git
  real_git="$(command -v git)"
  local shim="$BATS_TEST_TMPDIR/gitshim"
  mkdir -p "$shim"
  local counter="$BATS_TEST_TMPDIR/push-attempts"
  # Shebang is this running bash's own absolute path ($BASH), not
  # /usr/bin/env -- a sandboxed nix build has no /usr/bin/env (same reason
  # bats.nix rewrites tests/fakes/* shebangs at build time), and this shim is
  # generated at test run time so nix substitution never sees it.
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

  export FAKE_CLAUDE_COMMIT=1
  export FAKE_CLAUDE_NO_OUTCOME=1
  export TRANSIENT_BACKOFF_SECS=0
  export HOLD_JITTER_SECS=0
  export MAX_REBASE_ATTEMPTS=3
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=blocked note=.*driver exited without emitting an outcome' <<<"$output"
  ! grep -q 'push failed' <<<"$output"
  git -C "$BATS_TEST_TMPDIR" ls-remote "https://github.com/owner/repo.git" "agent/issue-7" | grep -q .
}

# A driver killed by a transient infrastructure failure (rate limit,
# overload, network) exits non-zero with no outcome line either -- but that
# case is NOT this backstop's to handle: the launcher's own
# ClassifyTransient/retry path (cmd/launcher/internal/dispatch) already owns
# it, and only runs when the container's own exit code is non-zero. The
# backstop must not swallow that non-zero exit under a synthetic
# status=blocked, which would silently turn a retryable transient failure
# into a terminal one.
@test "driver crashes non-zero with no outcome -> non-zero exit propagates, no synthetic line" {
  export FAKE_CLAUDE_NO_OUTCOME=1
  export FAKE_CLAUDE_CRASH_EXIT=17
  run bash "$ENTRYPOINT"
  [ "$status" -eq 17 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 0 ]
}

# The no-outcome backstop no longer branches on draft-ness (issue #1654): a
# non-draft PR on BRANCH is no longer treated as a salvage signal that the
# Driver reached status=ready and merely lost the line -- the launcher's own
# no-outcome path never adopts off draft-ness either, so both sides agree a
# lost outcome line always synthesizes status=blocked.
@test "no outcome line + open non-draft PR on branch -> synthetic blocked" {
  export FAKE_CLAUDE_COMMIT=1
  export FAKE_CLAUDE_NO_OUTCOME=1
  export FAKE_GH_PR_LIST_7='[{"isDraft":false}]'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=blocked note=.*driver exited without emitting an outcome' <<<"$output"
}

# A non-draft PR is not a salvage signal (issue #1654) -- the backstop must
# still synthesize status=blocked exactly as it does when no PR exists at
# all, even with zero local commits ahead of base (e.g. this Box resumed a
# session whose transcript is gone but whose branch/PR another process
# already advanced): the no-work-to-preserve early return (#1606) skips the
# push, not the synthesized outcome line.
@test "no outcome line + no commits + open non-draft PR on branch -> synthetic blocked" {
  export FAKE_CLAUDE_NO_OUTCOME=1
  export FAKE_GH_PR_LIST_7='[{"isDraft":false}]'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=blocked note=.*no work to preserve' <<<"$output"
}

@test "no outcome line + draft PR on branch -> synthetic blocked, same as no PR" {
  export FAKE_CLAUDE_COMMIT=1
  export FAKE_CLAUDE_NO_OUTCOME=1
  export FAKE_GH_PR_LIST_7='[{"isDraft":true}]'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=blocked note=.*driver exited without emitting an outcome' <<<"$output"
}

# Regression for the #1582 shape end-to-end: the driver's own outcome line
# was backtick-wrapped (FAKE_CLAUDE_WRAP_OUTCOME=backticks, issue #1611's
# repro of the same dogfood run), and there is a ready PR on the branch.
# #1611 already made the extractor tolerate the wrapping, so the real
# status=ready line surfaces and this backstop never even runs -- but the
# combined, end-to-end guarantee this issue adds is what matters: no
# synthetic status=blocked line ever appears alongside a ready PR.
@test "markdown-mangled outcome line (#1582) + open non-draft PR -> no synthetic blocked line" {
  export FAKE_CLAUDE_WRAP_OUTCOME=backticks
  export FAKE_GH_PR_LIST_7='[{"isDraft":false}]'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  ! grep -q 'status=blocked' <<<"$output"
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=https://github.com/owner/repo/pull/1 status=ready' <<<"$output"
}

# Regression for the #1998 dogfood shape (issue #2012): the driver's own
# outcome line was well-formed but used a colon instead of the required space
# delimiter after the token. That is genuinely machine-recoverable -- the
# extractor normalizes the delimiter and surfaces it, so the backstop never
# runs at all.
@test "colon-delimited outcome line (#2012) -> extractor salvages it, no synthetic blocked line" {
  export FAKE_CLAUDE_WRAP_OUTCOME=colon
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  ! grep -q 'status=blocked' <<<"$output"
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=https://github.com/owner/repo/pull/1 status=ready note=fake$' <<<"$output"
}

# End-to-end regression for the exact #1998 dogfood shape that opened issue
# #2012: a sign-off with no issue=/landing=/status= fields at all ("Complete —
# implemented issue #1998 …") is genuinely not machine-recoverable, so the
# extractor correctly leaves it unmatched -- but the Driver's staged
# implementation must still survive: the backstop it falls through to salvages
# the dirty index into a commit and pushes it, rather than discarding it as
# "no work to preserve".
@test "prose-only outcome sign-off (#2012, #1998 shape) + staged work -> backstop salvages the work" {
  export FAKE_CLAUDE_STAGE_ONLY=1
  export FAKE_CLAUDE_WRAP_OUTCOME=prose
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=blocked note=.*salvaged uncommitted work' <<<"$output"
  ! grep -q 'no work to preserve' <<<"$output"
  git -C "$BATS_TEST_TMPDIR" ls-remote "https://github.com/owner/repo.git" "agent/issue-7" | grep -q .
}

# A prose sign-off can contain one stray field-marker word mid-sentence
# without becoming a genuine key=value line (issue #2012) -- the extractor
# must require landing= and status= together, not treat any single marker as
# proof of a well-formed line, or this would surface as a synthetic outcome
# and skip the resume/backstop safety net over a line the launcher's own
# outcome.Parse would still reject as a near miss (missing landing=). No
# commits or staged work here, so a plain "no work to preserve" backstop
# firing is itself the proof the extractor rejected the stray marker.
# Issue #2011: a Driver that backgrounds work or parks its turn awaiting a
# subagent notification a headless `claude -p` session can never receive
# ends its turn the same way any other forgetful driver does -- no
# SPINDRIFT_OUTCOME line, rc=0 -- so it must land on this same backstop
# regardless of which invoker (driver-exec directly, or the orchestrator)
# ran the Driver. FAKE_CLAUDE_NO_OUTCOME stands in for that parked turn:
# CLAUDE_CODE_DISABLE_BACKGROUND_TASKS (this issue's structural fix) closes
# the parking vector itself, but this test is the last-resort net for
# whatever still gets through it -- a run must never exit silently.
@test "orchestrator path: driver parks with no outcome line -> entrypoint still emits a synthetic blocked outcome" {
  export ORCHESTRATOR_ENABLED=1
  export FAKE_CLAUDE_COMMIT=1
  export FAKE_CLAUDE_NO_OUTCOME=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=blocked note=.*driver exited without emitting an outcome' <<<"$output"
  git -C "$BATS_TEST_TMPDIR" ls-remote "https://github.com/owner/repo.git" "agent/issue-7" | grep -q .
}

@test "prose with one stray field marker (#2012) -> extractor still leaves it unmatched" {
  export FAKE_CLAUDE_WRAP_OUTCOME=stray-field
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=blocked note=.*no work to preserve' <<<"$output"
}

# Issue #2094: a read-only github Box (BOX_WRITE_ENABLED unset) holds no push
# token by design, so the backstop's own `git push --force-with-lease` here
# could only ever 403 -- a structural failure, not a transient one. Instead
# the branch must be relayed via the harness-owned bundle-out step (issue
# #1808/#2082), the same outbox seam.bundle a read-only status=ready hand-off
# already uses.
@test "read-only github + no outcome line -> branch relayed via outbox bundle, no force-push" {
  unset BOX_WRITE_ENABLED     # read-only Box: no push token
  unset CODE_FORGE            # default github
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"
  export FAKE_CLAUDE_COMMIT=1
  export FAKE_CLAUDE_NO_OUTCOME=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=blocked note=.*relayed via outbox bundle' <<<"$output"
  ! grep -q 'push failed' <<<"$output"
  # The branch was relayed through the outbox bundle, never force-pushed.
  [ -f "$OUTBOX_DIR/seam.bundle" ]
  run git -C "$WORK_DIR" bundle verify "$OUTBOX_DIR/seam.bundle"
  [ "$status" -eq 0 ]
  # A read-only Box holds no push token -- the branch must never reach the remote.
  run git -C "$BATS_TEST_TMPDIR" ls-remote "https://github.com/owner/repo.git" "agent/issue-7"
  [ -z "$output" ]
}

# CODE_FORGE=local's read-only-style no-writable-remote note (issue #1808)
# must stay exactly as it was before this change: no bundle is written on
# this no-outcome backstop path (only a genuine status=ready claim triggers
# the harness-owned bundle-out step at the bottom of main()).
@test "CODE_FORGE=local + no outcome line -> unchanged: no bundle, no-writable-remote note" {
  export CODE_FORGE=local
  export REPO_MOUNT_DIR="$REMOTE_ROOT/owner/repo.git"
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"
  export FAKE_CLAUDE_COMMIT=1
  export FAKE_CLAUDE_NO_OUTCOME=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=blocked note=.*no writable remote under CODE_FORGE=local' <<<"$output"
  [ ! -f "$OUTBOX_DIR/seam.bundle" ]
}
