#!/usr/bin/env bats
# Egress restriction (issue #100): PODMAN_NETWORK and BWRAP_UNSHARE_NET behaviour.

load helper

setup() {
  setup_run_env
}

# --- Egress restriction (issue #100) -----------------------------------------

@test "runtime=podman passes --network flag when PODMAN_NETWORK is set" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tOnly issue'
  export PODMAN_NETWORK=pasta
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q -- '--network pasta' "$PODMAN_LOG"
}

@test "runtime=podman omits --network when PODMAN_NETWORK is unset" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tOnly issue'
  unset PODMAN_NETWORK
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q -- '--network' "$PODMAN_LOG"
}

@test "runtime=bwrap adds --unshare-net when BWRAP_UNSHARE_NET is set" {
  export BWRAP_UNSHARE_NET=1
  export FAKE_GH_ISSUES=$'1\tOnly issue'
  run "$BWRAP_RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q -- '--unshare-net' "$BWRAP_LOG"
}

@test "runtime=bwrap default: no --unshare-net (shares host netns; host-loopback reachable)" {
  export FAKE_GH_ISSUES=$'1\tOnly issue'
  unset BWRAP_UNSHARE_NET
  run "$BWRAP_RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q -- '--unshare-net' "$BWRAP_LOG"
}

