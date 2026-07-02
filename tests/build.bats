#!/usr/bin/env bats
# Behaviour of the nix-generated `build` command: it realises the image
# derivation, then loads it — falling back to an ephemeral Nix container on the
# runtime when the host has no Linux builder. Driven entirely through fakes
# (nix + podman), so no real build, container, or store is touched.

load helper

setup() {
  setup_fakes
  cd "$BATS_TEST_TMPDIR"
}

# --- host-build path (a host WITH a Linux builder) ---------------------------

@test "build realises the derivation on the host, then loads the baked path" {
  export FAKE_NIX_BUILD_OK=1
  run "$BUILD_CMD"
  [ "$status" -eq 0 ]
  grep -q 'build' "$NIX_LOG"
  grep -q "load -i $IMAGE_PATH" "$PODMAN_LOG"
  ! grep -q '^run ' "$PODMAN_LOG"
}

@test "the host-build path loads a nix store path for the image archive" {
  export FAKE_NIX_BUILD_OK=1
  run "$BUILD_CMD"
  [ "$status" -eq 0 ]
  grep -q 'load -i /nix/store/' "$PODMAN_LOG"
}

# --- container-fallback path (a host WITHOUT a Linux builder) -----------------

@test "build falls back to an ephemeral Nix container when the host can't realise it" {
  export FAKE_NIX_BUILD_OK=0
  run "$BUILD_CMD"
  [ "$status" -eq 0 ]
  grep -q 'build' "$NIX_LOG"
  grep -q '^run ' "$PODMAN_LOG"
  grep -q 'spindrift-nix:/nix' "$PODMAN_LOG"
  grep -q 'load -i' "$PODMAN_LOG"
}

@test "the fallback is incremental: a second run reuses the /nix named volume" {
  export FAKE_NIX_BUILD_OK=0
  run "$BUILD_CMD"
  [ "$status" -eq 0 ]
  run "$BUILD_CMD"
  [ "$status" -eq 0 ]
  # Both container builds mount the same named volume, so /nix persists.
  [ "$(grep -c 'spindrift-nix:/nix' "$PODMAN_LOG")" -eq 2 ]
}

# --- both paths impossible ----------------------------------------------------

@test "build exits non-zero with an actionable message when neither path works" {
  export FAKE_NIX_BUILD_OK=0
  # BUILD_NO_RUNTIME_CMD bakes a runtime that is never on PATH, so the container
  # fallback is unavailable too.
  run "$BUILD_NO_RUNTIME_CMD"
  [ "$status" -ne 0 ]
  [[ "$output" == *"Linux builder"* ]]
  [[ "$output" == *"container runtime"* ]]
}
