#!/usr/bin/env bats
# Covers two distinct halves of pnpm's registry Binding (issue #2855).
#
# 1. .npmrc characterization tests: prove pnpm needs no distinct Binding
#    entry for a per-scope in-tree .npmrc rewrite. pnpm reads the exact
#    same `.npmrc` file format npm does (a default `registry=` line and/or
#    `@scope:registry=` lines), and phase_npm_intree_binding_apply's
#    rewrite (entrypoint-npm-intree-binding.bats, issue #2854) is a plain
#    sed over the whole file content gated only on `.npmrc` existing +
#    being git-tracked -- it never inspects which lockfile is present, so
#    an npm project and a pnpm project with an equivalent committed
#    .npmrc are indistinguishable to this phase. These tests demonstrate
#    that identical behavior with a pnpm-lock.yaml marker present instead
#    of a package-lock.json/npm project, with zero production code
#    changes.
#
# 2. pnpm-workspace.yaml tests: pnpm also supports its own `registries:`
#    key in pnpm-workspace.yaml (per pnpm.io/registries), a format npm has
#    no equivalent for, so this needs a new production phase pair --
#    phase_pnpm_workspace_intree_binding_apply / pnpm_workspace_intree_
#    binding_revert -- mirroring phase_npm_intree_binding_apply's sed +
#    skip-worktree approach but over the YAML `registries:` map instead of
#    `.npmrc` lines.

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
  export REGISTRY_PROXY_FORWARDER_PORT=27190
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
  export REGISTRY_PROXY_FORWARDER_PORT=27190
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

# Seeds the remote's main branch with a pnpm-lock.yaml marker AND a
# committed pnpm-workspace.yaml pinning a private scoped registry via pnpm's
# own `registries:` key (keyed by URL, per pnpm.io/registries) at
# $REGISTRY_PROXY_UPSTREAM_HOST, in one commit. Call after setup_bare_repo.
_seed_pnpm_workspace_config() {
  local host="$1"
  local seed="$BATS_TEST_TMPDIR/seed-pnpm-workspace"
  git clone -q "https://github.com/owner/repo.git" "$seed"
  touch "$seed/pnpm-lock.yaml"
  cat >"$seed/pnpm-workspace.yaml" <<EOF
registries:
  https://${host}/:
    scopes: ["@mycorp"]
EOF
  git -C "$seed" add pnpm-lock.yaml pnpm-workspace.yaml
  git -C "$seed" commit -q -m "chore: seed pnpm workspace with private scoped registry"
  git -C "$seed" push -q origin HEAD:main
}

@test "pnpm-workspace.yaml registries entry referencing the upstream host is rewritten and hidden via skip-worktree (issue #2855)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27190
  export REGISTRY_PROXY_UPSTREAM_HOST="npm.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  _seed_pnpm_workspace_config "$REGISTRY_PROXY_UPSTREAM_HOST"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ -f "$WORK_DIR/pnpm-lock.yaml" ]
  [ -f "$WORK_DIR/pnpm-workspace.yaml" ]
  grep -q "http://127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}/:" "$WORK_DIR/pnpm-workspace.yaml"
  if grep -q "npm.mycorp.example" "$WORK_DIR/pnpm-workspace.yaml"; then
    echo "expected npm.mycorp.example to be rewritten away, but it is still present" >&2
    return 1
  fi

  # skip-worktree bit set: `git ls-files -v` prefixes a skip-worktree path
  # with uppercase 'S' (lowercase is reserved for the separate
  # assume-unchanged bit, which this phase never sets).
  local _lsfiles
  _lsfiles="$(git -C "$WORK_DIR" ls-files -v pnpm-workspace.yaml)"
  [[ "$_lsfiles" == S* ]]

  [ -z "$(git -C "$WORK_DIR" status --short)" ]

  [[ "$output" == *"pnpm-workspace.yaml rewritten"* ]]
}

@test "skip-worktree pnpm-workspace.yaml is never stageable (issue #2855)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27190
  export REGISTRY_PROXY_UPSTREAM_HOST="npm.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  _seed_pnpm_workspace_config "$REGISTRY_PROXY_UPSTREAM_HOST"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  git -C "$WORK_DIR" add -A
  [ -z "$(git -C "$WORK_DIR" status --short)" ]
  if git -C "$WORK_DIR" diff --cached --name-only | grep -q 'pnpm-workspace\.yaml'; then
    echo "expected pnpm-workspace.yaml to be excluded from the stage by skip-worktree, but it was staged" >&2
    return 1
  fi
}

@test "no-op when no pnpm-workspace.yaml exists (issue #2855)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27190
  export REGISTRY_PROXY_UPSTREAM_HOST="npm.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  seed_dependency_manifest "pnpm-lock.yaml"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ -f "$WORK_DIR/pnpm-lock.yaml" ]
  [ ! -f "$WORK_DIR/pnpm-workspace.yaml" ]
}

@test "no-op when no .npmrc exists in a pnpm project (issue #2855)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27190
  export REGISTRY_PROXY_UPSTREAM_HOST="npm.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  seed_dependency_manifest "pnpm-lock.yaml"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ -f "$WORK_DIR/pnpm-lock.yaml" ]
}

@test "no-op when pnpm-workspace.yaml exists but is untracked (issue #2855)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27190
  export REGISTRY_PROXY_UPSTREAM_HOST="npm.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  seed_dependency_manifest "pnpm-lock.yaml"

  # clone_repo's own `git clone "$CLONE_URL" "$WORK_DIR"` requires $WORK_DIR
  # to not already exist/be non-empty, so an untracked pnpm-workspace.yaml
  # can't just be pre-seeded there before the entrypoint runs -- it has to
  # land in the working tree as part of the clone itself, without ever being
  # a committed blob. A git template dir's post-checkout hook runs, cwd
  # already $WORK_DIR, right after `git clone`'s own initial checkout
  # populates the tracked files, so writing pnpm-workspace.yaml there (never
  # `git add`ed) reproduces an untracked pnpm-workspace.yaml exactly as a
  # real .gitignore'd one would land -- the phase then sees `[ -f
  # pnpm-workspace.yaml ]` true but `git ls-files` blind to it, same as any
  # other gitignored file. `git update-index --skip-worktree` exits 128 on
  # an untracked path, so this proves the phase's tracked-file guard makes
  # it a clean no-op instead of that 128 aborting the whole script under
  # `set -euo pipefail`.
  local _content
  _content=$(cat <<EOF
registries:
  https://${REGISTRY_PROXY_UPSTREAM_HOST}/:
    scopes: ["@mycorp"]
EOF
)
  local _tmpl="$BATS_TEST_TMPDIR/git-template"
  mkdir -p "$_tmpl/hooks"
  cat >"$_tmpl/hooks/post-checkout" <<EOF
#!/bin/sh
cat >pnpm-workspace.yaml <<'YAML'
${_content}
YAML
EOF
  chmod +x "$_tmpl/hooks/post-checkout"
  git config --global init.templateDir "$_tmpl"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ -f "$WORK_DIR/pnpm-lock.yaml" ]
  [ -f "$WORK_DIR/pnpm-workspace.yaml" ]
  # Untouched: the phase must have returned before ever reaching the sed
  # rewrite, so the upstream host is still present verbatim.
  [ "$(cat "$WORK_DIR/pnpm-workspace.yaml")" = "$_content" ]
}

@test "no-op when committed pnpm-workspace.yaml doesn't reference the configured host (issue #2855)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27190
  export REGISTRY_PROXY_UPSTREAM_HOST="npm.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  _seed_pnpm_workspace_config "npm.other-registry.example"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ -f "$WORK_DIR/pnpm-workspace.yaml" ]
  grep -q "npm.other-registry.example" "$WORK_DIR/pnpm-workspace.yaml"

  local _lsfiles
  _lsfiles="$(git -C "$WORK_DIR" ls-files -v pnpm-workspace.yaml)"
  [[ "$_lsfiles" != S* ]]
}

# Advances the remote's main branch with a further commit that ALSO modifies
# pnpm-workspace.yaml, appending a second scope/registry entry. Call after
# _seed_pnpm_workspace_config: this is the specific condition (the committed
# blob for this path genuinely differs between the branch being rebased and
# the base it's rebasing onto) that triggers git's checkout-safety collision
# without the revert/re-apply wrapper (mirrors npm's
# _advance_npm_intree_config, issue #2854).
_advance_pnpm_workspace_config() {
  local host="$1"
  local seed="$BATS_TEST_TMPDIR/seed-pnpm-workspace-advance"
  git clone -q "https://github.com/owner/repo.git" "$seed"
  cat >"$seed/pnpm-workspace.yaml" <<EOF
registries:
  https://${host}/:
    scopes: ["@mycorp"]
  https://${host}/other/:
    scopes: ["@othercorp"]
EOF
  git -C "$seed" add pnpm-workspace.yaml
  git -C "$seed" commit -q -m "chore: pin second private pnpm scoped registry"
  git -C "$seed" push -q origin HEAD:main
}

@test "harness-driven rebase with divergent pnpm-workspace.yaml across branches succeeds with the rewrite in place (issue #2855)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27190
  export REGISTRY_PROXY_UPSTREAM_HOST="npm.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  _seed_pnpm_workspace_config "$REGISTRY_PROXY_UPSTREAM_HOST"

  # Stale prior work: agent/issue-7 branches off the pre-advance commit, with
  # unrelated work of its own that never touches pnpm-workspace.yaml.
  local prior="$BATS_TEST_TMPDIR/prior"
  git clone -q "https://github.com/owner/repo.git" "$prior"
  git -C "$prior" checkout -b "agent/issue-7" "origin/main"
  echo "branch work" > "$prior/branch.txt"
  git -C "$prior" add branch.txt
  git -C "$prior" commit -q -m "feat: prior run work"
  git -C "$prior" push -q origin "agent/issue-7"

  # origin/main advances further, also touching pnpm-workspace.yaml -- so
  # the committed blob for that path now genuinely differs between the
  # branch being rebased and the base it rebases onto.
  _advance_pnpm_workspace_config "$REGISTRY_PROXY_UPSTREAM_HOST"

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
  grep -q "http://127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}/:" "$WORK_DIR/pnpm-workspace.yaml"
  grep -q "http://127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}/other/:" "$WORK_DIR/pnpm-workspace.yaml"
  if grep -q "npm.mycorp.example" "$WORK_DIR/pnpm-workspace.yaml"; then
    echo "expected npm.mycorp.example to be rewritten away, but it is still present" >&2
    return 1
  fi

  local _lsfiles
  _lsfiles="$(git -C "$WORK_DIR" ls-files -v pnpm-workspace.yaml)"
  [[ "$_lsfiles" == S* ]]

  [ -z "$(git -C "$WORK_DIR" status --short)" ]

  [[ "$output" == *"pnpm-workspace.yaml rewritten"* ]]
}

@test "phase_conflict_resolve reverts in-tree pnpm-workspace.yaml rewrite before aborting an unresolvable rebase conflict (issue #2855)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27190
  export REGISTRY_PROXY_UPSTREAM_HOST="npm.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  _seed_pnpm_workspace_config "$REGISTRY_PROXY_UPSTREAM_HOST"

  # Unrelated README conflict (mirrors entrypoint-branch-recovery.bats'
  # setup_rebase_conflict) -- pnpm-workspace.yaml itself is untouched by
  # either side, so it rides through the stopped rebase unchanged and
  # phase_pnpm_workspace_intree_binding_apply's second call site (right
  # after phase_prework_rebase, main()) rewrites and skip-worktrees it again
  # while the rebase is still mid-conflict, exactly the state
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
  # matches the index -- so if pnpm_workspace_intree_binding_revert hadn't
  # cleared the bit and restored the original content first, the rewritten
  # 127.0.0.1 endpoint and the skip-worktree bit would both still be here
  # after abort.
  [ -f "$WORK_DIR/pnpm-workspace.yaml" ]
  grep -q "https://${REGISTRY_PROXY_UPSTREAM_HOST}/:" "$WORK_DIR/pnpm-workspace.yaml"
  if grep -q "127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}" "$WORK_DIR/pnpm-workspace.yaml"; then
    echo "expected the in-tree pnpm-workspace.yaml rewrite to have been reverted before the rebase abort, but it is still present" >&2
    return 1
  fi

  local _lsfiles
  _lsfiles="$(git -C "$WORK_DIR" ls-files -v pnpm-workspace.yaml)"
  [[ "$_lsfiles" != S* ]]
}
