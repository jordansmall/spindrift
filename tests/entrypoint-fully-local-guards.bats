#!/usr/bin/env bats
# Fully-local mode (CODE_FORGE=local AND ISSUE_TRACKER=local) must not require
# REPO_SLUG or GH_TOKEN, which only mean something when the Box talks to a real
# forge. Everything else stays unconditional (issue #2121; mirrors
# cmd/launcher/main.go's validate()).

load helper

setup() {
  setup_entrypoint_env
}

@test "fully-local mode does not require REPO_SLUG or GH_TOKEN" {
  export CODE_FORGE="local"
  export ISSUE_TRACKER="local"
  export BOX_TRACKER_AXIS_READ=LOCAL
  unset BOX_TRACKER_AXIS_WRITE
  # entrypoint.sh reads the launcher-forwarded BOX_FULLY_LOCAL instead of
  # deriving it from CODE_FORGE/ISSUE_TRACKER (issue #2527), so supply it here.
  export BOX_FULLY_LOCAL=1
  unset GH_TOKEN REPO_SLUG

  run bash "$ENTRYPOINT"
  [[ "$output" != *"REPO_SLUG (owner/repo) is required"* ]]
  [[ "$output" != *"GH_TOKEN is required"* ]]
}

# Relaxing the guards needs both CODE_FORGE=local and ISSUE_TRACKER=local, so a
# local tracker alone must still demand GH_TOKEN. The test says nothing about
# REPO_SLUG: entrypoint.sh stopped checking it (issue #2527), leaving it to the
# launcher's validate() and a nix eval assert (lib/mkHarness.nix's
# repoSlugCoherenceOk), which a standalone entrypoint.sh run never reaches.
@test "non-fully-local mode (local tracker only) still requires GH_TOKEN" {
  export ISSUE_TRACKER="local"
  export BOX_TRACKER_AXIS_READ=LOCAL
  unset BOX_TRACKER_AXIS_WRITE
  unset GH_TOKEN REPO_SLUG

  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"GH_TOKEN is required"* ]]
}

# The other direction, a local forge with the default github tracker, is equally
# short of fully-local, so GH_TOKEN stays required. The test says nothing about
# REPO_SLUG for the same reason as above (issue #2527).
@test "non-fully-local mode (local forge only) still requires GH_TOKEN" {
  export CODE_FORGE="local"
  unset GH_TOKEN REPO_SLUG

  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"GH_TOKEN is required"* ]]
}

# GIT_USER_NAME, GIT_USER_EMAIL, and ISSUE_NUMBER stay required in fully-local
# mode. The `:?` guards abort at the first unset var, so each gets its own test
# rather than one test that could only ever reach the first.
@test "fully-local mode still requires ISSUE_NUMBER" {
  export CODE_FORGE="local"
  export ISSUE_TRACKER="local"
  export BOX_TRACKER_AXIS_READ=LOCAL
  unset BOX_TRACKER_AXIS_WRITE
  export BOX_FULLY_LOCAL=1
  unset GH_TOKEN REPO_SLUG ISSUE_NUMBER

  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"ISSUE_NUMBER is required"* ]]
}

@test "fully-local mode still requires GIT_USER_NAME" {
  export CODE_FORGE="local"
  export ISSUE_TRACKER="local"
  export BOX_TRACKER_AXIS_READ=LOCAL
  unset BOX_TRACKER_AXIS_WRITE
  export BOX_FULLY_LOCAL=1
  unset GH_TOKEN REPO_SLUG GIT_USER_NAME

  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"GIT_USER_NAME is required"* ]]
}

@test "fully-local mode still requires GIT_USER_EMAIL" {
  export CODE_FORGE="local"
  export ISSUE_TRACKER="local"
  export BOX_TRACKER_AXIS_READ=LOCAL
  unset BOX_TRACKER_AXIS_WRITE
  export BOX_FULLY_LOCAL=1
  unset GH_TOKEN REPO_SLUG GIT_USER_EMAIL

  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"GIT_USER_EMAIL is required"* ]]
}
