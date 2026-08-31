#!/usr/bin/env bats
# In-tree cargo config binding (ADR 0044, issue #2851): a Target repo can
# commit its own $WORK_DIR/.cargo/config.toml pinning a private registry
# directly (e.g. `[registries.NAME] index =
# "sparse+https://cargo.mycorp.example/index/"`). In-tree cargo config wins
# over the user-level $CARGO_HOME/config.toml phase_registry_proxy_bindings
# writes (rendered by bindregistry.CargoConfigTOML, tested in
# cmd/launcher/internal/bindregistry/registrybindings_test.go), and cargo's
# table-valued [source]/[registries] keys aren't overridable via CARGO_* env
# vars (cargo#5416) -- so
# phase_cargo_intree_binding textually rewrites the committed file in place
# to point at the local Forwarder instead, then hides the rewrite from git
# via skip-worktree so it can never be staged or committed.

load helper

setup() {
  setup_entrypoint_env
}

teardown() {
  kill_stand_in_socat
}

# Seeds the remote's main branch with a committed .cargo/config.toml pinning
# a private registry at $REGISTRY_PROXY_UPSTREAM_HOST via a sparse+https://
# URL. Call after setup_bare_repo.
_seed_cargo_intree_config() {
  local host="$1"
  local seed="$BATS_TEST_TMPDIR/seed-cargo"
  git clone -q "https://github.com/owner/repo.git" "$seed"
  mkdir -p "$seed/.cargo"
  cat >"$seed/.cargo/config.toml" <<EOF
[registries.mycorp]
index = "sparse+https://${host}/index/"
EOF
  git -C "$seed" add .cargo/config.toml
  git -C "$seed" commit -q -m "chore: pin private cargo registry"
  git -C "$seed" push -q origin HEAD:main
}

@test "in-tree cargo config referencing the upstream host is rewritten and hidden via skip-worktree (issue #2851)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27184
  export REGISTRY_PROXY_UPSTREAM_HOST="cargo.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  _seed_cargo_intree_config "$REGISTRY_PROXY_UPSTREAM_HOST"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ -f "$WORK_DIR/.cargo/config.toml" ]
  grep -q "http://127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}" "$WORK_DIR/.cargo/config.toml"
  if grep -q "cargo.mycorp.example" "$WORK_DIR/.cargo/config.toml"; then
    echo "expected cargo.mycorp.example to be rewritten away, but it is still present" >&2
    return 1
  fi

  # skip-worktree bit set: `git ls-files -v` prefixes a skip-worktree path
  # with uppercase 'S' (lowercase is reserved for the separate
  # assume-unchanged bit, which this phase never sets).
  local _lsfiles
  _lsfiles="$(git -C "$WORK_DIR" ls-files -v .cargo/config.toml)"
  [[ "$_lsfiles" == S* ]]

  [ -z "$(git -C "$WORK_DIR" status --short)" ]

  [[ "$output" == *"==> "*"cargo"*"config.toml"* ]]
}

@test "skip-worktree path is never stageable (issue #2851)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27184
  export REGISTRY_PROXY_UPSTREAM_HOST="cargo.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  _seed_cargo_intree_config "$REGISTRY_PROXY_UPSTREAM_HOST"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  git -C "$WORK_DIR" add -A
  [ -z "$(git -C "$WORK_DIR" status --short)" ]
  if git -C "$WORK_DIR" diff --cached --name-only | grep -q '\.cargo/config\.toml'; then
    echo "expected .cargo/config.toml to be excluded from the stage by skip-worktree, but it was staged" >&2
    return 1
  fi
}

@test "no-op when no .cargo/config.toml exists (issue #2851)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27184
  export REGISTRY_PROXY_UPSTREAM_HOST="cargo.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ ! -f "$WORK_DIR/.cargo/config.toml" ]
}

# Advances the remote's main branch with a further commit that ALSO modifies
# .cargo/config.toml, appending a second [registries.*] block. Call after
# _seed_cargo_intree_config: this is the specific condition (the committed
# blob for this path genuinely differs between the branch being rebased and
# the base it's rebasing onto) that triggers git's checkout-safety collision
# without the revert/re-apply wrapper (ADR 0044, issue #2851).
_advance_cargo_intree_config() {
  local host="$1"
  local seed="$BATS_TEST_TMPDIR/seed-cargo-advance"
  git clone -q "https://github.com/owner/repo.git" "$seed"
  cat >>"$seed/.cargo/config.toml" <<EOF

[registries.other]
index = "sparse+https://${host}/other-index/"
EOF
  git -C "$seed" add .cargo/config.toml
  git -C "$seed" commit -q -m "chore: pin second private cargo registry"
  git -C "$seed" push -q origin HEAD:main
}

@test "harness-driven rebase with divergent .cargo/config.toml across branches succeeds with the rewrite in place (issue #2851)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27184
  export REGISTRY_PROXY_UPSTREAM_HOST="cargo.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  _seed_cargo_intree_config "$REGISTRY_PROXY_UPSTREAM_HOST"

  # Stale prior work: agent/issue-7 branches off the pre-advance commit, with
  # unrelated work of its own that never touches .cargo/config.toml.
  local prior="$BATS_TEST_TMPDIR/prior"
  git clone -q "https://github.com/owner/repo.git" "$prior"
  git -C "$prior" checkout -b "agent/issue-7" "origin/main"
  echo "branch work" > "$prior/branch.txt"
  git -C "$prior" add branch.txt
  git -C "$prior" commit -q -m "feat: prior run work"
  git -C "$prior" push -q origin "agent/issue-7"

  # origin/main advances further, also touching .cargo/config.toml -- so the
  # committed blob for that path now genuinely differs between the branch
  # being rebased and the base it rebases onto.
  _advance_cargo_intree_config "$REGISTRY_PROXY_UPSTREAM_HOST"

  # Open PR so the adoption path is taken (git checkout -b agent/issue-7
  # origin/agent/issue-7), mirroring entrypoint-branch-recovery.bats, so
  # phase_prework_rebase's `git rebase origin/main` has real work to replay.
  export FAKE_GH_PR_LIST_7="https://github.com/owner/repo/pull/7"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # The rebase must have actually replayed the branch's own work onto the
  # advanced base.
  [ -f "$WORK_DIR/branch.txt" ]

  # Final content must reflect BOTH the base's new registry entry (proving the
  # rebase actually replayed the base's change, not that it silently kept
  # stale pre-rebase content) AND the local-endpoint rewrite.
  grep -q "registries.other" "$WORK_DIR/.cargo/config.toml"
  grep -q "http://127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}" "$WORK_DIR/.cargo/config.toml"
  if grep -q "cargo.mycorp.example" "$WORK_DIR/.cargo/config.toml"; then
    echo "expected cargo.mycorp.example to be rewritten away, but it is still present" >&2
    return 1
  fi

  local _lsfiles
  _lsfiles="$(git -C "$WORK_DIR" ls-files -v .cargo/config.toml)"
  [[ "$_lsfiles" == S* ]]

  [ -z "$(git -C "$WORK_DIR" status --short)" ]
}

@test "no-op when committed cargo config doesn't reference the configured host (issue #2851)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27184
  export REGISTRY_PROXY_UPSTREAM_HOST="cargo.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  _seed_cargo_intree_config "cargo.other-registry.example"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ -f "$WORK_DIR/.cargo/config.toml" ]
  grep -q "cargo.other-registry.example" "$WORK_DIR/.cargo/config.toml"

  local _lsfiles
  _lsfiles="$(git -C "$WORK_DIR" ls-files -v .cargo/config.toml)"
  [[ "$_lsfiles" != S* ]]
}
