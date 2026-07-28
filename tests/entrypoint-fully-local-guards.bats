#!/usr/bin/env bats
# Fully-local mode (CODE_FORGE=local AND ISSUE_TRACKER=local) must not require
# REPO_SLUG or GH_TOKEN — those two are only meaningful when the Box talks to
# a real forge. GIT_USER_NAME, GIT_USER_EMAIL, and ISSUE_NUMBER stay
# unconditional in every mode (issue #2121; mirrors cmd/launcher/main.go's
# validate(), lines 439-451).

load helper

setup() {
  setup_entrypoint_env
}

@test "fully-local mode does not require REPO_SLUG or GH_TOKEN" {
  export CODE_FORGE="local"
  export ISSUE_TRACKER="local"
  unset GH_TOKEN REPO_SLUG

  run bash "$ENTRYPOINT"
  [[ "$output" != *"REPO_SLUG (owner/repo) is required"* ]]
  [[ "$output" != *"GH_TOKEN is required"* ]]
}

# Regression guard: only the CODE_FORGE=local AND ISSUE_TRACKER=local
# combination relaxes the guards above. Anything short of that (here,
# CODE_FORGE left at its github default while ISSUE_TRACKER=local) must
# still demand REPO_SLUG and GH_TOKEN, same as before this change.
@test "non-fully-local mode (local tracker only) still requires REPO_SLUG and GH_TOKEN" {
  export ISSUE_TRACKER="local"
  unset GH_TOKEN REPO_SLUG

  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"REPO_SLUG (owner/repo) is required"* ]]
}

# Mirror of the guard above: the other half of the AND (CODE_FORGE=local while
# ISSUE_TRACKER stays at its github default) is equally short of fully-local,
# so it too must still demand REPO_SLUG and GH_TOKEN.
@test "non-fully-local mode (local forge only) still requires REPO_SLUG and GH_TOKEN" {
  export CODE_FORGE="local"
  unset GH_TOKEN REPO_SLUG

  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"REPO_SLUG (owner/repo) is required"* ]]
}

# GIT_USER_NAME, GIT_USER_EMAIL, and ISSUE_NUMBER stay unconditional even in
# fully-local mode -- only REPO_SLUG and GH_TOKEN are relaxed. The `:?` guards
# abort at the first unset var, so each of the three gets its own test (unset
# in isolation) rather than one test that could only ever reach the first.
@test "fully-local mode still requires ISSUE_NUMBER" {
  export CODE_FORGE="local"
  export ISSUE_TRACKER="local"
  unset GH_TOKEN REPO_SLUG ISSUE_NUMBER

  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"ISSUE_NUMBER is required"* ]]
}

@test "fully-local mode still requires GIT_USER_NAME" {
  export CODE_FORGE="local"
  export ISSUE_TRACKER="local"
  unset GH_TOKEN REPO_SLUG GIT_USER_NAME

  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"GIT_USER_NAME is required"* ]]
}

@test "fully-local mode still requires GIT_USER_EMAIL" {
  export CODE_FORGE="local"
  export ISSUE_TRACKER="local"
  unset GH_TOKEN REPO_SLUG GIT_USER_EMAIL

  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"GIT_USER_EMAIL is required"* ]]
}
