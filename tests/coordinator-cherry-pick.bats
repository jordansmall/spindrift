#!/usr/bin/env bats
# Real git-mechanics coverage for the coordinator fragment's cherry-pick step
# (issue #2058 AC4: "Sequential coordinator runs still pass with
# worktree-isolated workers"). templates/default/prompts/fragments/coordinator.md
# tells the coordinator to integrate a delegated worker's branch with:
#
#   git cherry-pick --no-commit $(git merge-base HEAD <branch>)..<branch>
#
# instead of the earlier `git cherry-pick --no-commit origin/${BASE_BRANCH}..<branch>`
# form. The earlier form assumed a worker's isolated worktree (Agent-tool
# `isolation: "worktree"`) always branches from origin/${BASE_BRANCH}, which is
# false: the harness's own worktree-isolation mechanism cuts from its own
# default, independent of spindrift's own --base-branch. This file exercises
# the actual git command the fragment prescribes -- not prompt-rendering --
# against a real repo where the coordinator's own tree and a worker's branch
# have diverged from a shared base in different directions, the exact shape
# that made the old form unsound. See tests/entrypoint-clone.bats for the
# repo's established style of real git init/branch fixtures in bats.

@test "coordinator cherry-pick: merge-base range cleanly integrates a worker branch cut from a diverged base" {
  cd "$BATS_TEST_TMPDIR" || return 1
  git init -q -b main repo
  cd repo || return 1
  git config user.email "test@example.com"
  git config user.name "Test"

  # 1. Initial commit -- the shared fork point for both branches below.
  echo base >base.txt
  git add -A
  git commit -q -m "base"

  # 2. coordinator-tree: the coordinator's own integration branch, advanced
  # by a commit that exists ONLY here -- simulating spindrift's own
  # --base-branch having already moved on (e.g. an earlier slice already
  # integrated on this run), which is exactly what the worker's isolated
  # worktree never sees.
  git checkout -q -b coordinator-tree
  echo coord >coord-only.txt
  git add -A
  git commit -q -m "coordinator-only commit"

  # 3. worker-branch: cut from the ORIGINAL base, not from coordinator-tree
  # -- simulating the harness's worktree-isolation mechanism cutting the
  # worker's worktree from its own default rather than the coordinator's
  # current tree.
  git checkout -q -b worker-branch main
  echo new >worker-file.txt
  git add -A
  git commit -q -m "worker commit: add worker-file.txt"

  # 4. Coordinator integrates from its own tree.
  git checkout -q coordinator-tree

  # 5. The exact command the fragment prescribes.
  run git cherry-pick --no-commit "$(git merge-base HEAD worker-branch)..worker-branch"
  [ "$status" -eq 0 ]

  # 6. The worker's new file lands staged and cleanly -- no conflict...
  run git diff --cached --name-only
  [ "$status" -eq 0 ]
  [[ "$output" == *"worker-file.txt"* ]]

  # ...and coordinator-tree's own diverged commit is untouched: still
  # present, unmodified, and not re-staged/duplicated by the integration.
  [[ "$output" != *"coord-only.txt"* ]]
  [ -f coord-only.txt ]
  run git diff HEAD -- coord-only.txt
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "coordinator cherry-pick: the old origin/\${BASE_BRANCH}..<branch> form is not range-safe in the same setup" {
  cd "$BATS_TEST_TMPDIR" || return 1
  git init -q -b main repo
  cd repo || return 1
  git config user.email "test@example.com"
  git config user.name "Test"

  echo base >base.txt
  git add -A
  git commit -q -m "base"

  git checkout -q -b coordinator-tree
  echo coord >coord-only.txt
  git add -A
  git commit -q -m "coordinator-only commit"

  git checkout -q -b worker-branch main
  echo new >worker-file.txt
  git add -A
  git commit -q -m "worker commit: add worker-file.txt"

  git checkout -q coordinator-tree

  # Negative control #1: a fresh repo like this one has no "origin" remote at
  # all, so the old form's literal `origin/${BASE_BRANCH}` never resolves --
  # the exact "assumed a ref that isn't there" failure mode the fix
  # eliminates by depending on nothing but HEAD and the worker's own branch.
  run git cherry-pick --no-commit origin/main..worker-branch
  [ "$status" -ne 0 ]
  [[ "$output" == *"bad revision"* ]]

  # Negative control #2: even granting the old form a same-named ref that
  # stands in for origin/${BASE_BRANCH} -- the coordinator's own tree, the
  # closest thing this fixture has to "what's pushed as the base branch" --
  # its tip is not the same commit as the true merge-base with worker-branch.
  # The old form's range and the merge-base range are therefore built from
  # two different lower bounds, not interchangeable in general even where
  # (as here) they happen to cherry-pick the same diff.
  merge_base_sha="$(git merge-base HEAD worker-branch)"
  old_form_base_sha="$(git rev-parse coordinator-tree)"
  [ "$merge_base_sha" != "$old_form_base_sha" ]
}
