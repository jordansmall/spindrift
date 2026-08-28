#!/usr/bin/env bats
# Proves pnpm needs no distinct Binding entry for a per-scope in-tree
# .npmrc rewrite (issue #2855): pnpm reads the exact same `.npmrc` file
# format npm does (a default `registry=` line and/or `@scope:registry=`
# lines), and phase_npm_intree_binding_apply's rewrite (entrypoint-npm-
# intree-binding.bats, issue #2854) is a plain sed over the whole file
# content gated only on `.npmrc` existing + being git-tracked -- it never
# inspects which lockfile is present, so an npm project and a pnpm project
# with an equivalent committed .npmrc are indistinguishable to this phase.
# This file demonstrates that identical behavior with a pnpm-lock.yaml
# marker present instead of a package-lock.json/npm project, with zero
# production code changes.

load helper

setup() {
  setup_entrypoint_env
}

teardown() {
  kill_stand_in_socat
}

# Seeds the remote's main branch with a pnpm-lock.yaml marker (so the repo
# looks like a real pnpm project) AND a committed .npmrc pinning a private
# scoped registry at $REGISTRY_PROXY_UPSTREAM_HOST, in one commit. Call
# after setup_bare_repo.
_seed_pnpm_intree_config() {
  local host="$1"
  local seed="$BATS_TEST_TMPDIR/seed-pnpm"
  git clone -q "https://github.com/owner/repo.git" "$seed"
  touch "$seed/pnpm-lock.yaml"
  cat >"$seed/.npmrc" <<EOF
@mycorp:registry=https://${host}/
EOF
  git -C "$seed" add pnpm-lock.yaml .npmrc
  git -C "$seed" commit -q -m "chore: seed pnpm project with private scoped registry"
  git -C "$seed" push -q origin HEAD:main
}

@test "in-tree .npmrc referencing the upstream host is rewritten and hidden via skip-worktree for a pnpm project (issue #2855)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27184
  export REGISTRY_PROXY_UPSTREAM_HOST="npm.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  _seed_pnpm_intree_config "$REGISTRY_PROXY_UPSTREAM_HOST"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ -f "$WORK_DIR/pnpm-lock.yaml" ]
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
}

@test "skip-worktree .npmrc is never stageable for a pnpm project (issue #2855)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27184
  export REGISTRY_PROXY_UPSTREAM_HOST="npm.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  _seed_pnpm_intree_config "$REGISTRY_PROXY_UPSTREAM_HOST"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  git -C "$WORK_DIR" add -A
  [ -z "$(git -C "$WORK_DIR" status --short)" ]
  if git -C "$WORK_DIR" diff --cached --name-only | grep -q '\.npmrc'; then
    echo "expected .npmrc to be excluded from the stage by skip-worktree, but it was staged" >&2
    return 1
  fi
}

@test "no-op when no .npmrc exists in a pnpm project (issue #2855)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27184
  export REGISTRY_PROXY_UPSTREAM_HOST="npm.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  seed_dependency_manifest "pnpm-lock.yaml"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ -f "$WORK_DIR/pnpm-lock.yaml" ]
  [ ! -f "$WORK_DIR/.npmrc" ]
}
