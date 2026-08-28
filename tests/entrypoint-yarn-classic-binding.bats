#!/usr/bin/env bats
# yarn classic (1.x) Binding (issue #2856): empirically, real yarn 1.22.22
# reads $WORK_DIR/.npmrc with the exact same file, format, and config
# precedence as npm itself (verified against a scratch project with a
# committed `@mycorp:registry=` scope: `npm_config_registry` env override won
# the unscoped default, the scoped entry stayed untouched, byte-for-byte the
# same as npm). So npm's existing Binding (issue #2854:
# phase_registry_proxy_forwarder's npm_config_registry export,
# phase_npm_intree_binding_apply's in-tree .npmrc rewrite) already covers
# yarn classic projects with zero new agent/entrypoint.sh code -- this test
# exists purely to prove that claim in-repo, not to add new behavior.

load helper

setup() {
  setup_entrypoint_env
}

teardown() {
  [ -n "${_test_socat_pid:-}" ] && kill "$_test_socat_pid" 2>/dev/null
  true
}

# Bounded poll for the stand-in socat's UNIX-LISTEN socket file to actually
# exist -- copied from entrypoint-registry-proxy-forwarder.bats.
_wait_for_socket() {
  local _path="$1" _tries=0
  while [ "$_tries" -lt 50 ]; do
    [ -S "$_path" ] && return 0
    sleep 0.1
    _tries=$((_tries + 1))
  done
  return 1
}

# Seeds the remote's main branch with a yarn-classic-shaped project: a
# committed .npmrc pinning a private scoped registry at
# $REGISTRY_PROXY_UPSTREAM_HOST (mirrors _seed_npm_intree_config in
# entrypoint-npm-intree-binding.bats), plus a committed yarn.lock -- empty is
# fine, neither Binding phase ever reads it. Call after setup_bare_repo.
_seed_yarn_classic_config() {
  local host="$1"
  local seed="$BATS_TEST_TMPDIR/seed-yarn-classic"
  git clone -q "https://github.com/owner/repo.git" "$seed"
  cat >"$seed/.npmrc" <<EOF
@mycorp:registry=https://${host}/
EOF
  : >"$seed/yarn.lock"
  git -C "$seed" add .npmrc yarn.lock
  git -C "$seed" commit -q -m "chore: pin private npm scoped registry for yarn classic"
  git -C "$seed" push -q origin HEAD:main
}

@test "yarn classic rides npm's in-tree .npmrc binding unchanged (issue #2856)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27188
  export REGISTRY_PROXY_UPSTREAM_HOST="npm.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  _wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  _seed_yarn_classic_config "$REGISTRY_PROXY_UPSTREAM_HOST"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ -f "$WORK_DIR/.npmrc" ]
  grep -q "@mycorp:registry=http://127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}" "$WORK_DIR/.npmrc"
  if grep -q "npm.mycorp.example" "$WORK_DIR/.npmrc"; then
    echo "expected npm.mycorp.example to be rewritten away, but it is still present" >&2
    return 1
  fi

  # skip-worktree bit set: `git ls-files -v` prefixes a skip-worktree path
  # with uppercase 'S' (lowercase is reserved for the separate
  # assume-unchanged bit, which this phase never sets).
  local _lsfiles
  _lsfiles="$(git -C "$WORK_DIR" ls-files -v .npmrc)"
  [[ "$_lsfiles" == S* ]]

  [ -z "$(git -C "$WORK_DIR" status --short)" ]

  # yarn.lock is not a config file either Binding phase touches -- it must
  # ride through untouched, still an empty stub.
  [ -f "$WORK_DIR/yarn.lock" ]
  [ ! -s "$WORK_DIR/yarn.lock" ]
}
