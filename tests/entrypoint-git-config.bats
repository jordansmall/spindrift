#!/usr/bin/env bats
# Hermetic global git config (issue #404).

load helper

setup() {
  setup_entrypoint_env
}

# --- hermetic global git config (issue #404) --------------------------------
# CI's `nix flake check` sandbox has no global git config. The entrypoint must
# provision Agent identity repo-locally on the workspace clone so the Box's
# global git config surface stays empty and CI-equivalent -- otherwise a
# git-shelling test that reads global config can pass in the Box and fail in
# CI (or vice versa).

@test "entrypoint sets agent identity repo-locally, not globally" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  run git -C "$WORK_DIR" config --local user.name
  [ "$status" -eq 0 ]
  [ "$output" = "$GIT_USER_NAME" ]
  run git -C "$WORK_DIR" config --local user.email
  [ "$status" -eq 0 ]
  [ "$output" = "$GIT_USER_EMAIL" ]
  # setup_bare_repo seeds the isolated $HOME with a "Seed" global identity to
  # create the local bare-repo fixture. The entrypoint must not overwrite it
  # with the Agent's identity -- proving identity was provisioned repo-locally.
  run git config --global user.name
  [ "$status" -eq 0 ]
  [ "$output" = "Seed" ]
}

@test "entrypoint leaves the global git config byte-identical" {
  # setup_bare_repo seeds $HOME/.gitconfig itself (identity + init.defaultBranch)
  # to create the local bare-repo fixture. The entrypoint must not add to or
  # change that file at all -- any global git config write here would leak a
  # setting CI's hermetic check environment lacks.
  local before
  before="$(cat "$HOME/.gitconfig")"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(cat "$HOME/.gitconfig")" = "$before" ]
}

