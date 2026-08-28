#!/usr/bin/env bats
# In-Box Forwarder (ADR 0044, issue #2849): phase_registry_proxy_forwarder
# presents the registry proxy's mounted unix socket
# (cmd/launcher/internal/runner/mount.go's registryProxySocketTarget) as a
# local TCP endpoint via socat, then binds cargo's crates-io source to it.
# REGISTRY_PROXY_SOCKET_PATH/REGISTRY_PROXY_FORWARDER_PORT are the
# test-only overrides these two tests rely on (agent/entrypoint.sh's
# configure_env) so this suite never touches the real host
# /registry-proxy.sock or collides with a stray real Forwarder on the
# production default port 27182.

load helper

setup() {
  setup_entrypoint_env
}

# Kills any backgrounded stand-in socat process this suite started, so a
# leaked process never survives past the test -- mirrors the chmod-then-
# continue shape of entrypoint-home-agent-files.bats' teardown, but for
# process cleanup instead of filesystem permissions.
teardown() {
  [ -n "${_test_socat_pid:-}" ] && kill "$_test_socat_pid" 2>/dev/null
  true
}

# Bounded poll for the stand-in socat's UNIX-LISTEN socket file to actually
# exist -- a freshly backgrounded socat may take a moment to bind -- in the
# same bounded-poll spirit as wait_for_log_lines (tests/helper.bash), just
# shaped around a filesystem test instead of a log-line count.
_wait_for_socket() {
  local _path="$1" _tries=0
  while [ "$_tries" -lt 50 ]; do
    [ -S "$_path" ] && return 0
    sleep 0.1
    _tries=$((_tries + 1))
  done
  return 1
}

@test "socket present: Forwarder starts and cargo config.toml is written (issue #2849)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27183

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!

  _wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ -f "$HOME/.cargo/config.toml" ]
  grep -q 'replace-with = "spindrift-registry-proxy"' "$HOME/.cargo/config.toml"
  grep -q "registry = \"sparse+http://127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}/\"" "$HOME/.cargo/config.toml"

  [[ "$output" == *"==> registry proxy Forwarder up on 127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}"* ]]
}

@test "socket absent: phase is a silent no-op (issue #2849)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27183

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ ! -f "$HOME/.cargo/config.toml" ]

  [[ "$output" != *"==> registry proxy Forwarder"* ]]
  [[ "$output" != *"==> WARNING:"*"socat"* ]]
}
