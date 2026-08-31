#!/usr/bin/env bats
# phase_registry_proxy_bindings's own no-op/guard behavior (ADR 0044, issue
# #2931), independent of any one downstream binding it feeds.
# phase_registry_proxy_bindings (agent/entrypoint.sh) is the shared
# general-purpose phase every registry-proxy consumer depends on: the
# npm/yarn/pnpm in-tree phases and the Go verb's own direct
# $CARGO_HOME/config.toml write all read the phase's
# `_registry_proxy_forwarder_ready` sentinel, and none of them re-probe the
# Forwarder themselves. Until issue #2934 (gradle binding moving from a bash
# phase into a Go verb) this phase's `unset FORWARDER_READY` guard was
# incidentally covered by tests/entrypoint-registry-proxy-gradle-binding.bats
# -- deleted in that same change, along with every test in it. This file
# exists to keep that guard, and the phase's plain no-op path, under direct
# test now that no downstream-mechanism suite covers them by accident.
#
# The ambient-leak test below proves the guard via
# phase_npm_intree_binding_apply, not the cargo config.toml the verb writes
# directly to $CARGO_HOME: that write happens inside the `driver-exec
# bind-registry` child process itself, gated only on whether the socket path
# it was handed is a real mounted socket -- it never reads bash's ambient
# FORWARDER_READY, so it can't distinguish the guard being present from it
# being missing. phase_npm_intree_binding_apply's own
# `[ -n "${_registry_proxy_forwarder_ready:-}" ]` check, by contrast, reads
# exactly the sentinel the guard protects.
#
# REGISTRY_PROXY_FORWARDER_PORT=27185 here is free again: it was the deleted
# gradle suite's own port (grep `REGISTRY_PROXY_FORWARDER_PORT=` across
# tests/*.bats to confirm no other suite claims 27185/27186/27187 today).

load helper

setup() {
  setup_entrypoint_env
}

teardown() {
  kill_stand_in_socat
}

# Seeds the remote's main branch with a committed .npmrc pinning a private
# scoped registry at $REGISTRY_PROXY_UPSTREAM_HOST -- the same fixture shape
# tests/entrypoint-npm-intree-binding.bats's own
# _seed_npm_intree_config uses. Call after setup_bare_repo.
_seed_npm_intree_config() {
  local host="$1"
  local seed="$BATS_TEST_TMPDIR/seed-npm"
  git clone -q "https://github.com/owner/repo.git" "$seed"
  cat >"$seed/.npmrc" <<EOF
@mycorp:registry=https://${host}/
EOF
  git -C "$seed" add .npmrc
  git -C "$seed" commit -q -m "chore: pin private npm scoped registry"
  git -C "$seed" push -q origin HEAD:main
}

@test "socket absent: phase_registry_proxy_bindings is a silent no-op (issue #2931)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27185

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ ! -f "$HOME/.cargo/config.toml" ]
  [[ "$output" != *"==> WARNING:"* ]]
}

@test "socket absent, ambient FORWARDER_READY leaked from outside the phase: downstream npm in-tree phase stays a no-op (issue #2931)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27185
  export REGISTRY_PROXY_UPSTREAM_HOST="npm.mycorp.example"

  # Simulates a pre-existing FORWARDER_READY sitting in the ambient
  # environment from outside phase_registry_proxy_bindings's control (e.g. a
  # leaked export from a parent process or a prior phase) -- exactly what
  # phase_registry_proxy_bindings's `unset FORWARDER_READY` right before
  # invoking `driver-exec bind-registry` (agent/entrypoint.sh) guards
  # against. The socket path above doesn't exist, so the verb is a silent
  # no-op that writes nothing to its bindings-env-output file; without the
  # guard, the read at `_registry_proxy_forwarder_ready="${FORWARDER_READY:-}"`
  # would pick up this ambient value instead and wrongly conclude the
  # Forwarder is up, which phase_npm_intree_binding_apply (run right after
  # clone_repo, sharing the same sentinel) would in turn trust to rewrite
  # $WORK_DIR/.npmrc at a Forwarder that was never actually started.
  export FORWARDER_READY=1

  _seed_npm_intree_config "$REGISTRY_PROXY_UPSTREAM_HOST"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # Untouched: with the guard intact, _registry_proxy_forwarder_ready must
  # stay empty despite the ambient leak, so phase_npm_intree_binding_apply
  # returns before ever rewriting the committed host reference.
  grep -q "npm.mycorp.example" "$WORK_DIR/.npmrc"
  if grep -q "127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}" "$WORK_DIR/.npmrc"; then
    echo "expected .npmrc to be untouched, but it was rewritten at a Forwarder that was never started" >&2
    return 1
  fi

  local _lsfiles
  _lsfiles="$(git -C "$WORK_DIR" ls-files -v .npmrc)"
  [[ "$_lsfiles" != S* ]]

  # $HOME/.cargo/config.toml is the user-level cargo binding the verb itself
  # renders directly to disk once it believes the Forwarder is ready --
  # still absent here too, since the verb's own no-op is keyed on the socket
  # path, not on this ambient leak.
  [ ! -f "$HOME/.cargo/config.toml" ]
}

@test "socket present: Forwarder starts and cargo/npm/pnpm/yarn berry are bound (issue #2934)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27185

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!

  wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # Surviving home for the two assertions
  # tests/entrypoint-registry-proxy-gradle-binding.bats used to carry
  # (deleted alongside gradle binding's move into a Go verb, issue #2934):
  # the rendered $HOME/.cargo/config.toml content, and proof that
  # npm_config_registry/pnpm_config_registry/YARN_NPM_REGISTRY_SERVER reach
  # the fake Driver's own child process via $DRIVER_LOG
  # (tests/fakes/_driver-common.bash), not just get exported in the
  # entrypoint shell.
  [ -f "$HOME/.cargo/config.toml" ]
  grep -q 'replace-with = "spindrift-registry-proxy"' "$HOME/.cargo/config.toml"
  grep -q "registry = \"sparse+http://127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}/\"" "$HOME/.cargo/config.toml"

  # $GRADLE_USER_HOME is unset in this test, so the verb resolves it to the
  # $HOME/.gradle default (driver-exec bind-registry, bindregistry_cmd.go).
  [ -f "$HOME/.gradle/init.d/spindrift-registry-proxy.init.gradle" ]

  grep -q "env: npm_config_registry=http://127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}/" "$DRIVER_LOG"
  grep -q "env: pnpm_config_registry=http://127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}/" "$DRIVER_LOG"
  grep -q "env: YARN_NPM_REGISTRY_SERVER=http://127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}/" "$DRIVER_LOG"
}
