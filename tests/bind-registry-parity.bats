#!/usr/bin/env bats
# Entrypoint <-> `driver-exec bind-registry` seam parity suite (issue #2935).
#
# This is now the SOLE bash-level test coverage for the entrypoint <->
# bind-registry seam's bindings/in-tree modes: the mechanism-focused
# per-ecosystem suites (tests/entrypoint-registry-proxy-bindings.bats,
# tests/entrypoint-cargo-intree-binding.bats,
# tests/entrypoint-yarn-classic-binding.bats) deleted along with the
# per-ecosystem phase functions they tested (parent issue #2918's Testing
# Decisions: "The five mechanism-focused bats suites and their socat
# fixtures and hand-assigned port table delete with the code they tested");
# the other two of that original five (npm, pnpm/yarn-berry) already went in
# #2933/#2934. The seam's classification mode has its own coverage,
# unaffected by any of this: tests/entrypoint-toolchain-nudge.bats drives
# the same entrypoint -> `driver-exec bind-registry` call for that mode.
#
# The verb itself is deeply unit-tested Go
# (cmd/launcher/driver-exec/bindregistry_cmd_test.go,
# cmd/launcher/internal/bindregistry/*_test.go). What that Go coverage
# doesn't pin is the seam: this suite runs $ENTRYPOINT through the real
# fake-driver-exec chain (tests/helper.bash's setup_entrypoint_env;
# tests/fakes/driver-exec's own `bind-registry` branch `exec`s the real
# $DRIVER_EXEC_BIN bind-registry "$@", never a bash reimplementation).
#
# Two tests cover this seam:
#   - "apply after clone rewrites the in-tree config and the sourced env
#     reaches the Driver's child" -- pins the initial intree_binding_apply
#     (agent/entrypoint.sh:1479) in isolation. main() unconditionally runs
#     revert -> phase_branch_recovery -> phase_prework_rebase -> apply again
#     right after that first call (:1483-1487) unless _is_research_kind, so
#     a plain work dispatch's final file state is the RE-apply's output --
#     it would look identical even if the first apply silently no-op'd. This
#     test sets DISPATCH_KIND=research (which still clones and still runs
#     intree_binding_apply, per tests/entrypoint-research-self-contained.bats's
#     "without SELF_CONTAINED still drives ... and clones") specifically
#     because that is the one dispatch kind where the revert/rebase/re-apply
#     block never runs at all, leaving the first apply's own output as the
#     only thing that could have produced the observed rewrite.
#   - "revert -> branch-recovery -> re-apply ends pristine and rebound" --
#     the harness-driven rebase story on top of that already-proven apply
#     mechanism, run as an ordinary work dispatch so the revert/rebase/
#     re-apply block actually executes.
#
# The Forwarder's own listen port is no longer a per-call --forwarder-port
# flag (issue #3141): bindregistry.ForwarderPort is a single fixed constant
# (27182), so both @tests below spawn a real detached Forwarder socat bound
# to the exact same port, one right after the other in this shard's shared
# network namespace. bindregistry.SpawnSocat detaches it (Setsid) precisely
# so it survives past the bash subprocess that spawned it -- which also means
# bats' own subshell-per-@test reaping never cleans it up, so without
# _kill_leaked_forwarder below, @test 2 would find @test 1's still-listening
# Forwarder already "ready" (EnsureForwarderReady probes before spawning) and
# reuse it -- silently bridging to @test 1's already-torn-down unix socket
# instead of @test 2's own fresh one.
readonly _FIXED_FORWARDER_PORT=27182

load helper

setup() {
  setup_entrypoint_env
}

teardown() {
  kill_stand_in_socat
  _kill_leaked_forwarder
}

# Kills any detached Forwarder socat process a prior @test's own `driver-exec
# bind-registry` call spawned and left listening on the fixed
# bindregistry.ForwarderPort (see _FIXED_FORWARDER_PORT's own comment above
# for why this teardown step exists at all). /proc-based rather than
# pkill/fuser, since neither is guaranteed on this harness's PATH.
_kill_leaked_forwarder() {
  local _proc _cmdline
  for _proc in /proc/[0-9]*; do
    _cmdline="$(tr '\0' ' ' <"$_proc/cmdline" 2>/dev/null)" || continue
    case "$_cmdline" in
    *"TCP-LISTEN:${_FIXED_FORWARDER_PORT}"*) kill "${_proc#/proc/}" 2>/dev/null ;;
    esac
  done
  true
}

# Seeds the remote's main branch with a committed .cargo/config.toml pinning
# a private registry at $REGISTRY_PROXY_UPSTREAM_HOST. Copied from the
# now-deleted tests/entrypoint-cargo-intree-binding.bats's own
# _seed_cargo_intree_config: this fixture is specific to the cargo config
# shape this suite seeds, not a candidate for tests/helper.bash (this repo's
# actual shared-fixture home, holding setup_bare_repo, seed_flake_repo,
# seed_dependency_manifest, wait_for_socket, kill_stand_in_socat). Call
# after setup_bare_repo. The two [source.proxy]/[registries.othercorp]
# entries below are deliberate, not filler: ApplyInTreeBinding runs two
# separate replacement passes over a cargo config (one per scheme,
# sparse+https:// and plain http://), so pruning either entry would silently
# delete that pass's coverage.
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
# condition that forces a checkout-safety collision without the
# revert/re-apply wrapper (ADR 0044, issue #2932), so phase_prework_rebase's
# `git rebase origin/main` has real conflicting-blob work to replay. Copied
# from the now-deleted tests/entrypoint-cargo-intree-binding.bats's own
# _advance_cargo_intree_config. Call after _seed_cargo_intree_config.
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

# Spawns the stand-in registry-proxy socat (the fixture faking the proxy's
# own unix socket, distinct from the real Forwarder driver-exec bind-registry
# itself spawns bridging to it) and exports REGISTRY_PROXY_MANIFEST (ADR
# 0045) naming its path -- the sole env var intree_binding_apply/
# phase_registry_proxy_bindings read now (issue #3141; entrypoint.sh no
# longer has its own REGISTRY_PROXY_SOCKET_PATH/_UPSTREAM_HOST vars to set).
# REGISTRY_PROXY_UPSTREAM_HOST stays a plain (non-exported) local here purely
# to parameterize both the manifest JSON below and this file's own
# _seed_cargo_intree_config/_advance_cargo_intree_config calls.
_start_stand_in_forwarder() {
  local _socket_path="$BATS_TEST_TMPDIR/registry-proxy.sock"
  REGISTRY_PROXY_UPSTREAM_HOST="cargo.mycorp.example"
  export REGISTRY_PROXY_MANIFEST="{\"endpoint\":\"unix://${_socket_path}\",\"routes\":[{\"prefix\":\"r0\",\"upstreamHost\":\"${REGISTRY_PROXY_UPSTREAM_HOST}\"}]}"

  socat "UNIX-LISTEN:$_socket_path,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  wait_for_socket "$_socket_path"
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
  # DISPATCH_KIND=research is the only dispatch kind whose main() (issue
  # #640, agent/entrypoint.sh:1483) skips the unconditional revert ->
  # phase_branch_recovery -> phase_prework_rebase -> re-apply dance right
  # after intree_binding_apply. A plain work dispatch would still clone and
  # apply, but the observed final file would be the RE-apply's output, not
  # this first apply's -- so it could pass even if intree_binding_apply
  # itself were broken. Research still clones and still calls
  # intree_binding_apply (tests/entrypoint-research-self-contained.bats), so
  # this is the one path that isolates the initial apply's own effect.
  export DISPATCH_KIND="research"
  _start_stand_in_forwarder

  _seed_cargo_intree_config "$REGISTRY_PROXY_UPSTREAM_HOST"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # Apply after clone: the committed cargo config's upstream host has been
  # rewritten to the local Forwarder endpoint. DISPATCH_KIND=research above
  # means no revert/rebase/re-apply ever runs, so this is genuinely the
  # first (and only) intree_binding_apply call's own output.
  grep -q "sparse+http://127.0.0.1:${_FIXED_FORWARDER_PORT}/index/" "$WORK_DIR/.cargo/config.toml"
  _assert_cargo_config_rewritten_and_hidden

  # The sourced bindings-env-output file's exports actually reach the fake
  # Driver's exec'd child process, not just the entrypoint shell.
  grep -q "env: npm_config_registry=http://127.0.0.1:${_FIXED_FORWARDER_PORT}/" "$DRIVER_LOG"

  # The sourced intree-bindings-env-output file's cargo placeholder token
  # export (ADR 0044's issue #3053 amendment) also reaches the fake Driver's
  # exec'd child process -- proving intree_binding_apply sources it, not just
  # the bindings-mode file above.
  grep -q "env: CARGO_REGISTRIES_OTHERCORP_TOKEN=spindrift-registry-proxy-placeholder-not-a-secret" "$DRIVER_LOG"
}

@test "bind-registry seam: revert -> branch-recovery -> re-apply ends pristine and rebound (issue #2935)" {
  _start_stand_in_forwarder

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

  # Final content must reflect BOTH the base's new registry entry (proving
  # the rebase actually replayed the base's change, not that it silently
  # kept stale pre-rebase content) AND the local-endpoint rewrite -- proving
  # re-apply ran again after the rebase, not stale pre-rebase content -- for
  # every entry: the original sparse+https one, the original plain http one,
  # and the one only the advanced base added.
  grep -q "\[registries.other\]" "$WORK_DIR/.cargo/config.toml"
  grep -q "sparse+http://127.0.0.1:${_FIXED_FORWARDER_PORT}/index/" "$WORK_DIR/.cargo/config.toml"
  grep -q "index = \"http://127.0.0.1:${_FIXED_FORWARDER_PORT}/other-index/\"" "$WORK_DIR/.cargo/config.toml"
  grep -q "sparse+http://127.0.0.1:${_FIXED_FORWARDER_PORT}/other-index/" "$WORK_DIR/.cargo/config.toml"
  _assert_cargo_config_rewritten_and_hidden

  # No stray unrelated files: revert -> rebase -> re-apply left nothing
  # dangling outside .cargo/config.toml itself, whose own rewrite the
  # skip-worktree assertion above already covers.
  [ -z "$(git -C "$WORK_DIR" status --short)" ]
}
