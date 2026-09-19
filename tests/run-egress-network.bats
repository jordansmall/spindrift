#!/usr/bin/env bats
# Egress restriction (issue #100): PODMAN_NETWORK and BWRAP_UNSHARE_NET behaviour.

load helper

setup() {
  setup_run_env
}

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
  # NETWORK_MODE is unset, so isolateNet() is true whatever this flag says and
  # pasta, not bwrap, is the top-level exec target (issue #2666). bwrap must
  # not also get --unshare-net: pasta already created the isolated namespace.
  ! grep -q -- '--unshare-net' "$BWRAP_LOG"
}

@test "runtime=bwrap default: isolates network namespace via pasta (host-loopback blocked)" {
  export FAKE_GH_ISSUES=$'1\tOnly issue'
  unset BWRAP_UNSHARE_NET
  unset NETWORK_MODE
  run "$BWRAP_RUN_CMD"
  [ "$status" -eq 0 ]
  # pasta is the top-level exec target (issue #2666), so assert against its own
  # recorded argv for the exact hardened ADR 0042 flags rather than an
  # unanchored substring match against bwrap's log.
  grep -q -- '-t none' "$PASTA_LOG"
  grep -q -- '-T none' "$PASTA_LOG"
  grep -q -- '-u none' "$PASTA_LOG"
  grep -q -- '-U none' "$PASTA_LOG"
  grep -q -- '--no-map-gw' "$PASTA_LOG"
  grep -q -- '--dns-forward 169.254.2.2' "$PASTA_LOG"
  grep -q -- '-f --' "$PASTA_LOG"
  # Regression check: bwrap must not unshare net when pasta already created
  # the namespace.
  ! grep -q -- '--unshare-net' "$BWRAP_LOG"
}

@test "runtime=bwrap NETWORK_MODE=host restores shared host netns (opt-out)" {
  export NETWORK_MODE=host
  export FAKE_GH_ISSUES=$'1\tOnly issue'
  run "$BWRAP_RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q -- '--unshare-net' "$BWRAP_LOG"
  ! grep -q -- 'pasta' "$BWRAP_LOG"
  # NETWORK_MODE=host opts out of pasta entirely, so pasta never runs and
  # never writes its log.
  [ ! -s "$PASTA_LOG" ]
}

