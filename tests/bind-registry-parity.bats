#!/usr/bin/env bats
# Sole bash-level coverage of the entrypoint to `driver-exec bind-registry`
# seam (issue #2935); the per-ecosystem suites deleted with the phase
# functions they tested (issue #2918). The verb is unit tested in Go; what
# this pins is the seam, via a fake driver-exec that execs the real binary.

# Cargo binds via source replacement, not an in-tree rewrite (issue #3201):
# the Target repo's committed .cargo/config.toml is the input
# CargoSourceReplacements reads, and the binding lands in
# $CARGO_HOME/config.toml instead. npm, yarn and pnpm still bind via the
# tracked in-tree rewrite and skip-worktree dance.

# bindregistry.ForwarderPort is one fixed constant (issue #3141), so both
# tests spawn a detached Forwarder on the same port. SpawnSocat detaches it
# (Setsid), so bats never reaps it: without _kill_leaked_forwarder, test 2
# would find test 1's listener already ready (EnsureForwarderReady probes
# before spawning) and bridge to test 1's torn-down socket.
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

# Kills this test's own Forwarder by the pid the driver printed on stdout.
# A no-op, not an error, when $_forwarder_pid is empty, which happens when
# EnsureForwarderReady's probe short-circuited before spawning;
# _kill_leaked_forwarder is the fallback for that case.
_kill_forwarder_by_pid() {
  case "${_forwarder_pid:-}" in
  '' | *[!0-9]*) return 0 ;;
  esac
  kill "$_forwarder_pid" 2>/dev/null
  true
}

# Fallback for when _kill_forwarder_by_pid had no pid: the already-ready
# short-circuit, or a process leaked by a prior run that was SIGKILLed before
# its teardown ran. Reads /proc rather than calling pkill or fuser, since
# neither is guaranteed on this harness's PATH.
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

# Seeds the remote's main branch with a committed .cargo/config.toml. Every
# stanza pins one scan path: [source.crates-io] has no `registry` key, so
# ParseCargoSourceDecls must pass over it; [source.proxy] claims the URL
# [registries.mirror] also declares, exercising issue #3248's source-name
# reuse; [registries.othercorp] is the unclaimed case that still mints a name.
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

# Advances main with a further commit that also modifies .cargo/config.toml,
# adding a second registry on the sparse+https scheme, so the plan must
# reflect the latest on-disk config once revert, rebase and re-apply land,
# not a cached pre-rebase read. Call after _seed_cargo_intree_config.
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

# Seeds main with a committed .npmrc naming the upstream host on both schemes.
# npm is still an in-tree rewrite row (issue #3201 retired only cargo's), so
# this fixture carries the rewrite, skip-worktree and revert story test 2
# pins. Call after setup_bare_repo.
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

# Advances main with a further commit that also modifies .npmrc, the
# condition that forces a checkout-safety collision without the revert and
# re-apply wrapper (ADR 0044, issue #2932), so phase_prework_rebase has real
# conflicting-blob work to replay. Call after _seed_npmrc_intree_config.
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

# Spawns the stand-in registry-proxy socat, the fixture faking the proxy's own
# unix socket, distinct from the real Forwarder that `driver-exec
# bind-registry` spawns to bridge to it.
_start_stand_in_forwarder() {
  local _socket_path="$BATS_TEST_TMPDIR/registry-proxy.sock"
  REGISTRY_PROXY_UPSTREAM_HOST="cargo.mycorp.example"
  # REGISTRY_PROXY_MANIFEST (ADR 0045) is the only env var
  # intree_binding_apply reads now (issue #3141). Routes are host-rooted
  # since issue #3261, so no registry here reuses the bare crates-io
  # replacement source. Bindings mode reads npm_config_registry out of the
  # "npm"-tagged enforcedPaths entry (issue #3259), "/" meaning the whole host.
  export REGISTRY_PROXY_MANIFEST="{\"endpoint\":\"unix://${_socket_path}\",\"routes\":[{\"prefix\":\"r0\",\"upstreamHost\":\"${REGISTRY_PROXY_UPSTREAM_HOST}\",\"enforcedPaths\":[{\"ecosystem\":\"npm\",\"path\":\"/\"}]}]}"

  socat "UNIX-LISTEN:$_socket_path,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  wait_for_socket "$_socket_path"
}

# Resolves $CARGO_HOME/config.toml the way resolveHomeConfigPath does for the
# cargo row: $CARGO_HOME if set, else $HOME/.cargo. setup_bare_repo exports
# HOME under $BATS_TEST_TMPDIR and nothing here sets CARGO_HOME, so this stays
# isolated per test without an override.
_cargo_home_config_path() {
  if [ -n "${CARGO_HOME:-}" ]; then
    echo "${CARGO_HOME}/config.toml"
  else
    echo "${HOME}/.cargo/config.toml"
  fi
}

# Proves the tracked .cargo/config.toml is left alone by the whole run (issue
# #3201: source replacement reads this file, it never rewrites it). Compares
# byte-for-byte against HEAD's blob rather than grepping for the host, which a
# rewrite that left the host string behind would slip past. The skip-worktree
# check proves the file was never hidden from git status either.
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

# Issue #3201 moved the in-tree rewrite role from cargo onto npm, so .npmrc is
# where the upstream host must be rewritten away and the skip-worktree bit set.
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
  # research is the only dispatch kind whose main() (issue #640) skips the
  # revert, branch-recovery, rebase and re-apply dance that otherwise runs
  # right after intree_binding_apply. Under a work dispatch the final file
  # would be the re-apply's output, so this test could pass with the first
  # apply broken. Research still clones and still applies.
  export DISPATCH_KIND="research"
  _start_stand_in_forwarder

  _seed_cargo_intree_config "$REGISTRY_PROXY_UPSTREAM_HOST"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # A non-empty pid proves SpawnSocat ran for this test rather than
  # EnsureForwarderReady's short-circuit reusing another process's still
  # listening Forwarder. teardown() kills it by this pid.
  _forwarder_pid="$(grep -oE 'Forwarder pid [0-9]+' <<<"$output" | grep -oE '[0-9]+' || true)"
  [ -n "$_forwarder_pid" ]

  # The committed .cargo/config.toml is the input, not the rewrite target.
  _assert_cargo_config_untouched

  # The rendered local index URL is rooted at "othercorp"'s real upstream
  # index path, since a host-rooted route serves the upstream host's own path
  # layout.
  local _cargo_home_config
  _cargo_home_config="$(_cargo_home_config_path)"
  grep -q '\[source\.spindrift-upstream-othercorp\]' "$_cargo_home_config"
  grep -q 'registry = "http://cargo.mycorp.example/other-index/"' "$_cargo_home_config"
  grep -q 'replace-with = "spindrift-registry-proxy-r0-othercorp"' "$_cargo_home_config"
  grep -q '\[registries\.spindrift-registry-proxy-r0-othercorp\]' "$_cargo_home_config"
  grep -q "index = \"sparse+http://127.0.0.1:${_FIXED_FORWARDER_PORT}/r0/other-index/\"" "$_cargo_home_config"

  # "mirror" declares the index URL the repo's [source.proxy] stanza already
  # claims (issue #3248), so the render reuses the repo's "proxy" name. A
  # second stanza for the same URL would break cargo's one-source-per-URL
  # rule and be rejected as a duplicate.
  grep -q '\[source\.proxy\]' "$_cargo_home_config"
  grep -q 'registry = "sparse+https://cargo.mycorp.example/index/"' "$_cargo_home_config"
  ! grep -q '\[source\.spindrift-upstream-mirror\]' "$_cargo_home_config"

  # The bindings-env-output exports reach the fake Driver's exec'd child, not
  # just the entrypoint shell. Bindings mode has no per-ecosystem route
  # mapping (issue #3142), so it binds to the first route's "r0" prefix.
  grep -q "env: npm_config_registry=http://127.0.0.1:${_FIXED_FORWARDER_PORT}/r0/" "$DRIVER_LOG"

  # The cargo placeholder token export (ADR 0044's issue #3053 amendment,
  # re-keyed to the proxy source name by issue #3201) reaches that child too,
  # proving intree_binding_apply sources its own file and not just the
  # bindings-mode one above. One export per minted source since issue #3261.
  grep -q "env: CARGO_REGISTRIES_SPINDRIFT_REGISTRY_PROXY_R0_OTHERCORP_TOKEN=spindrift-registry-proxy-placeholder-not-a-secret" "$DRIVER_LOG"
}

@test "bind-registry seam: revert -> branch-recovery -> re-apply ends pristine and rebound (issue #2935)" {
  _start_stand_in_forwarder

  _seed_cargo_intree_config "$REGISTRY_PROXY_UPSTREAM_HOST"
  _seed_npmrc_intree_config "$REGISTRY_PROXY_UPSTREAM_HOST"

  # Stale prior work: agent/issue-7 branches off the pre-advance commit, with
  # unrelated work that never touches .cargo/config.toml or .npmrc.
  local prior="$BATS_TEST_TMPDIR/prior"
  git clone -q "https://github.com/owner/repo.git" "$prior"
  git -C "$prior" checkout -b "agent/issue-7" "origin/main"
  echo "branch work" > "$prior/branch.txt"
  git -C "$prior" add branch.txt
  git -C "$prior" commit -q -m "feat: prior run work"
  git -C "$prior" push -q origin "agent/issue-7"

  # origin/main advances further so the committed blob for each path differs
  # between the branch being rebased and the base it rebases onto.
  _advance_cargo_intree_config "$REGISTRY_PROXY_UPSTREAM_HOST"
  _advance_npmrc_intree_config "$REGISTRY_PROXY_UPSTREAM_HOST"

  # An open PR makes the run adopt the existing branch, so
  # phase_prework_rebase has real work to replay.
  export FAKE_GH_PR_LIST_7="https://github.com/owner/repo/pull/7"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # A non-empty pid proves this run spawned a fresh Forwarder rather than
  # reusing one the sibling test left listening.
  _forwarder_pid="$(grep -oE 'Forwarder pid [0-9]+' <<<"$output" | grep -oE '[0-9]+' || true)"
  [ -n "$_forwarder_pid" ]

  # The rebase replayed the branch's own work onto the advanced base.
  [ -f "$WORK_DIR/branch.txt" ]

  # .cargo/config.toml is still the rebased input, never rewritten.
  _assert_cargo_config_untouched

  # $CARGO_HOME/config.toml must carry both the original "othercorp" registry
  # and the "other" only the advanced base added, proving the re-render read
  # the latest on-disk content rather than cached pre-rebase state.
  local _cargo_home_config
  _cargo_home_config="$(_cargo_home_config_path)"
  grep -q '\[source\.spindrift-upstream-othercorp\]' "$_cargo_home_config"
  grep -q '\[source\.spindrift-upstream-other\]' "$_cargo_home_config"
  grep -q 'registry = "sparse+https://cargo.mycorp.example/other-index/"' "$_cargo_home_config"
  grep -q "index = \"sparse+http://127.0.0.1:${_FIXED_FORWARDER_PORT}/r0/other-index/\"" "$_cargo_home_config"

  # "other" and "othercorp" name the same upstream index path on two schemes,
  # so both share the proxy source the first of them minted. Counted rather
  # than grepped: a repeated [registries.…] name is a TOML error cargo
  # refuses to parse, not a merge.
  [ "$(grep -c '\[registries\.spindrift-registry-proxy-r0-othercorp\]' "$_cargo_home_config")" -eq 1 ]

  # Every .npmrc entry must show both the base's new scoped entry, proving
  # the rebase replayed the base's change, and the local-endpoint rewrite,
  # proving re-apply ran again after the rebase.
  grep -q "@mycorp:registry=http://127.0.0.1:${_FIXED_FORWARDER_PORT}/r0/" "$WORK_DIR/.npmrc"
  grep -q "^registry=http://127.0.0.1:${_FIXED_FORWARDER_PORT}/r0/" "$WORK_DIR/.npmrc"
  grep -q "@another:registry=http://127.0.0.1:${_FIXED_FORWARDER_PORT}/r0/another/" "$WORK_DIR/.npmrc"
  _assert_npmrc_rewritten_and_hidden

  # revert, rebase and re-apply left nothing dangling outside .npmrc, whose
  # own rewrite the skip-worktree assertion above covers.
  [ -z "$(git -C "$WORK_DIR" status --short)" ]
}
