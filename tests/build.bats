#!/usr/bin/env bats
# Behaviour of the nix-generated `build` command (loads the image into podman).

load helper

setup() {
  setup_fakes
}

@test "build loads the image by its baked store path" {
  run "$BUILD_CMD"
  [ "$status" -eq 0 ]
  grep -q "load -i $IMAGE_PATH" "$PODMAN_LOG"
}

@test "build references a nix store path for the image archive" {
  run "$BUILD_CMD"
  [ "$status" -eq 0 ]
  grep -q 'load -i /nix/store/' "$PODMAN_LOG"
}
