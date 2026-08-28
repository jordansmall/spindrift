#!/usr/bin/env bats
# In-tree yarn berry config binding (issue #2856): a Target repo can commit
# its own $WORK_DIR/.yarnrc.yml pinning a private registry per-scope (e.g.
# `npmScopes.mycorp.npmRegistryServer: https://npm.mycorp.example/`).
# phase_registry_proxy_forwarder's YARN_NPM_REGISTRY_SERVER env-var override
# only reaches yarn berry's single top-level default npmRegistryServer key --
# npmScopes entries have no env-var equivalent, so a per-scope
# npmRegistryServer entry is a different config key entirely, only reachable
# by rewriting the committed file. phase_yarn_berry_intree_binding_apply
# textually rewrites the committed file in place to point at the local
# Forwarder instead, then hides the rewrite from git via skip-worktree so it
# can never be staged or committed, mirroring phase_npm_intree_binding_apply
# (entrypoint-npm-intree-binding.bats) exactly, applied to a YAML file
# instead of an INI-ish one.

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

# Seeds the remote's main branch with a committed .yarnrc.yml pinning both a
# top-level default registry and a per-scope registry, both at
# $REGISTRY_PROXY_UPSTREAM_HOST. Call after setup_bare_repo.
_seed_yarn_berry_intree_config() {
  local host="$1"
  local seed="$BATS_TEST_TMPDIR/seed-yarn-berry"
  git clone -q "https://github.com/owner/repo.git" "$seed"
  cat >"$seed/.yarnrc.yml" <<EOF
npmRegistryServer: "https://${host}/"
npmScopes:
  mycorp:
    npmRegistryServer: "https://${host}/other/"
EOF
  git -C "$seed" add .yarnrc.yml
  git -C "$seed" commit -q -m "chore: pin private yarn berry registries"
  git -C "$seed" push -q origin HEAD:main
}

@test "in-tree .yarnrc.yml referencing the upstream host is rewritten and hidden via skip-worktree (issue #2856)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27189
  export REGISTRY_PROXY_UPSTREAM_HOST="npm.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  _wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  _seed_yarn_berry_intree_config "$REGISTRY_PROXY_UPSTREAM_HOST"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ -f "$WORK_DIR/.yarnrc.yml" ]
  grep -q "npmRegistryServer: \"http://127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}/\"" "$WORK_DIR/.yarnrc.yml"
  grep -q "npmRegistryServer: \"http://127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}/other/\"" "$WORK_DIR/.yarnrc.yml"
  if grep -q "npm.mycorp.example" "$WORK_DIR/.yarnrc.yml"; then
    echo "expected npm.mycorp.example to be rewritten away, but it is still present" >&2
    return 1
  fi

  # skip-worktree bit set: `git ls-files -v` prefixes a skip-worktree path
  # with uppercase 'S' (lowercase is reserved for the separate
  # assume-unchanged bit, which this phase never sets).
  local _lsfiles
  _lsfiles="$(git -C "$WORK_DIR" ls-files -v .yarnrc.yml)"
  [[ "$_lsfiles" == S* ]]

  [ -z "$(git -C "$WORK_DIR" status --short)" ]

  [[ "$output" == *"==> "*"yarnrc.yml"* ]]
}

@test "skip-worktree .yarnrc.yml is never stageable (issue #2856)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27189
  export REGISTRY_PROXY_UPSTREAM_HOST="npm.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  _wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  _seed_yarn_berry_intree_config "$REGISTRY_PROXY_UPSTREAM_HOST"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  git -C "$WORK_DIR" add -A
  [ -z "$(git -C "$WORK_DIR" status --short)" ]
  if git -C "$WORK_DIR" diff --cached --name-only | grep -q '\.yarnrc\.yml'; then
    echo "expected .yarnrc.yml to be excluded from the stage by skip-worktree, but it was staged" >&2
    return 1
  fi
}

@test "no-op when no .yarnrc.yml exists (issue #2856)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27189
  export REGISTRY_PROXY_UPSTREAM_HOST="npm.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  _wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ ! -f "$WORK_DIR/.yarnrc.yml" ]
}

@test "no-op when .yarnrc.yml exists but is untracked (issue #2856)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27189
  export REGISTRY_PROXY_UPSTREAM_HOST="npm.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  _wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  # clone_repo's own `git clone "$CLONE_URL" "$WORK_DIR"` requires $WORK_DIR
  # to not already exist/be non-empty, so an untracked .yarnrc.yml can't just
  # be pre-seeded there before the entrypoint runs -- it has to land in the
  # working tree as part of the clone itself, without ever being a committed
  # blob. A git template dir's post-checkout hook runs, cwd already
  # $WORK_DIR, right after `git clone`'s own initial checkout populates the
  # tracked files, so writing .yarnrc.yml there (never `git add`ed)
  # reproduces an untracked .yarnrc.yml exactly as a real gitignore'd one
  # would land -- the phase then sees `[ -f .yarnrc.yml ]` true but
  # `git ls-files` blind to it, same as any other gitignored file. `git
  # update-index --skip-worktree` exits 128 on an untracked path, so this
  # proves the phase's tracked-file guard makes it a clean no-op instead of
  # that 128 aborting the whole script under `set -euo pipefail`.
  local _content="npmRegistryServer: \"https://${REGISTRY_PROXY_UPSTREAM_HOST}/\""
  local _tmpl="$BATS_TEST_TMPDIR/git-template"
  mkdir -p "$_tmpl/hooks"
  # Nested heredoc, not a `printf '%s\n' "$_content"` one-liner: $_content
  # itself contains double-quote characters (the YAML value's own quoting),
  # so splicing it inside another pair of double quotes would prematurely
  # close them and corrupt the written file. The inner heredoc's quoted
  # 'YAMLEOF' delimiter takes $_content verbatim, no re-quoting needed.
  cat >"$_tmpl/hooks/post-checkout" <<HOOKEOF
#!/bin/sh
cat >.yarnrc.yml <<'YAMLEOF'
$_content
YAMLEOF
HOOKEOF
  chmod +x "$_tmpl/hooks/post-checkout"
  git config --global init.templateDir "$_tmpl"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ -f "$WORK_DIR/.yarnrc.yml" ]
  # Untouched: the phase must have returned before ever reaching the sed
  # rewrite, so the upstream host is still present verbatim.
  [ "$(cat "$WORK_DIR/.yarnrc.yml")" = "$_content" ]
}

@test "no-op when committed .yarnrc.yml doesn't reference the configured host (issue #2856)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27189
  export REGISTRY_PROXY_UPSTREAM_HOST="npm.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  _wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  _seed_yarn_berry_intree_config "npm.other-registry.example"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ -f "$WORK_DIR/.yarnrc.yml" ]
  grep -q "npm.other-registry.example" "$WORK_DIR/.yarnrc.yml"

  local _lsfiles
  _lsfiles="$(git -C "$WORK_DIR" ls-files -v .yarnrc.yml)"
  [[ "$_lsfiles" != S* ]]
}

# Advances the remote's main branch with a further commit that ALSO modifies
# .yarnrc.yml, adding a second npmScopes entry. Call after
# _seed_yarn_berry_intree_config: this is the specific condition (the
# committed blob for this path genuinely differs between the branch being
# rebased and the base it's rebasing onto) that triggers git's
# checkout-safety collision without the revert/re-apply wrapper (mirrors
# npm's _advance_npm_intree_config, issue #2854).
_advance_yarn_berry_intree_config() {
  local host="$1"
  local seed="$BATS_TEST_TMPDIR/seed-yarn-berry-advance"
  git clone -q "https://github.com/owner/repo.git" "$seed"
  cat >>"$seed/.yarnrc.yml" <<EOF
  othercorp:
    npmRegistryServer: "https://${host}/other2/"
EOF
  git -C "$seed" add .yarnrc.yml
  git -C "$seed" commit -q -m "chore: pin second private yarn berry scoped registry"
  git -C "$seed" push -q origin HEAD:main
}

@test "harness-driven rebase with divergent .yarnrc.yml across branches succeeds with the rewrite in place (issue #2856)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27189
  export REGISTRY_PROXY_UPSTREAM_HOST="npm.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  _wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  _seed_yarn_berry_intree_config "$REGISTRY_PROXY_UPSTREAM_HOST"

  # Stale prior work: agent/issue-7 branches off the pre-advance commit, with
  # unrelated work of its own that never touches .yarnrc.yml.
  local prior="$BATS_TEST_TMPDIR/prior"
  git clone -q "https://github.com/owner/repo.git" "$prior"
  git -C "$prior" checkout -b "agent/issue-7" "origin/main"
  echo "branch work" > "$prior/branch.txt"
  git -C "$prior" add branch.txt
  git -C "$prior" commit -q -m "feat: prior run work"
  git -C "$prior" push -q origin "agent/issue-7"

  # origin/main advances further, also touching .yarnrc.yml -- so the
  # committed blob for that path now genuinely differs between the branch
  # being rebased and the base it rebases onto.
  _advance_yarn_berry_intree_config "$REGISTRY_PROXY_UPSTREAM_HOST"

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
  grep -q "npmRegistryServer: \"http://127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}/other2/\"" "$WORK_DIR/.yarnrc.yml"
  grep -q "npmRegistryServer: \"http://127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}/\"" "$WORK_DIR/.yarnrc.yml"
  grep -q "npmRegistryServer: \"http://127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}/other/\"" "$WORK_DIR/.yarnrc.yml"
  if grep -q "npm.mycorp.example" "$WORK_DIR/.yarnrc.yml"; then
    echo "expected npm.mycorp.example to be rewritten away, but it is still present" >&2
    return 1
  fi

  local _lsfiles
  _lsfiles="$(git -C "$WORK_DIR" ls-files -v .yarnrc.yml)"
  [[ "$_lsfiles" == S* ]]

  [ -z "$(git -C "$WORK_DIR" status --short)" ]
}

@test "phase_conflict_resolve reverts in-tree .yarnrc.yml rewrite before aborting an unresolvable rebase conflict (issue #2856)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27189
  export REGISTRY_PROXY_UPSTREAM_HOST="npm.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  _wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  _seed_yarn_berry_intree_config "$REGISTRY_PROXY_UPSTREAM_HOST"

  # Unrelated README conflict (mirrors entrypoint-branch-recovery.bats'
  # setup_rebase_conflict) -- .yarnrc.yml itself is untouched by either side,
  # so it rides through the stopped rebase unchanged and
  # phase_yarn_berry_intree_binding_apply's second call site (right after
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
  # matches the index -- so if yarn_berry_intree_binding_revert hadn't
  # cleared the bit and restored the original content first, the rewritten
  # 127.0.0.1 endpoint and the skip-worktree bit would both still be here
  # after abort.
  [ -f "$WORK_DIR/.yarnrc.yml" ]
  grep -q "npmRegistryServer: \"https://${REGISTRY_PROXY_UPSTREAM_HOST}/\"" "$WORK_DIR/.yarnrc.yml"
  if grep -q "127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}" "$WORK_DIR/.yarnrc.yml"; then
    echo "expected the in-tree .yarnrc.yml rewrite to have been reverted before the rebase abort, but it is still present" >&2
    return 1
  fi

  local _lsfiles
  _lsfiles="$(git -C "$WORK_DIR" ls-files -v .yarnrc.yml)"
  [[ "$_lsfiles" != S* ]]
}

@test "no-op when REGISTRY_PROXY_UPSTREAM_HOST is unset even though .yarnrc.yml references a host (issue #2856)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27189
  unset REGISTRY_PROXY_UPSTREAM_HOST

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  _wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  _seed_yarn_berry_intree_config "npm.mycorp.example"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ -f "$WORK_DIR/.yarnrc.yml" ]
  grep -q "npmRegistryServer: \"https://npm.mycorp.example/\"" "$WORK_DIR/.yarnrc.yml"

  local _lsfiles
  _lsfiles="$(git -C "$WORK_DIR" ls-files -v .yarnrc.yml)"
  [[ "$_lsfiles" != S* ]]
}
