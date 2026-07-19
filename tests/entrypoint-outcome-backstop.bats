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
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=blocked note=.*push failed.*simulated push failure' <<<"$output"
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
