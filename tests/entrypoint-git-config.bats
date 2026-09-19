#!/usr/bin/env bats
# Hermetic global git config (issue #404): CI's `nix flake check` sandbox has
# no global git config, so the entrypoint provisions the Agent identity
# repo-locally on the workspace clone, keeping the Box's global config empty
# and CI-equivalent.

load helper

setup() {
  setup_entrypoint_env
}

@test "entrypoint sets agent identity repo-locally, not globally" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  run git -C "$WORK_DIR" config --local user.name
  [ "$status" -eq 0 ]
  [ "$output" = "$GIT_USER_NAME" ]
  run git -C "$WORK_DIR" config --local user.email
  [ "$status" -eq 0 ]
  [ "$output" = "$GIT_USER_EMAIL" ]
  # setup_bare_repo seeds the isolated $HOME with a "Seed" global identity. It
  # must survive the entrypoint, which proves the Agent identity landed
  # repo-locally.
  run git config --global user.name
  [ "$status" -eq 0 ]
  [ "$output" = "Seed" ]
}

@test "entrypoint leaves the global git config byte-identical" {
  # setup_bare_repo writes $HOME/.gitconfig itself. Any global write by the
  # entrypoint would leak a setting CI's hermetic check environment lacks.
  local before
  before="$(cat "$HOME/.gitconfig")"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(cat "$HOME/.gitconfig")" = "$before" ]
}

