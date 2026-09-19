#!/usr/bin/env bats
# Issue #3199: after the driver runs, warn about any git-tracked lockfile that
# still names the run's Forwarder URL, so a stale pin doesn't ship in the PR.

load helper

readonly _FIXED_FORWARDER_PORT=27182

setup() {
  setup_entrypoint_env
}

# The scan (issue #3199) only calls registrymanifest.Parse to decide the proxy
# was on for this dispatch, never resolveRegistryProxyGate, so this endpoint
# need not be reachable.
_export_registry_proxy_manifest() {
  export REGISTRY_PROXY_MANIFEST="{\"endpoint\":\"unix://${BATS_TEST_TMPDIR}/registry-proxy.sock\",\"routes\":[]}"
}

_seed_stale_cargo_lock() {
  local seed="$BATS_TEST_TMPDIR/seed-cargo-lock"
  git clone -q "https://github.com/owner/repo.git" "$seed"
  cat >"$seed/Cargo.lock" <<EOF
[[package]]
name = "example"
source = "registry+http://127.0.0.1:${_FIXED_FORWARDER_PORT}/r0/index/"
EOF
  git -C "$seed" add Cargo.lock
  git -C "$seed" commit -q -m "chore: pin registry"
  git -C "$seed" push -q origin HEAD:main
}

@test "lockfile scan: warns at settle when a tracked lockfile still names the Forwarder URL" {
  _export_registry_proxy_manifest
  _seed_stale_cargo_lock

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "==> WARNING: cargo lockfile Cargo.lock still names the registry proxy Forwarder URL 127.0.0.1:${_FIXED_FORWARDER_PORT} — this will ship in the PR (issue #3199)"
}

@test "lockfile scan: clean repo produces no warning and no other new output" {
  _export_registry_proxy_manifest

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! echo "$output" | grep -q "still names the registry proxy Forwarder URL"
}

@test "lockfile scan: warning still emitted when the driver exits non-zero" {
  _export_registry_proxy_manifest
  _seed_stale_cargo_lock
  export FAKE_DRIVER_CRASH_EXIT=17

  run bash "$ENTRYPOINT"
  [ "$status" -eq 17 ]
  echo "$output" | grep -q "==> WARNING: cargo lockfile Cargo.lock still names the registry proxy Forwarder URL 127.0.0.1:${_FIXED_FORWARDER_PORT} — this will ship in the PR (issue #3199)"
}
