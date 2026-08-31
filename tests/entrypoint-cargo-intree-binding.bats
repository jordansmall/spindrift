#!/usr/bin/env bats
# In-tree cargo config binding (ADR 0044, issue #2932): a Target repo can
# commit its own $WORK_DIR/.cargo/config.toml pinning a private registry
# directly (e.g. `[registries.NAME] index =
# "sparse+https://cargo.mycorp.example/index/"`). In-tree cargo config wins
# over the user-level $CARGO_HOME/config.toml phase_registry_proxy_bindings
# writes (rendered by bindregistry.CargoConfigTOML, tested in
# cmd/launcher/internal/bindregistry/registrybindings_test.go), and cargo's
# table-valued [source]/[registries] keys aren't overridable via CARGO_* env
# vars (cargo#5416) -- so this rewrite has to edit the committed file itself.
#
# Issue #2932 moved the rewrite/revert mechanics from a deleted bash phase
# (phase_cargo_intree_binding_apply, along with the old bats suite that
# covered it) into a Go engine
# (ApplyInTreeBinding/RevertInTreeBinding,
# cmd/launcher/internal/bindregistry/intreebinding.go), driven from
# agent/entrypoint.sh's two thin wrappers intree_binding_apply/
# intree_binding_revert via `driver-exec bind-registry --intree-action
# apply|revert --intree-work-dir ...`. Issue #2933 then generalized that same
# Go engine to a table of rows (cargo, npm, yarn, pnpm --
# bindregistry.InTreeBindings()) and deleted the equivalent bash phases for
# npm/yarn/pnpm too, so every ecosystem's in-tree rewrite goes through this
# one engine now. This suite re-pins the choreography at the entrypoint.sh
# call-site level -- the four places intree_binding_apply/
# intree_binding_revert are actually called from (clone_repo's initial pass;
# the revert/re-apply pair around phase_branch_recovery/phase_prework_rebase
# in main(); and phase_conflict_resolve's defensive revert-before-abort) --
# using cargo's own config file as the one fixture, since ApplyInTreeBinding
# is ecosystem-agnostic and the Go-side per-row coverage already lives in
# cmd/launcher/internal/bindregistry/intreebinding_test.go.
#
# Cargo's row spawns/probes a Forwarder via `socat` exactly like
# phase_registry_proxy_bindings' own bindings mode does (see
# tests/entrypoint-pnpm-intree-binding.bats) -- so this suite uses
# the shared helper.bash `wait_for_socket`/`kill_stand_in_socat` stand-in
# socat pattern those suites use.
#
# REGISTRY_PROXY_FORWARDER_PORT=27191 here is distinct from every port
# already claimed elsewhere in this suite's siblings (grep
# `REGISTRY_PROXY_FORWARDER_PORT=` across tests/*.bats: 27184 npm, 27185
# registry-proxy-bindings, 27188 yarn-classic, 27189 yarn-berry, 27190
# pnpm), so a socat TCP-LISTEN bind never collides if bats sharding runs
# multiple *.bats files concurrently.

load helper

setup() {
  setup_entrypoint_env
}

teardown() {
  kill_stand_in_socat
}

# Seeds the remote's main branch with a committed .cargo/config.toml pinning
# a private registry at $REGISTRY_PROXY_UPSTREAM_HOST via BOTH a
# sparse+https:// index URL and a plain http:// (no "s") one -- mirroring
# cmd/launcher/internal/bindregistry/intreebinding_test.go's own
# TestApplyInTreeBinding_MultipleReferences fixture, since ApplyInTreeBinding
# does two separate ReplaceAll passes (one for the "https://" prefix, one for
# the bare "http://" prefix) and a single-scheme fixture would leave one pass
# entirely unexercised by this suite. Call after setup_bare_repo.
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

@test "in-tree cargo config referencing the upstream host is rewritten and hidden via skip-worktree (issue #2932)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27191
  export REGISTRY_PROXY_UPSTREAM_HOST="cargo.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  _seed_cargo_intree_config "$REGISTRY_PROXY_UPSTREAM_HOST"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ -f "$WORK_DIR/.cargo/config.toml" ]
  # The sparse+https:// entry: "https://" is a substring of "sparse+https://",
  # so the httpsFrom ReplaceAll pass rewrites it in place, leaving the
  # "sparse+" prefix untouched.
  grep -q "sparse+http://127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}/index/" "$WORK_DIR/.cargo/config.toml"
  # The plain http:// entry: only the httpFrom pass matches this one.
  grep -q "index = \"http://127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}/other-index/\"" "$WORK_DIR/.cargo/config.toml"
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

@test "skip-worktree path is never stageable (issue #2932)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27191
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

@test "no-op when no .cargo/config.toml exists (issue #2932)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27191
  export REGISTRY_PROXY_UPSTREAM_HOST="cargo.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ ! -f "$WORK_DIR/.cargo/config.toml" ]
}

@test "no-op when .cargo/config.toml exists but is untracked (issue #2932)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27191
  export REGISTRY_PROXY_UPSTREAM_HOST="cargo.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  # clone_repo's own `git clone "$CLONE_URL" "$WORK_DIR"` requires $WORK_DIR
  # to not already exist/be non-empty, so an untracked .cargo/config.toml
  # can't just be pre-seeded there before the entrypoint runs -- it has to
  # land in the working tree as part of the clone itself, without ever being
  # a committed blob. A git template dir's post-checkout hook runs, cwd
  # already $WORK_DIR, right after `git clone`'s own initial checkout
  # populates the tracked files, so writing .cargo/config.toml there (never
  # `git add`ed) reproduces an untracked file exactly as a real
  # .gitignore'd one would land -- ApplyInTreeBinding's own isTracked check
  # (`git ls-files --error-unmatch`) then sees it as untracked and returns
  # early rather than rewriting/skip-worktree-hiding a path git never tracked
  # in the first place.
  local _content='[registries.mycorp]
index = "sparse+https://'"${REGISTRY_PROXY_UPSTREAM_HOST}"'/index/"'
  local _tmpl="$BATS_TEST_TMPDIR/git-template"
  mkdir -p "$_tmpl/hooks"
  cat >"$_tmpl/hooks/post-checkout" <<EOF
#!/bin/sh
mkdir -p .cargo
printf '%s\n' '$_content' >.cargo/config.toml
EOF
  chmod +x "$_tmpl/hooks/post-checkout"
  git config --global init.templateDir "$_tmpl"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ -f "$WORK_DIR/.cargo/config.toml" ]
  # Untouched: the engine must have returned before ever reaching the
  # content rewrite, so the upstream host is still present verbatim.
  [ "$(cat "$WORK_DIR/.cargo/config.toml")" = "$_content" ]

  local _lsfiles
  _lsfiles="$(git -C "$WORK_DIR" ls-files -v .cargo/config.toml)"
  [[ "$_lsfiles" != S* ]]
}

@test "no-op when committed cargo config doesn't reference the configured host (issue #2932)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27191
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

# Advances the remote's main branch with a further commit that ALSO modifies
# .cargo/config.toml, appending a second [registries.*] block. Call after
# _seed_cargo_intree_config: this is the specific condition (the committed
# blob for this path genuinely differs between the branch being rebased and
# the base it's rebasing onto) that triggers git's checkout-safety collision
# without the revert/re-apply wrapper (ADR 0044, issue #2932).
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

@test "harness-driven rebase with divergent .cargo/config.toml across branches succeeds with the rewrite in place (issue #2932)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27191
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
  # stale pre-rebase content) AND the local-endpoint rewrite, for every entry
  # -- the original sparse+https one, the original plain http one, and the
  # one only the advanced base added.
  grep -q "registries.other" "$WORK_DIR/.cargo/config.toml"
  grep -q "sparse+http://127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}/index/" "$WORK_DIR/.cargo/config.toml"
  grep -q "index = \"http://127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}/other-index/\"" "$WORK_DIR/.cargo/config.toml"
  grep -q "sparse+http://127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}/other-index/" "$WORK_DIR/.cargo/config.toml"
  if grep -q "cargo.mycorp.example" "$WORK_DIR/.cargo/config.toml"; then
    echo "expected cargo.mycorp.example to be rewritten away, but it is still present" >&2
    return 1
  fi

  local _lsfiles
  _lsfiles="$(git -C "$WORK_DIR" ls-files -v .cargo/config.toml)"
  [[ "$_lsfiles" == S* ]]

  [ -z "$(git -C "$WORK_DIR" status --short)" ]
}

@test "phase_conflict_resolve reverts in-tree cargo config rewrite before aborting an unresolvable rebase conflict (issue #2932)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27191
  export REGISTRY_PROXY_UPSTREAM_HOST="cargo.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  _seed_cargo_intree_config "$REGISTRY_PROXY_UPSTREAM_HOST"

  # Unrelated README conflict (mirrors entrypoint-branch-recovery.bats'
  # setup_rebase_conflict) -- .cargo/config.toml itself is untouched by
  # either side, so it rides through the stopped rebase unchanged and
  # intree_binding_apply's second call site (right after phase_prework_rebase,
  # main()) rewrites and skip-worktrees it again while the rebase is still
  # mid-conflict, exactly the state intree_binding_revert's call from
  # phase_conflict_resolve must clean up before aborting.
  local prior="$BATS_TEST_TMPDIR/prior"
  git clone -q "https://github.com/owner/repo.git" "$prior"
  git -C "$prior" checkout -b "agent/issue-7" "origin/main"
  printf "branch version\n" > "$prior/README.md"
  git -C "$prior" add README.md
  git -C "$prior" commit -q -m "feat: branch modifies README"
  git -C "$prior" push -q origin "agent/issue-7"

  local advance="$BATS_TEST_TMPDIR/advance"
  git clone -q "https://github.com/owner/repo.git" "$advance"
  printf "main version\n" > "$advance/README.md"
  git -C "$advance" add README.md
  git -C "$advance" commit -q -m "chore: main modifies README (conflicts)"
  git -C "$advance" push -q origin HEAD:main

  export FAKE_GH_PR_LIST_7="https://github.com/owner/repo/pull/7"
  # No FAKE_DRIVER_RESOLVE_CONFLICT -- stub does not complete the rebase, so
  # phase_conflict_resolve still finds .git/rebase-merge and takes the
  # revert-then-abort branch.

  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"pre-work rebase"* ]]

  # `git rebase --abort` hard-resets the working tree, but a skip-worktree
  # path is one git deliberately leaves alone on the assumption it already
  # matches the index -- so if intree_binding_revert hadn't cleared the bit
  # and restored the original content first, the rewritten 127.0.0.1
  # endpoint and the skip-worktree bit would both still be here after abort.
  [ -f "$WORK_DIR/.cargo/config.toml" ]
  grep -q "sparse+https://${REGISTRY_PROXY_UPSTREAM_HOST}/index/" "$WORK_DIR/.cargo/config.toml"
  grep -q "index = \"http://${REGISTRY_PROXY_UPSTREAM_HOST}/other-index/\"" "$WORK_DIR/.cargo/config.toml"
  if grep -q "127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}" "$WORK_DIR/.cargo/config.toml"; then
    echo "expected the in-tree cargo config rewrite to have been reverted before the rebase abort, but it is still present" >&2
    return 1
  fi

  local _lsfiles
  _lsfiles="$(git -C "$WORK_DIR" ls-files -v .cargo/config.toml)"
  [[ "$_lsfiles" != S* ]]
}

@test "intree_binding_apply reverts its own rewrite when the in-tree config itself is an unmerged conflict (issue #2932)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27191
  export REGISTRY_PROXY_UPSTREAM_HOST="cargo.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  _seed_cargo_intree_config "$REGISTRY_PROXY_UPSTREAM_HOST"

  # Unlike _advance_cargo_intree_config above (an unrelated append that
  # merges cleanly), both sides here edit the SAME othercorp index line, so
  # the rebase leaves .cargo/config.toml itself as an unmerged conflicting
  # path -- the specific condition that makes intree_binding_apply's second
  # call site (right after phase_prework_rebase, main()) fail: git refuses
  # `update-index --skip-worktree` for an unmerged path (exit 128,
  # ApplyInTreeBinding, cmd/launcher/internal/bindregistry/intreebinding.go),
  # the same failure this issue's entrypoint.sh fix now reacts to by calling
  # intree_binding_revert.
  local prior="$BATS_TEST_TMPDIR/prior"
  git clone -q "https://github.com/owner/repo.git" "$prior"
  git -C "$prior" checkout -b "agent/issue-7" "origin/main"
  sed -i 's#other-index/"#other-index-feature/"#' "$prior/.cargo/config.toml"
  git -C "$prior" commit -q -am "feat: branch retargets othercorp index"
  git -C "$prior" push -q origin "agent/issue-7"

  local advance="$BATS_TEST_TMPDIR/advance"
  git clone -q "https://github.com/owner/repo.git" "$advance"
  sed -i 's#other-index/"#other-index-main/"#' "$advance/.cargo/config.toml"
  git -C "$advance" commit -q -am "chore: main retargets othercorp index (conflicts)"
  git -C "$advance" push -q origin HEAD:main

  export FAKE_GH_PR_LIST_7="https://github.com/owner/repo/pull/7"
  # No FAKE_DRIVER_RESOLVE_CONFLICT -- stub does not complete the rebase, so
  # .cargo/config.toml stays an unmerged conflicting path through
  # intree_binding_apply's second call site and into phase_conflict_resolve.

  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"pre-work rebase"* ]]

  # Non-fatal: the failed apply on the unmerged path did not kill the script
  # outright -- execution reached phase_conflict_resolve and its own
  # rebase-abort path, which is what actually produced the exit above and
  # restored HEAD's own .cargo/config.toml content.
  [ -f "$WORK_DIR/.cargo/config.toml" ]
  # `git rebase --abort` resets to the branch's own pre-rebase tip -- byte
  # identical to what the branch pushed, never the proxy rewrite, the
  # conflict markers, or main's own competing edit. (The Go-side atomicity
  # fix already guarantees the rewrite itself never lands on an unmerged
  # path -- this pins that invariant end-to-end through this call site too.)
  diff -q "$prior/.cargo/config.toml" "$WORK_DIR/.cargo/config.toml"

  # The point of this test: the apply failure at the second call site must
  # have triggered its own revert attempt (issue #2932's entrypoint.sh fix).
  # With .cargo/config.toml itself unmerged, that revert's own `git checkout
  # --` fails too -- git refuses to check out an unmerged path, same as the
  # "phase_conflict_resolve reverts..." test's own revert-before-abort call
  # above -- so two separate "(in-tree revert) failed" warnings show up in
  # the output: the first from intree_binding_apply's own new cleanup call,
  # the second from phase_conflict_resolve's pre-existing revert-before-abort
  # call. Without the fix, only the second would ever print.
  [ "$(grep -c "in-tree revert) failed" <<<"$output")" -eq 2 ]
}

@test "no-op when REGISTRY_PROXY_UPSTREAM_HOST is unset even though .cargo/config.toml references a host (issue #2932)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27191
  unset REGISTRY_PROXY_UPSTREAM_HOST

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  _seed_cargo_intree_config "cargo.mycorp.example"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ -f "$WORK_DIR/.cargo/config.toml" ]
  grep -q "sparse+https://cargo.mycorp.example/index/" "$WORK_DIR/.cargo/config.toml"
  grep -q "index = \"http://cargo.mycorp.example/other-index/\"" "$WORK_DIR/.cargo/config.toml"

  local _lsfiles
  _lsfiles="$(git -C "$WORK_DIR" ls-files -v .cargo/config.toml)"
  [[ "$_lsfiles" != S* ]]
}
