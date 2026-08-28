#!/usr/bin/env bats
# In-tree npm config binding (issue #2854): a Target repo can commit its own
# $WORK_DIR/.npmrc pinning a private registry per-scope (e.g.
# `@mycorp:registry=https://npm.mycorp.example/`). phase_registry_proxy_forwarder's
# npm_config_registry env-var override only reaches npm's single unscoped
# default registry key -- a per-scope `@scope:registry=` entry is a
# different config key entirely, and env vars only map to known top-level
# npm config keys, not arbitrary scoped ones -- so phase_npm_intree_binding_apply
# textually rewrites the committed file in place to point at the local
# Forwarder instead, then hides the rewrite from git via skip-worktree so it
# can never be staged or committed, mirroring phase_cargo_intree_binding_apply
# (entrypoint-cargo-intree-binding.bats) exactly.

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

# Seeds the remote's main branch with a committed .npmrc pinning a private
# scoped registry at $REGISTRY_PROXY_UPSTREAM_HOST. Call after setup_bare_repo.
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

@test "in-tree .npmrc referencing the upstream host is rewritten and hidden via skip-worktree (issue #2854)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27184
  export REGISTRY_PROXY_UPSTREAM_HOST="npm.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  _wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  _seed_npm_intree_config "$REGISTRY_PROXY_UPSTREAM_HOST"

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

  [[ "$output" == *"==> "*"npmrc"* ]]
}

@test "skip-worktree .npmrc is never stageable (issue #2854)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27184
  export REGISTRY_PROXY_UPSTREAM_HOST="npm.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  _wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  _seed_npm_intree_config "$REGISTRY_PROXY_UPSTREAM_HOST"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  git -C "$WORK_DIR" add -A
  [ -z "$(git -C "$WORK_DIR" status --short)" ]
  if git -C "$WORK_DIR" diff --cached --name-only | grep -q '\.npmrc'; then
    echo "expected .npmrc to be excluded from the stage by skip-worktree, but it was staged" >&2
    return 1
  fi
}

@test "no-op when no .npmrc exists (issue #2854)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27184
  export REGISTRY_PROXY_UPSTREAM_HOST="npm.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  _wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ ! -f "$WORK_DIR/.npmrc" ]
}

@test "no-op when .npmrc exists but is untracked (issue #2854)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27184
  export REGISTRY_PROXY_UPSTREAM_HOST="npm.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  _wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  # clone_repo's own `git clone "$CLONE_URL" "$WORK_DIR"` requires $WORK_DIR
  # to not already exist/be non-empty, so an untracked .npmrc can't just be
  # pre-seeded there before the entrypoint runs -- it has to land in the
  # working tree as part of the clone itself, without ever being a committed
  # blob. A git template dir's post-checkout hook runs, cwd already
  # $WORK_DIR, right after `git clone`'s own initial checkout populates the
  # tracked files, so writing .npmrc there (never `git add`ed) reproduces an
  # untracked .npmrc exactly as a real .gitignore'd one would land -- the
  # phase then sees `[ -f .npmrc ]` true but `git ls-files` blind to it, same
  # as any other gitignored file. `git update-index --skip-worktree` exits
  # 128 on an untracked path, so this proves the phase's tracked-file guard
  # makes it a clean no-op instead of that 128 aborting the whole script
  # under `set -euo pipefail`.
  local _content="@mycorp:registry=https://${REGISTRY_PROXY_UPSTREAM_HOST}/"
  local _tmpl="$BATS_TEST_TMPDIR/git-template"
  mkdir -p "$_tmpl/hooks"
  cat >"$_tmpl/hooks/post-checkout" <<EOF
#!/bin/sh
printf '%s\n' "$_content" >.npmrc
EOF
  chmod +x "$_tmpl/hooks/post-checkout"
  git config --global init.templateDir "$_tmpl"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ -f "$WORK_DIR/.npmrc" ]
  # Untouched: the phase must have returned before ever reaching the sed
  # rewrite, so the upstream host is still present verbatim.
  [ "$(cat "$WORK_DIR/.npmrc")" = "$_content" ]
}

@test "no-op when committed .npmrc doesn't reference the configured host (issue #2854)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27184
  export REGISTRY_PROXY_UPSTREAM_HOST="npm.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  _wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  _seed_npm_intree_config "npm.other-registry.example"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ -f "$WORK_DIR/.npmrc" ]
  grep -q "npm.other-registry.example" "$WORK_DIR/.npmrc"

  local _lsfiles
  _lsfiles="$(git -C "$WORK_DIR" ls-files -v .npmrc)"
  [[ "$_lsfiles" != S* ]]
}

# Advances the remote's main branch with a further commit that ALSO modifies
# .npmrc, appending a second @scope:registry= entry. Call after
# _seed_npm_intree_config: this is the specific condition (the committed
# blob for this path genuinely differs between the branch being rebased and
# the base it's rebasing onto) that triggers git's checkout-safety collision
# without the revert/re-apply wrapper (mirrors cargo's
# _advance_cargo_intree_config, issue #2851).
_advance_npm_intree_config() {
  local host="$1"
  local seed="$BATS_TEST_TMPDIR/seed-npm-advance"
  git clone -q "https://github.com/owner/repo.git" "$seed"
  cat >>"$seed/.npmrc" <<EOF
@othercorp:registry=https://${host}/other/
EOF
  git -C "$seed" add .npmrc
  git -C "$seed" commit -q -m "chore: pin second private npm scoped registry"
  git -C "$seed" push -q origin HEAD:main
}

@test "harness-driven rebase with divergent .npmrc across branches succeeds with the rewrite in place (issue #2854)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27184
  export REGISTRY_PROXY_UPSTREAM_HOST="npm.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  _wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  _seed_npm_intree_config "$REGISTRY_PROXY_UPSTREAM_HOST"

  # Stale prior work: agent/issue-7 branches off the pre-advance commit, with
  # unrelated work of its own that never touches .npmrc.
  local prior="$BATS_TEST_TMPDIR/prior"
  git clone -q "https://github.com/owner/repo.git" "$prior"
  git -C "$prior" checkout -b "agent/issue-7" "origin/main"
  echo "branch work" > "$prior/branch.txt"
  git -C "$prior" add branch.txt
  git -C "$prior" commit -q -m "feat: prior run work"
  git -C "$prior" push -q origin "agent/issue-7"

  # origin/main advances further, also touching .npmrc -- so the committed
  # blob for that path now genuinely differs between the branch being
  # rebased and the base it rebases onto.
  _advance_npm_intree_config "$REGISTRY_PROXY_UPSTREAM_HOST"

  # Open PR so the adoption path is taken (git checkout -b agent/issue-7
  # origin/agent/issue-7), mirroring entrypoint-branch-recovery.bats, so
  # phase_prework_rebase's `git rebase origin/main` has real work to replay.
  export FAKE_GH_PR_LIST_7="https://github.com/owner/repo/pull/7"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # The rebase must have actually replayed the branch's own work onto the
  # advanced base.
  [ -f "$WORK_DIR/branch.txt" ]

  # Final content must reflect BOTH the base's new scoped entry (proving the
  # rebase actually replayed the base's change, not that it silently kept
  # stale pre-rebase content) AND the local-endpoint rewrite.
  grep -q "@othercorp:registry=http://127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}" "$WORK_DIR/.npmrc"
  grep -q "@mycorp:registry=http://127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}" "$WORK_DIR/.npmrc"
  if grep -q "npm.mycorp.example" "$WORK_DIR/.npmrc"; then
    echo "expected npm.mycorp.example to be rewritten away, but it is still present" >&2
    return 1
  fi

  local _lsfiles
  _lsfiles="$(git -C "$WORK_DIR" ls-files -v .npmrc)"
  [[ "$_lsfiles" == S* ]]

  [ -z "$(git -C "$WORK_DIR" status --short)" ]
}

@test "phase_conflict_resolve reverts in-tree .npmrc rewrite before aborting an unresolvable rebase conflict (issue #2854)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27184
  export REGISTRY_PROXY_UPSTREAM_HOST="npm.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  _wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  _seed_npm_intree_config "$REGISTRY_PROXY_UPSTREAM_HOST"

  # Unrelated README conflict (mirrors entrypoint-branch-recovery.bats'
  # setup_rebase_conflict) -- .npmrc itself is untouched by either side, so
  # it rides through the stopped rebase unchanged and
  # phase_npm_intree_binding_apply's second call site (right after
  # phase_prework_rebase, main()) rewrites and skip-worktrees it again while
  # the rebase is still mid-conflict, exactly the state
  # phase_conflict_resolve's revert call must clean up before aborting.
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
  # matches the index -- so if npm_intree_binding_revert hadn't cleared the
  # bit and restored the original content first, the rewritten 127.0.0.1
  # endpoint and the skip-worktree bit would both still be here after abort.
  [ -f "$WORK_DIR/.npmrc" ]
  grep -q "@mycorp:registry=https://${REGISTRY_PROXY_UPSTREAM_HOST}/" "$WORK_DIR/.npmrc"
  if grep -q "127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}" "$WORK_DIR/.npmrc"; then
    echo "expected the in-tree .npmrc rewrite to have been reverted before the rebase abort, but it is still present" >&2
    return 1
  fi

  local _lsfiles
  _lsfiles="$(git -C "$WORK_DIR" ls-files -v .npmrc)"
  [[ "$_lsfiles" != S* ]]
}

@test "no-op when REGISTRY_PROXY_UPSTREAM_HOST is unset even though .npmrc references a host (issue #2854)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27184
  unset REGISTRY_PROXY_UPSTREAM_HOST

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  _wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  _seed_npm_intree_config "npm.mycorp.example"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ -f "$WORK_DIR/.npmrc" ]
  grep -q "@mycorp:registry=https://npm.mycorp.example/" "$WORK_DIR/.npmrc"

  local _lsfiles
  _lsfiles="$(git -C "$WORK_DIR" ls-files -v .npmrc)"
  [[ "$_lsfiles" != S* ]]
}
