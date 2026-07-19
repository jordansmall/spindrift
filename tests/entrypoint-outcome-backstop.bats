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

# Adoption-aware backstop (issue #1614): an open, non-draft PR on BRANCH is a
# real, mergeable result even without a parsed outcome line -- the backstop
# must stay silent so the launcher's own no-outcome adoption path (discover
# PR -> self-heal merge gate -> merge) settles the run instead of a synthetic
# line preempting it.
@test "no outcome line + open non-draft PR on branch -> backstop stays silent" {
  export FAKE_CLAUDE_NO_OUTCOME=1
  export FAKE_GH_PR_LIST_7='[{"isDraft":false}]'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 0 ]
}

# A draft PR is not a done, mergeable result -- the backstop must still
# synthesize status=blocked exactly as it does when no PR exists at all.
@test "no outcome line + draft PR on branch -> synthetic blocked, same as no PR" {
  export FAKE_CLAUDE_NO_OUTCOME=1
  export FAKE_GH_PR_LIST_7='[{"isDraft":true}]'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=blocked note=.*driver exited without emitting an outcome' <<<"$output"
}

# Regression for the #1582 shape: the driver DID print its outcome line, but
# backtick-wrapped so the extractor's anchored grep misses it exactly as it
# did on that dogfood run. With a ready PR on the branch the backstop must
# still stay silent -- the launcher's own adoption path is what settles this
# run, not a synthetic blocked line racing ahead of it.
@test "markdown-mangled outcome line (#1582) + open non-draft PR -> no synthetic blocked line" {
  export FAKE_CLAUDE_MANGLED_OUTCOME=1
  export FAKE_GH_PR_LIST_7='[{"isDraft":false}]'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 0 ]
}
