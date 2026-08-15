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
#
# extract_cherry_pick_cmd (below) matches the fragment's command line against
# a literal grep pattern (still hand-typed once, here, rather than at every
# call site) and substitutes in the real branch name; if a future edit
# changes the fragment's actual command text, the grep stops matching and
# extraction fails loud, instead of these tests staying silently green
# against stale wording. The extracted string is later executed via
# `bash -c` (below) -- real, not eval'd fragment prose.

COORDINATOR_FRAGMENT="${PROMPTS_DIR:-$BATS_TEST_DIRNAME/../templates/default/prompts}/fragments/coordinator.md"

# Extracts the fragment's prescribed cherry-pick command, with the literal
# `<branch>` placeholder substituted for $1. Greps the fragment for the one
# line containing "git cherry-pick --no-commit" via a literal pattern that
# must match the fragment's exact syntax, then swaps in the real branch name
# via a shell parameter substitution. The result is a real shell command
# string, later run with `bash -c` -- this function only locates and
# parameterizes it, it doesn't itself execute anything.
extract_cherry_pick_cmd() {
  local branch="$1"
  local line
  line="$(grep -o 'git cherry-pick --no-commit \$(git merge-base HEAD <branch>)\.\.<branch>' "$COORDINATOR_FRAGMENT")"
  [ -n "$line" ] || return 1
  echo "${line//<branch>/$branch}"
}

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

  # 5. The exact command the fragment prescribes, extracted from the
  # fragment itself rather than hand-typed.
  local cmd
  cmd="$(extract_cherry_pick_cmd worker-branch)"
  [ -n "$cmd" ]
  run bash -c "$cmd"
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
  # This is deliberately the OLD, no-longer-prescribed form -- not the
  # fragment's current text -- so it stays hand-typed rather than extracted.
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

@test "coordinator cherry-pick: sequential slices integrate cleanly across the coordinator's own commit loop" {
  cd "$BATS_TEST_TMPDIR" || return 1
  git init -q -b main repo
  cd repo || return 1
  git config user.email "test@example.com"
  git config user.name "Test"

  # 1. Initial commit -- the shared fork point for coordinator-tree and every
  # worker branch below.
  echo base >base.txt
  git add -A
  git commit -q -m "base"
  base_sha="$(git rev-parse HEAD)"

  git checkout -q -b coordinator-tree

  # 2. worker-branch-1: cut from main (not coordinator-tree) -- mirrors the
  # harness's worktree-isolation mechanism cutting from its own default.
  git checkout -q -b worker-branch-1 main
  echo slice1 >slice-1.txt
  git add -A
  git commit -q -m "worker commit: add slice-1.txt"

  # 3. Coordinator integrates slice 1 from its own tree, then authors the
  # commit itself -- the fragment's "author the commit yourself ... before
  # ... handing out the next one" step.
  git checkout -q coordinator-tree
  local cmd
  cmd="$(extract_cherry_pick_cmd worker-branch-1)"
  [ -n "$cmd" ]
  run bash -c "$cmd"
  [ "$status" -eq 0 ]
  git commit -q -m "integrate slice 1"

  # 4. No leftover cherry-pick sequencer state survives the coordinator's own
  # commit -- if the coordinator had forgotten to commit before starting the
  # next slice, CHERRY_PICK_HEAD would still be sitting there.
  [ ! -e .git/CHERRY_PICK_HEAD ]

  # ...and, more concretely, the working tree is fully clean -- no staged or
  # unstaged residue from slice 1's cherry-pick. This is the check that
  # actually discriminates a forgotten commit: verified separately that
  # `--no-commit` never sets CHERRY_PICK_HEAD in this git version even on a
  # real conflict, so that assertion alone is always true regardless of
  # whether `git commit` ran. What forgetting the commit actually does is
  # leave slice 1 staged, so the next cherry-pick below would silently land
  # on top of it with no error -- exactly the "leftover state could bite"
  # failure mode the fragment's "author the commit yourself ... before ...
  # handing out the next one" step exists to prevent.
  run git status --porcelain
  [ "$status" -eq 0 ]
  [ -z "$output" ]

  # 5. worker-branch-2: ALSO cut from main -- not from worker-branch-1 and
  # not from the now-advanced coordinator-tree -- same "worker never sees an
  # earlier slice's work" isolation as worker-branch-1.
  git checkout -q -b worker-branch-2 main
  echo slice2 >slice-2.txt
  git add -A
  git commit -q -m "worker commit: add slice-2.txt"

  # 6. Back on coordinator-tree (now containing slice 1's integrated
  # commit), the merge-base with worker-branch-2 tracks the coordinator's own
  # moving HEAD -- it's the original base commit, NOT worker-branch-1's tip,
  # since worker-branch-2 never saw slice 1.
  git checkout -q coordinator-tree
  merge_base_sha="$(git merge-base HEAD worker-branch-2)"
  [ "$merge_base_sha" = "$base_sha" ]

  # 7. Integrate slice 2 using the fragment's command against that
  # merge-base -- succeeds, stages the second file, and leaves slice 1's
  # file untouched, proving both slices coexist across the sequential loop.
  cmd="$(extract_cherry_pick_cmd worker-branch-2)"
  [ -n "$cmd" ]
  run bash -c "$cmd"
  [ "$status" -eq 0 ]

  run git diff --cached --name-only
  [ "$status" -eq 0 ]
  [[ "$output" == *"slice-2.txt"* ]]

  [ -f slice-1.txt ]
  run git diff HEAD -- slice-1.txt
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "coordinator cherry-pick: concurrent worktrees isolate uncommitted changes (AC2)" {
  cd "$BATS_TEST_TMPDIR" || return 1
  git init -q -b main repo
  cd repo || return 1
  git config user.email "test@example.com"
  git config user.name "Test"

  # 1. Initial commit -- the shared fork point both workers' worktrees branch
  # from, and the shared object database (.git) both worktrees below point at.
  echo base >base.txt
  git add -A
  git commit -q -m "base"

  # 2. Two "concurrent workers", each a real `git worktree add -b <branch>
  # <path> <base>` off THIS SAME repo -- the exact git mechanism the Agent
  # tool's `isolation: "worktree"` relies on (coordinator.md) when the
  # coordinator delegates two slices "at once". Both cut from main, exactly
  # as two simultaneous delegations would, and both live alongside the main
  # checkout sharing one .git object database.
  git worktree add -q -b worker-a-branch ../worker-a main
  git worktree add -q -b worker-b-branch ../worker-b main

  # 3. Worker A makes an UNCOMMITTED change in its own worktree: one new file
  # staged, one existing file edited but left unstaged -- covering both halves
  # of "staged and/or unstaged, not committed".
  (
    cd ../worker-a || exit 1
    echo worker-a-new >worker-a-only.txt
    git add worker-a-only.txt
    echo worker-a-edit >>base.txt
  )

  # 4. Worker B makes its own, DIFFERENT uncommitted change in its own
  # worktree, concurrently with (i.e. without any dependency on) step 3 --
  # simulating the two workers running at the same time.
  (
    cd ../worker-b || exit 1
    echo worker-b-new >worker-b-only.txt
    git add worker-b-only.txt
    echo worker-b-edit >>base.txt
  )

  # 5. Worker A's own working directory and index show ONLY worker A's
  # changes -- worker B's staged file and edit never crossed over, even
  # though both worktrees share the same .git object database.
  run git -C ../worker-a status --porcelain
  [ "$status" -eq 0 ]
  [[ "$output" == *"A  worker-a-only.txt"* ]]
  [[ "$output" == *" M base.txt"* ]]
  [[ "$output" != *"worker-b-only.txt"* ]]
  [ ! -e ../worker-a/worker-b-only.txt ]
  run cat ../worker-a/base.txt
  [ "$status" -eq 0 ]
  [[ "$output" == *"worker-a-edit"* ]]
  [[ "$output" != *"worker-b-edit"* ]]

  # 6. ...and, symmetrically, worker B's working directory and index show
  # ONLY worker B's changes -- worker A's uncommitted work never leaked the
  # other way either.
  run git -C ../worker-b status --porcelain
  [ "$status" -eq 0 ]
  [[ "$output" == *"A  worker-b-only.txt"* ]]
  [[ "$output" == *" M base.txt"* ]]
  [[ "$output" != *"worker-a-only.txt"* ]]
  [ ! -e ../worker-b/worker-a-only.txt ]
  run cat ../worker-b/base.txt
  [ "$status" -eq 0 ]
  [[ "$output" == *"worker-b-edit"* ]]
  [[ "$output" != *"worker-a-edit"* ]]

  # 7. Belt and suspenders: the coordinator's own main checkout -- the shared
  # tree both worktrees were cut from -- is untouched by either worker's
  # uncommitted work, confirming isolation runs both worktree-to-worktree AND
  # worktree-to-main-checkout.
  run git status --porcelain
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  [ ! -e worker-a-only.txt ]
  [ ! -e worker-b-only.txt ]
  run cat base.txt
  [ "$status" -eq 0 ]
  [ "$output" = "base" ]
}
