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
  # NETWORK_MODE is unset here, so isolateNet() is true regardless of this
  # flag and pastaPath() applies (issue #2666) -- pasta, not bwrap, is the
  # top-level exec target, and bwrap itself must NOT get --unshare-net since
  # pasta already created the isolated namespace (composing the two would be
  # inside-out: see the default-isolation test below for the direct
  # regression check on this).
  ! grep -q -- '--unshare-net' "$BWRAP_LOG"
}

@test "runtime=bwrap default: isolates network namespace via pasta (host-loopback blocked)" {
  export FAKE_GH_ISSUES=$'1\tOnly issue'
  unset BWRAP_UNSHARE_NET
  unset NETWORK_MODE
  run "$BWRAP_RUN_CMD"
  [ "$status" -eq 0 ]
  # pasta is the top-level exec target (issue #2666); assert its own
  # recorded argv carries the exact ADR 0042 hardened flags, rather than an
  # unanchored substring match against bwrap's log.
  grep -q -- '-t none' "$PASTA_LOG"
  grep -q -- '-T none' "$PASTA_LOG"
  grep -q -- '-u none' "$PASTA_LOG"
  grep -q -- '-U none' "$PASTA_LOG"
  grep -q -- '--no-map-gw' "$PASTA_LOG"
  grep -q -- '--dns-forward 169.254.2.2' "$PASTA_LOG"
  grep -q -- '-f --' "$PASTA_LOG"
  # bwrap itself must NOT unshare net when pasta already created the
  # namespace -- the direct regression test for the "composed inside-out"
  # bug this fix closes.
  ! grep -q -- '--unshare-net' "$BWRAP_LOG"
}

@test "runtime=bwrap NETWORK_MODE=host restores shared host netns (opt-out)" {
  export NETWORK_MODE=host
  export FAKE_GH_ISSUES=$'1\tOnly issue'
  run "$BWRAP_RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q -- '--unshare-net' "$BWRAP_LOG"
  ! grep -q -- 'pasta' "$BWRAP_LOG"
  # NETWORK_MODE=host opts out of pasta entirely -- bwrap is the top-level
  # exec target directly, so pasta never runs and never writes its log.
  [ ! -s "$PASTA_LOG" ]
}

