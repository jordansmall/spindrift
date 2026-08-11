#!/usr/bin/env bats
# tests/fakes/gh's `pr ready` arm (issue #2423): models the launcher's
# MarkReady (cmd/launcher/internal/forge/github/exec_pr.go) shelling out to
# `gh pr ready <prURL>` to flip a draft PR to ready. Exercised directly
# against the fake -- no isolated fake-only bats convention exists yet
# (checked: no tests/fake-gh*.bats or similar precedent), so this is a
# minimal standalone unit test of the fake's own stdin/stdout/GH_LOG/GH_STATE
# contract, mirroring tests/credential-deny.bats's direct-script-invocation
# style rather than the run/entrypoint harness in tests/helper.bash.

setup() {
  GH_BIN="${FAKES_DIR:-$BATS_TEST_DIRNAME/fakes}/gh"
  [ -f "$GH_BIN" ]
  export GH_LOG="$BATS_TEST_TMPDIR/gh.log"
  export GH_STATE="$BATS_TEST_TMPDIR/gh.state"
  : >"$GH_LOG"
}

@test "gh pr ready records the invocation and flips a subsequent isDraft query to false" {
  export FAKE_GH_PR_DRAFT_42="true"
  run bash "$GH_BIN" pr ready https://github.com/owner/repo/pull/42
  [ "$status" -eq 0 ]
  grep -qF "pr ready https://github.com/owner/repo/pull/42" "$GH_LOG"

  run bash "$GH_BIN" pr view https://github.com/owner/repo/pull/42 --json isDraft
  [ "$status" -eq 0 ]
  [ "$output" = "false" ]
}

@test "isDraft falls back to the static FAKE_GH_PR_DRAFT_N env var when no ready flip was recorded" {
  export FAKE_GH_PR_DRAFT_43="true"
  run bash "$GH_BIN" pr view https://github.com/owner/repo/pull/43 --json isDraft
  [ "$status" -eq 0 ]
  [ "$output" = "true" ]
}
