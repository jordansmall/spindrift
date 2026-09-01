#!/usr/bin/env bats
# Entrypoint <-> `driver-exec bind-registry` seam parity suite (issue #2935).
#
# This is the sole bash-level coverage for that seam's bindings/in-tree modes:
# the mechanism-focused per-ecosystem suites deleted along with the
# per-ecosystem phase functions they tested (parent issue #2918). The seam's
# classification mode is covered by tests/entrypoint-toolchain-nudge.bats.
#
# The verb itself is deeply unit-tested Go; what that coverage doesn't pin is
# the seam. This suite runs $ENTRYPOINT through the real fake-driver-exec chain,
# whose own `bind-registry` branch execs the real $DRIVER_EXEC_BIN, never a bash
# reimplementation.
#
# Two tests cover this seam:
#   - "apply after clone ..." pins the initial intree_binding_apply in
#     isolation. main() unconditionally runs revert -> phase_branch_recovery ->
#     phase_prework_rebase -> apply again right after that first call unless
#     _is_research_kind, so a work dispatch's final file state is the RE-apply's
#     output and would look identical even if the first apply silently no-op'd.
#     This test sets DISPATCH_KIND=research (which still clones and still
#     applies) precisely because that is the one kind where the
#     revert/rebase/re-apply block never runs.
#   - "revert -> branch-recovery -> re-apply ..." is the harness-driven rebase
#     story on top of that mechanism, run as an ordinary work dispatch.
#
# This file claims two ports, REGISTRY_PROXY_FORWARDER_PORT=27192 and 27193;
# nothing else in tests/*.bats claims either. Per-shard Nix sandboxing already
# makes cross-shard collision impossible, so the ports matter only for a runner
# this suite doesn't control: a plain local `bats tests/*.bats`, which shares
# one network namespace across every suite's fixed ports.

load helper

setup() {
  setup_entrypoint_env
}

teardown() {
  kill_stand_in_socat
}

# Seeds the remote's main branch with a committed .cargo/config.toml pinning a
# private registry at $REGISTRY_PROXY_UPSTREAM_HOST. Call after setup_bare_repo.
# The two [source.proxy]/[registries.othercorp] entries are deliberate, not
# filler: ApplyInTreeBinding runs two separate replacement passes over a cargo
# config (one per scheme, sparse+https:// and plain http://), so pruning either
# entry would silently delete that pass's coverage.
_seed_cargo_intree_config() {
  local host="$1"
  local seed="$BATS_TEST_TMPDIR/seed-cargo"
  git clone -q "https://github.com/owner/repo.git" "$seed"
  mkdir -p "$seed/.cargo"
  cat >"$seed/.cargo/config.toml" <<EOF
[source.crates-io]
replace-with = "proxy"

[source.proxy]
registry = "sparse+https://${host}/index/"

[registries.othercorp]
index = "http://${host}/other-index/"
EOF
  git -C "$seed" add .cargo/config.toml
  git -C "$seed" commit -q -m "chore: pin private cargo registry"
  git -C "$seed" push -q origin HEAD:main
}

# Advances the remote's main branch with a further commit that ALSO modifies
# .cargo/config.toml, appending a second [registries.*] block -- the specific
# condition that forces a checkout-safety collision without the revert/re-apply
# wrapper (ADR 0044), so phase_prework_rebase's rebase has real conflicting-blob
# work to replay. Call after _seed_cargo_intree_config.
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

# Spawns the stand-in Forwarder socat on $1 and exports the
# REGISTRY_PROXY_* vars intree_binding_apply reads, shared by both @tests
# below (each passes its own port so the two tests never contend).
_start_stand_in_forwarder() {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT="$1"
  export REGISTRY_PROXY_UPSTREAM_HOST="cargo.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"
}

# Shared closing assertions both @tests below end on: the upstream host is
# gone and the skip-worktree bit is set on the rewritten file.
_assert_cargo_config_rewritten_and_hidden() {
  if grep -q "cargo.mycorp.example" "$WORK_DIR/.cargo/config.toml"; then
    echo "expected cargo.mycorp.example to be rewritten away, but it is still present" >&2
    return 1
  fi

  # `git ls-files -v` prefixes a skip-worktree path with uppercase 'S'.
  local _lsfiles
  _lsfiles="$(git -C "$WORK_DIR" ls-files -v .cargo/config.toml)"
  [[ "$_lsfiles" == S* ]]
}

@test "bind-registry seam: apply after clone rewrites the in-tree config and the sourced env reaches the Driver's child (issue #2935)" {
  # DISPATCH_KIND=research is the only dispatch kind whose main() skips the
  # unconditional revert -> branch-recovery -> prework-rebase -> re-apply dance
  # after intree_binding_apply. A work dispatch would still clone and apply, but
  # the observed final file would be the RE-apply's output, so it could pass
  # even if intree_binding_apply itself were broken.
  export DISPATCH_KIND="research"
  _start_stand_in_forwarder 27192

  _seed_cargo_intree_config "$REGISTRY_PROXY_UPSTREAM_HOST"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # Apply after clone: the committed cargo config's upstream host has been
  # rewritten to the local Forwarder endpoint -- genuinely the first (and only)
  # intree_binding_apply call's own output, per the research kind above.
  grep -q "sparse+http://127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}/index/" "$WORK_DIR/.cargo/config.toml"
  _assert_cargo_config_rewritten_and_hidden

  # The sourced bindings-env-output file's exports actually reach the fake
  # Driver's exec'd child process, not just the entrypoint shell.
  grep -q "env: npm_config_registry=http://127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}/" "$DRIVER_LOG"

  # The sourced intree-bindings-env-output file's cargo placeholder token export
  # (ADR 0044, issue #3053) also reaches the fake Driver's exec'd child --
  # proving intree_binding_apply sources it, not just the bindings-mode file.
  grep -q "env: CARGO_REGISTRIES_OTHERCORP_TOKEN=spindrift-registry-proxy-placeholder-not-a-secret" "$DRIVER_LOG"
}

@test "bind-registry seam: revert -> branch-recovery -> re-apply ends pristine and rebound (issue #2935)" {
  _start_stand_in_forwarder 27193

  _seed_cargo_intree_config "$REGISTRY_PROXY_UPSTREAM_HOST"

  # Stale prior work: agent/issue-7 branches off the pre-advance commit,
  # with unrelated work of its own that never touches .cargo/config.toml.
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
  # origin/agent/issue-7), so phase_prework_rebase's `git rebase origin/main`
  # has real work to replay.
  export FAKE_GH_PR_LIST_7="https://github.com/owner/repo/pull/7"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # The rebase must have actually replayed the branch's own work onto the
  # advanced base.
  [ -f "$WORK_DIR/branch.txt" ]

  # Final content must reflect BOTH the base's new registry entry (proving the
  # rebase replayed the base's change rather than keeping stale pre-rebase
  # content) AND the local-endpoint rewrite (proving re-apply ran after the
  # rebase) -- for every entry: the original sparse+https one, the original
  # plain http one, and the one only the advanced base added.
  grep -q "\[registries.other\]" "$WORK_DIR/.cargo/config.toml"
  grep -q "sparse+http://127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}/index/" "$WORK_DIR/.cargo/config.toml"
  grep -q "index = \"http://127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}/other-index/\"" "$WORK_DIR/.cargo/config.toml"
  grep -q "sparse+http://127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}/other-index/" "$WORK_DIR/.cargo/config.toml"
  _assert_cargo_config_rewritten_and_hidden

  # No stray unrelated files: revert -> rebase -> re-apply left nothing
  # dangling outside .cargo/config.toml itself, whose own rewrite the
  # skip-worktree assertion above already covers.
  [ -z "$(git -C "$WORK_DIR" status --short)" ]
}
