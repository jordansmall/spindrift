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
# Cargo binds via source replacement, not an in-tree rewrite (issue #3201):
# the Target repo's own committed .cargo/config.toml stays untouched -- it's
# the *input* CargoSourceReplacements keys off, read post-clone from
# $WORK_DIR -- and the binding itself lands in $CARGO_HOME/config.toml
# instead. npm/yarn/pnpm are unaffected: they still bind via the tracked
# in-tree rewrite + skip-worktree hide/revert dance this suite has always
# pinned.
#
# Two tests cover this seam:
#   - "apply after clone re-renders $CARGO_HOME/config.toml from the
#     un-rewritten repo config and the sourced placeholder reaches the
#     Driver's child" -- pins the initial intree_binding_apply
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
#     only thing that could have produced the observed render.
#   - "revert -> branch-recovery -> re-apply ends pristine and rebound" --
#     the harness-driven rebase story on top of that already-proven apply
#     mechanism, run as an ordinary work dispatch so the revert/rebase/
#     re-apply block actually executes. Cargo no longer participates in the
#     skip-worktree/revert dance (it never rewrites the tracked file), so
#     this test's rewrite/skip-worktree/revert assertions now pin .npmrc --
#     npm is still an in-tree row -- while the cargo config's own
#     assertions stay on the "untouched input, re-rendered $CARGO_HOME
#     output" shape the other test already established.
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
  _kill_forwarder_by_pid
  _kill_leaked_forwarder
}

# Kills the Forwarder this @test's own `driver-exec bind-registry` call
# spawned, by the exact pid the driver printed on stdout ("==> registry proxy
# Forwarder pid <N>", captured into $_forwarder_pid right after each `run
# bash "$ENTRYPOINT"` below). This is the primary cleanup mechanism -- it's a
# no-op (not an error) when $_forwarder_pid is empty, which happens on the
# already-ready short-circuit (EnsureForwarderReady's probe found a listener
# before spawning, so no pid was ever printed); _kill_leaked_forwarder below
# is the fallback for that case.
_kill_forwarder_by_pid() {
  case "${_forwarder_pid:-}" in
  '' | *[!0-9]*) return 0 ;;
  esac
  kill "$_forwarder_pid" 2>/dev/null
  true
}

# Fallback for when _kill_forwarder_by_pid above had no pid to work with:
# either the already-ready short-circuit (no pid was ever printed this run),
# or a process leaked by a prior run that was SIGKILLed before its own
# teardown ran (this run never saw that pid at all). Kills any detached
# Forwarder socat process left listening on the fixed
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

# Seeds the remote's main branch with a committed .cargo/config.toml naming
# one private registry ("othercorp") at $REGISTRY_PROXY_UPSTREAM_HOST, plus a
# second registry ("mirror") whose index is byte-identical to the
# [source.proxy] stanza's own `registry` value. Since issue #3201 this file
# is never rewritten -- it's the input CargoSourceReplacements parses
# post-clone -- so, unlike its npmrc counterpart below, this fixture needs no
# rewritten/hidden assertion of its own; _assert_cargo_config_untouched
# (below) is what both @tests run against it instead.
#
# Each stanza here earns its place against one of the two line-based scans
# (issue #3201's ParseCargoRegistryDecls, issue #3248's ParseCargoSourceDecls),
# not as filler:
#   - [source.crates-io] carries only `replace-with`, no `registry` key, so
#     it proves ParseCargoSourceDecls' scan passes over a [source.*] table
#     lacking the key it looks for instead of misreading it as a claim.
#   - [source.proxy] DOES claim a real registry URL, and [registries.mirror]
#     (below) declares that exact same URL -- together they exercise
#     issue #3248's source-name-reuse path: CargoSourceReplacements reuses
#     the repo's own "proxy" name rather than minting
#     "spindrift-upstream-mirror".
#   - [registries.othercorp] stays the unclaimed case the fixture pinned
#     before #3248: no [source.*] stanza claims its URL, so it still mints
#     "spindrift-upstream-othercorp" exactly as before.
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

[registries.mirror]
index = "sparse+https://${host}/index/"

[registries.othercorp]
index = "http://${host}/other-index/"
EOF
  git -C "$seed" add .cargo/config.toml
  git -C "$seed" commit -q -m "chore: pin private cargo registry"
  git -C "$seed" push -q origin HEAD:main
}

# Advances the remote's main branch with a further commit that ALSO modifies
# .cargo/config.toml, appending a second [registries.*] block ("other", on
# the sparse+https scheme this time, "othercorp" above being plain http) --
# proving CargoSourceReplacements' plan reflects whatever the very latest
# on-disk repo config says once the whole revert/rebase/re-apply dance
# lands, not some cached pre-rebase read. Call after
# _seed_cargo_intree_config.
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

# Seeds the remote's main branch with a committed .npmrc naming
# $REGISTRY_PROXY_UPSTREAM_HOST on both schemes (a scoped registry entry on
# https, an unscoped one on http) -- npm is still an in-tree rewrite row
# (issue #3201 only retired cargo's), so this is the fixture that now carries
# the rewrite/skip-worktree/revert story test 2 below pins. Call after
# setup_bare_repo.
_seed_npmrc_intree_config() {
  local host="$1"
  local seed="$BATS_TEST_TMPDIR/seed-npmrc"
  git clone -q "https://github.com/owner/repo.git" "$seed"
  cat >"$seed/.npmrc" <<EOF
@mycorp:registry=https://${host}/
registry=http://${host}/
EOF
  git -C "$seed" add .npmrc
  git -C "$seed" commit -q -m "chore: pin private npm registry"
  git -C "$seed" push -q origin HEAD:main
}

# Advances the remote's main branch with a further commit that ALSO modifies
# .npmrc, appending a second scoped entry -- the specific condition that
# forces a checkout-safety collision without the revert/re-apply wrapper
# (ADR 0044, issue #2932), so phase_prework_rebase's `git rebase origin/main`
# has real conflicting-blob work to replay. Call after
# _seed_npmrc_intree_config.
_advance_npmrc_intree_config() {
  local host="$1"
  local seed="$BATS_TEST_TMPDIR/seed-npmrc-advance"
  git clone -q "https://github.com/owner/repo.git" "$seed"
  cat >>"$seed/.npmrc" <<EOF
@another:registry=https://${host}/another/
EOF
  git -C "$seed" add .npmrc
  git -C "$seed" commit -q -m "chore: pin second private npm registry"
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
# _seed_*_intree_config/_advance_*_intree_config calls. The manifest's sole
# route uses prefix "r0", and every route is host-rooted since issue #3261:
# each declared registry resolves through its own local URL carrying that
# registry's real upstream index path, under its own minted
# "spindrift-registry-proxy-r0-<registry>" source. So no named registry here
# reuses CargoConfigTOML's own crates-io replacement source
# ("spindrift-registry-proxy", rendered at the bare "/r0/" local URL) --
# nothing in this fixture's config is served from the upstream host's root.
#
# The route also carries one "npm"-tagged enforcedPaths entry, since bindings
# mode reads npm_config_registry's own value out of that list (issue #3259)
# rather than from the route root. "/" is registrypathset's rendering of a
# whole-host declaration, which is exactly what this fixture's own .npmrc
# declares ("registry=http://<host>/"), and NpmFamilyBindings renders it back
# to the bare route root.
_start_stand_in_forwarder() {
  local _socket_path="$BATS_TEST_TMPDIR/registry-proxy.sock"
  REGISTRY_PROXY_UPSTREAM_HOST="cargo.mycorp.example"
  export REGISTRY_PROXY_MANIFEST="{\"endpoint\":\"unix://${_socket_path}\",\"routes\":[{\"prefix\":\"r0\",\"upstreamHost\":\"${REGISTRY_PROXY_UPSTREAM_HOST}\",\"enforcedPaths\":[{\"ecosystem\":\"npm\",\"path\":\"/\"}]}]}"

  socat "UNIX-LISTEN:$_socket_path,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  wait_for_socket "$_socket_path"
}

# Resolves $CARGO_HOME/config.toml the same way runBindRegistryRepoAwareHomeConfigs'
# shared resolveHomeConfigPath helper does for the cargo row: $CARGO_HOME if
# set, else $HOME/.cargo. setup_bare_repo (via setup_entrypoint_env) exports
# HOME under $BATS_TEST_TMPDIR, and neither entrypoint.sh nor this suite ever
# sets CARGO_HOME, so this always lands under $BATS_TEST_TMPDIR too --
# already isolated per-test, no override needed.
_cargo_home_config_path() {
  if [ -n "${CARGO_HOME:-}" ]; then
    echo "${CARGO_HOME}/config.toml"
  else
    echo "${HOME}/.cargo/config.toml"
  fi
}

# Shared by both @tests: proves the tracked .cargo/config.toml is genuinely
# left alone by the whole run (issue #3201's non-composing invariant --
# source replacement keys off this file, it never rewrites it). Byte-for-byte
# against HEAD's own committed blob, via `git diff --quiet`, not just a
# substring grep: a rewrite that happened to leave "cargo.mycorp.example"
# somewhere in the file (e.g. only $CARGO_HOME's copy changed) would slip
# past a weaker check. `git ls-files -v` must also report no skip-worktree
# ('S') prefix -- the file was never hidden from git status either.
_assert_cargo_config_untouched() {
  local _lsfiles
  _lsfiles="$(git -C "$WORK_DIR" ls-files -v .cargo/config.toml)"
  if [[ "$_lsfiles" == S* ]]; then
    echo "expected .cargo/config.toml to carry no skip-worktree bit, but ls-files -v reported: $_lsfiles" >&2
    return 1
  fi

  if ! git -C "$WORK_DIR" diff --quiet -- .cargo/config.toml; then
    echo "expected .cargo/config.toml to stay byte-identical to HEAD's own blob, but it differs" >&2
    return 1
  fi

  grep -q "cargo.mycorp.example" "$WORK_DIR/.cargo/config.toml"
}

# npm's own counterpart to the retired _assert_cargo_config_rewritten_and_hidden
# (issue #3201 moved that role from cargo's in-tree config onto npm's, the
# only other row this suite drives through the same file): the upstream host
# is gone from .npmrc and the skip-worktree bit is set.
_assert_npmrc_rewritten_and_hidden() {
  if grep -q "cargo.mycorp.example" "$WORK_DIR/.npmrc"; then
    echo "expected cargo.mycorp.example to be rewritten away from .npmrc, but it is still present" >&2
    return 1
  fi

  # `git ls-files -v` prefixes a skip-worktree path with uppercase 'S'.
  local _lsfiles
  _lsfiles="$(git -C "$WORK_DIR" ls-files -v .npmrc)"
  [[ "$_lsfiles" == S* ]]
}

@test "bind-registry seam: apply after clone re-renders \$CARGO_HOME/config.toml from the un-rewritten repo config and the sourced placeholder reaches the Driver's child (issue #2935)" {
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

  # Captures the pid driver-exec bind-registry printed for the Forwarder it
  # spawned (see teardown()'s _kill_forwarder_by_pid above) and asserts it's
  # non-empty -- proving SpawnSocat genuinely ran this @test rather than
  # EnsureForwarderReady's already-ready short-circuit reusing some other
  # process's still-listening Forwarder.
  _forwarder_pid="$(grep -oE 'Forwarder pid [0-9]+' <<<"$output" | grep -oE '[0-9]+' || true)"
  [ -n "$_forwarder_pid" ]

  # The committed .cargo/config.toml itself is untouched -- it's the input,
  # not the rewrite target.
  _assert_cargo_config_untouched

  # $CARGO_HOME/config.toml carries the source-replacement stanzas
  # CargoRepoAwareConfig derived from that un-rewritten input: the real
  # upstream index for "othercorp", replaced-with the
  # "spindrift-registry-proxy-r0-othercorp" source that registry mints for
  # itself, and that source's own [registries.…] table naming the Forwarder's
  # local index URL -- rooted at "othercorp"'s real upstream index path, since
  # a host-rooted route serves the upstream host's own path layout.
  local _cargo_home_config
  _cargo_home_config="$(_cargo_home_config_path)"
  grep -q '\[source\.spindrift-upstream-othercorp\]' "$_cargo_home_config"
  grep -q 'registry = "http://cargo.mycorp.example/other-index/"' "$_cargo_home_config"
  grep -q 'replace-with = "spindrift-registry-proxy-r0-othercorp"' "$_cargo_home_config"
  grep -q '\[registries\.spindrift-registry-proxy-r0-othercorp\]' "$_cargo_home_config"
  grep -q "index = \"sparse+http://127.0.0.1:${_FIXED_FORWARDER_PORT}/r0/other-index/\"" "$_cargo_home_config"

  # "mirror" declares the same index URL the repo's own [source.proxy]
  # stanza already claims (issue #3248), so the rendered config reuses the
  # repo's "proxy" name instead of minting a second [source.…] stanza for
  # the same URL -- which cargo's URL->source-name 1:1 rule would reject as
  # a duplicate source outright.
  grep -q '\[source\.proxy\]' "$_cargo_home_config"
  grep -q 'registry = "sparse+https://cargo.mycorp.example/index/"' "$_cargo_home_config"
  ! grep -q '\[source\.spindrift-upstream-mirror\]' "$_cargo_home_config"

  # The sourced bindings-env-output file's exports actually reach the fake
  # Driver's exec'd child process, not just the entrypoint shell. Bindings
  # mode has no per-ecosystem route mapping (issue #3142), so it binds to
  # the first (only) manifest route's own "r0" prefix, set by
  # _start_stand_in_forwarder above.
  grep -q "env: npm_config_registry=http://127.0.0.1:${_FIXED_FORWARDER_PORT}/r0/" "$DRIVER_LOG"

  # The sourced intree-bindings-env-output file's cargo placeholder token
  # export (ADR 0044's issue #3053 amendment, re-keyed to the proxy source
  # name by issue #3201) also reaches the fake Driver's exec'd child process
  # -- proving intree_binding_apply sources it, not just the bindings-mode
  # file above. One export per minted proxy source since issue #3261, so
  # "othercorp"'s own is what the fake Driver reports.
  grep -q "env: CARGO_REGISTRIES_SPINDRIFT_REGISTRY_PROXY_R0_OTHERCORP_TOKEN=spindrift-registry-proxy-placeholder-not-a-secret" "$DRIVER_LOG"
}

@test "bind-registry seam: revert -> branch-recovery -> re-apply ends pristine and rebound (issue #2935)" {
  _start_stand_in_forwarder

  _seed_cargo_intree_config "$REGISTRY_PROXY_UPSTREAM_HOST"
  _seed_npmrc_intree_config "$REGISTRY_PROXY_UPSTREAM_HOST"

  # Stale prior work: agent/issue-7 branches off the pre-advance commit,
  # with unrelated work of its own that never touches .cargo/config.toml or
  # .npmrc.
  local prior="$BATS_TEST_TMPDIR/prior"
  git clone -q "https://github.com/owner/repo.git" "$prior"
  git -C "$prior" checkout -b "agent/issue-7" "origin/main"
  echo "branch work" > "$prior/branch.txt"
  git -C "$prior" add branch.txt
  git -C "$prior" commit -q -m "feat: prior run work"
  git -C "$prior" push -q origin "agent/issue-7"

  # origin/main advances further, touching both .cargo/config.toml and
  # .npmrc -- so the committed blob for each path now genuinely differs
  # between the branch being rebased and the base it rebases onto.
  _advance_cargo_intree_config "$REGISTRY_PROXY_UPSTREAM_HOST"
  _advance_npmrc_intree_config "$REGISTRY_PROXY_UPSTREAM_HOST"

  # Open PR so the adoption path is taken (git checkout -b agent/issue-7
  # origin/agent/issue-7), so phase_prework_rebase's `git rebase origin/main`
  # has real work to replay.
  export FAKE_GH_PR_LIST_7="https://github.com/owner/repo/pull/7"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # Captures this @test's own Forwarder pid (see the sibling @test above for
  # why) and asserts it's non-empty -- proving this @test's own run spawned a
  # fresh Forwarder rather than reusing one still listening from the sibling
  # @test above (teardown() there should have killed it by pid already).
  _forwarder_pid="$(grep -oE 'Forwarder pid [0-9]+' <<<"$output" | grep -oE '[0-9]+' || true)"
  [ -n "$_forwarder_pid" ]

  # The rebase must have actually replayed the branch's own work onto the
  # advanced base.
  [ -f "$WORK_DIR/branch.txt" ]

  # .cargo/config.toml is still just the (rebased) input, never rewritten.
  _assert_cargo_config_untouched

  # $CARGO_HOME/config.toml reflects the FINAL, post-rebase repo config --
  # both the original "othercorp" registry and the one only the advanced
  # base added ("other") -- proving the repo-aware re-render (which runs
  # after revert -> rebase -> re-apply, not before) read the latest on-disk
  # content, not some cached pre-rebase state.
  local _cargo_home_config
  _cargo_home_config="$(_cargo_home_config_path)"
  grep -q '\[source\.spindrift-upstream-othercorp\]' "$_cargo_home_config"
  grep -q '\[source\.spindrift-upstream-other\]' "$_cargo_home_config"
  grep -q 'registry = "sparse+https://cargo.mycorp.example/other-index/"' "$_cargo_home_config"
  grep -q "index = \"sparse+http://127.0.0.1:${_FIXED_FORWARDER_PORT}/r0/other-index/\"" "$_cargo_home_config"

  # "other" and "othercorp" name the same upstream index path on two schemes,
  # so both resolve through one local URL and share the proxy source the first
  # of them minted. Counted, not just grepped: the shared [registries.…] table
  # must be rendered once, a name repeated in one file being a TOML error
  # cargo refuses to parse rather than a merge.
  [ "$(grep -c '\[registries\.spindrift-registry-proxy-r0-othercorp\]' "$_cargo_home_config")" -eq 1 ]

  # .npmrc now carries the story .cargo/config.toml used to: final content
  # must reflect BOTH the base's new scoped entry (proving the rebase
  # actually replayed the base's change, not that it silently kept stale
  # pre-rebase content) AND the local-endpoint rewrite -- proving re-apply
  # ran again after the rebase, not stale pre-rebase content -- for every
  # entry: the original scoped one, the original unscoped one, and the one
  # only the advanced base added.
  grep -q "@mycorp:registry=http://127.0.0.1:${_FIXED_FORWARDER_PORT}/r0/" "$WORK_DIR/.npmrc"
  grep -q "^registry=http://127.0.0.1:${_FIXED_FORWARDER_PORT}/r0/" "$WORK_DIR/.npmrc"
  grep -q "@another:registry=http://127.0.0.1:${_FIXED_FORWARDER_PORT}/r0/another/" "$WORK_DIR/.npmrc"
  _assert_npmrc_rewritten_and_hidden

  # No stray unrelated files: revert -> rebase -> re-apply left nothing
  # dangling outside .npmrc itself, whose own rewrite the skip-worktree
  # assertion above already covers.
  [ -z "$(git -C "$WORK_DIR" status --short)" ]
}
