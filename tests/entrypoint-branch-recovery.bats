#!/usr/bin/env bats
# Pre-work rebase and conflict resolution, including generated files (issues #215, #216, #403).

load helper

setup() {
  setup_entrypoint_env
}

# The box rebases the working branch onto the latest origin/BASE_BRANCH before
# the agent starts, so the agent works against current main rather than the
# state of origin at clone time (issue #215).

@test "entrypoint rebases prior work onto latest origin/BASE_BRANCH before agent starts" {
  # A prior run pushed agent/issue-7, then main advanced without conflicting.
  local prior="$BATS_TEST_TMPDIR/prior"
  git clone -q "https://github.com/owner/repo.git" "$prior"
  git -C "$prior" checkout -b "agent/issue-7" "origin/main"
  echo "branch work" > "$prior/branch.txt"
  git -C "$prior" add branch.txt
  git -C "$prior" commit -q -m "feat: prior run work"
  git -C "$prior" push -q origin "agent/issue-7"

  local advance="$BATS_TEST_TMPDIR/advance"
  git clone -q "https://github.com/owner/repo.git" "$advance"
  echo "main advance" > "$advance/main_advance.txt"
  git -C "$advance" add main_advance.txt
  git -C "$advance" commit -q -m "chore: advance main"
  git -C "$advance" push -q origin HEAD:main

  # Open PR so the adoption path is taken (no force-reset).
  export FAKE_GH_PR_LIST_7="https://github.com/owner/repo/pull/7"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # A branch rebased on top of the latest main has both the prior branch work
  # and the main advance.
  [ -f "$WORK_DIR/branch.txt" ]
  [ -f "$WORK_DIR/main_advance.txt" ]

  # The rebased branch must have been force-pushed so the agent's first
  # incremental push is a fast-forward, not a non-fast-forward rejection.
  echo "agent work" > "$WORK_DIR/agent.txt"
  git -C "$WORK_DIR" add agent.txt
  git -C "$WORK_DIR" commit -q -m "feat: agent work on rebased branch"
  run git -C "$WORK_DIR" push origin "agent/issue-7"
  [ "$status" -eq 0 ]
}

@test "entrypoint bundles rebased branch to outbox instead of force-pushing when read-only" {
  # Same setup as the read-write case above, but with BOX_WRITE_ENABLED unset
  # (issue #1979): the box holds no push-capable token, so publishing the
  # rebased branch must relay via the outbox bundle instead of a direct
  # force-push that would 403.
  local prior="$BATS_TEST_TMPDIR/prior"
  git clone -q "https://github.com/owner/repo.git" "$prior"
  git -C "$prior" checkout -b "agent/issue-7" "origin/main"
  echo "branch work" > "$prior/branch.txt"
  git -C "$prior" add branch.txt
  git -C "$prior" commit -q -m "feat: prior run work"
  git -C "$prior" push -q origin "agent/issue-7"

  local advance="$BATS_TEST_TMPDIR/advance"
  git clone -q "https://github.com/owner/repo.git" "$advance"
  echo "main advance" > "$advance/main_advance.txt"
  git -C "$advance" add main_advance.txt
  git -C "$advance" commit -q -m "chore: advance main"
  git -C "$advance" push -q origin HEAD:main

  export FAKE_GH_PR_LIST_7="https://github.com/owner/repo/pull/7"
  unset BOX_WRITE_ENABLED
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"

  local before_sha
  before_sha="$(git -C "$prior" rev-parse "agent/issue-7")"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # The remote branch must be untouched: no direct push in read-only mode.
  local after_sha
  after_sha="$(git --git-dir="$REMOTE_ROOT/owner/repo.git" rev-parse "refs/heads/agent/issue-7")"
  [ "$before_sha" = "$after_sha" ]

  [ -f "$OUTBOX_DIR/seam.bundle" ]
  run git -C "$WORK_DIR" bundle verify "$OUTBOX_DIR/seam.bundle"
  [ "$status" -eq 0 ]
}

@test "entrypoint bundling a rebase with no commits ahead of base is a no-op, not a failure" {
  # The adopted branch tip already equals origin/main, so the rebase is a no-op
  # and the outbox range origin/BASE_BRANCH..BRANCH is empty. `git bundle
  # create` refuses to write an empty bundle and exits non-zero. The read-write
  # push path tolerates that, so the read-only bundle path must too rather than
  # failing the whole box over nothing to relay (issue #1979).
  local prior="$BATS_TEST_TMPDIR/prior"
  git clone -q "https://github.com/owner/repo.git" "$prior"
  git -C "$prior" checkout -b "agent/issue-7" "origin/main"
  git -C "$prior" push -q origin "agent/issue-7"

  local advance="$BATS_TEST_TMPDIR/advance"
  git clone -q "https://github.com/owner/repo.git" "$advance"
  echo "main advance" > "$advance/main_advance.txt"
  git -C "$advance" add main_advance.txt
  git -C "$advance" commit -q -m "chore: advance main"
  git -C "$advance" push -q origin HEAD:main

  export FAKE_GH_PR_LIST_7="https://github.com/owner/repo/pull/7"
  unset BOX_WRITE_ENABLED
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -e "$OUTBOX_DIR/seam.bundle" ]
}

@test "entrypoint fails fast when pre-work rebase conflicts with latest main" {
  # A prior run modified README.md on the branch, then main landed a
  # conflicting change to the same file.
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

  # Open PR so the adoption path is taken (where the rebase is attempted).
  export FAKE_GH_PR_LIST_7="https://github.com/owner/repo/pull/7"

  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"pre-work rebase"* ]]
}

# A pre-work rebase conflict spawns an agent to resolve it. Only genuinely
# unresolvable conflicts fail the box (issue #216).

setup_rebase_conflict() {
  local prior advance
  prior="$BATS_TEST_TMPDIR/prior"
  advance="$BATS_TEST_TMPDIR/advance"

  git clone -q "https://github.com/owner/repo.git" "$prior"
  git -C "$prior" checkout -b "agent/issue-7" "origin/main"
  printf "branch version\n" > "$prior/README.md"
  git -C "$prior" add README.md
  git -C "$prior" commit -q -m "feat: branch modifies README"
  git -C "$prior" push -q origin "agent/issue-7"

  git clone -q "https://github.com/owner/repo.git" "$advance"
  printf "main version\n" > "$advance/README.md"
  git -C "$advance" add README.md
  git -C "$advance" commit -q -m "chore: main modifies README (conflicts)"
  git -C "$advance" push -q origin HEAD:main

  export FAKE_GH_PR_LIST_7="https://github.com/owner/repo/pull/7"
}

@test "pre-work rebase conflict: agent resolves and entrypoint continues" {
  setup_rebase_conflict
  # FAKE_DRIVER_RESOLVE_CONFLICT=1 makes the stub agent run git rebase --continue.
  export FAKE_DRIVER_RESOLVE_CONFLICT=1

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -d "$WORK_DIR/.git" ]
  grep -q "Implement GitHub issue #7" "$DRIVER_PROMPT_FILE"
  # FAKE_DRIVER_RESOLVE_CONFLICT stays exported for the whole run, so the main
  # agent invocation sees it too with no rebase left in progress. It must fall
  # through to a real outcome, or issue #1607's resume-once recovery kicks in
  # on a silent no-op.
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=.*status=ready' <<<"$output"
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 2 ]
}

# _write_env_handoff used to feed MAX_BUDGET_TOKENS/MAX_BUDGET_USD to `jq
# --argjson`, which requires valid JSON, so a malformed value failed that jq
# call and `set -euo pipefail` killed the whole box before
# phase_conflict_resolve's rebase-fixup pass finished. driver-exec env-handoff
# parses these leniently now, degrading a malformed value to 0 (issue #2975).
@test "pre-work rebase conflict: malformed MAX_BUDGET_TOKENS does not crash the pre-Handoff pass" {
  setup_rebase_conflict
  export FAKE_DRIVER_RESOLVE_CONFLICT=1
  export MAX_BUDGET_TOKENS="not-a-number"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
}

@test "pre-work rebase conflict: unresolvable conflict exits non-zero" {
  setup_rebase_conflict
  # No FAKE_DRIVER_RESOLVE_CONFLICT, so the stub leaves the rebase unfinished.

  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"pre-work rebase"* ]]
}

# An unresolvable-conflict run exits before the main agent, so it is the one
# case here where $DRIVER_PROMPT_FILE holds conflict-resolve-prompt.md: every
# other test also reaches the main agent, whose driver invocation
# overwrites the same capture file. This prompt renders through the bash-only
# `_subst` path, so nothing else sets CAVEMAN_STEP/SKILL_PREAMBLE (issue #2706).
@test "pre-work rebase conflict: unresolvable conflict prompt carries caveman directive when baked" {
  setup_rebase_conflict
  export HARNESS_SKILLS_DIR="$BATS_TEST_TMPDIR/harness-skills"
  mkdir -p "$HARNESS_SKILLS_DIR/caveman"
  cat >"$HARNESS_SKILLS_DIR/caveman/SKILL.md" <<'SKILL'
---
name: caveman
description: Ultra-compressed communication mode.
---
Respond terse like smart caveman.
SKILL
  # No FAKE_DRIVER_RESOLVE_CONFLICT, so the stub leaves the rebase unfinished.

  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  grep -q "Default to the \`/caveman\` skill" "$DRIVER_PROMPT_FILE"
  grep -q "Skills available:" "$DRIVER_PROMPT_FILE"
  grep -q "Skills available: caveman" "$DRIVER_PROMPT_FILE"
}

@test "pre-work rebase conflict: unresolvable conflict prompt has no caveman directive or literal tokens by default" {
  setup_rebase_conflict
  # No FAKE_DRIVER_RESOLVE_CONFLICT, so the stub leaves the rebase unfinished.

  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]

  run grep -q "Default to the \`/caveman\` skill" "$DRIVER_PROMPT_FILE"
  [ "$status" -ne 0 ]

  run grep -q '\${CAVEMAN_STEP}' "$DRIVER_PROMPT_FILE"
  [ "$status" -ne 0 ]

  run grep -q '\${SKILL_PREAMBLE}' "$DRIVER_PROMPT_FILE"
  [ "$status" -ne 0 ]

  # issue #3505: the code-comments policy is now inlined verbatim in
  # conflict-resolve-prompt.md, unconditionally rather than gated on a
  # code-comments skill probe, so it renders even with no code-comments
  # skill staged under HARNESS_SKILLS_DIR, and the ${CODE_COMMENTS_STEP}
  # literal (removed) never appears.
  grep -qi "non-obvious why" "$DRIVER_PROMPT_FILE"

  run grep -q '\${CODE_COMMENTS_STEP}' "$DRIVER_PROMPT_FILE"
  [ "$status" -ne 0 ]
}

@test "CONFLICT_RESOLVE_PR_URL: exits after resolving without running main agent" {
  setup_rebase_conflict
  export FAKE_DRIVER_RESOLVE_CONFLICT=1
  export CONFLICT_RESOLVE_PR_URL="https://github.com/owner/repo/pull/7"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # An absent issue prompt proves the main agent was never invoked.
  ! grep -q "Implement GitHub issue #7" "$DRIVER_PROMPT_FILE"
}

# issue #3505 removed the code-comments fragment/probe this test used to
# target; caveman-default.md is the only fragment phase_conflict_resolve
# still reads via `_subst` under `set -e`, so a missing copy must still abort
# the run. The copy-then-remove PROMPTS_DIR pattern is the one
# promptassembly's promptsDirMissingFragment test helper uses.
@test "pre-work rebase conflict: PROMPTS_DIR override missing fragments/caveman-default.md aborts the run when the skill is baked" {
  setup_rebase_conflict
  export HARNESS_SKILLS_DIR="$BATS_TEST_TMPDIR/harness-skills"
  mkdir -p "$HARNESS_SKILLS_DIR/caveman"
  cat >"$HARNESS_SKILLS_DIR/caveman/SKILL.md" <<'SKILL'
---
name: caveman
description: Ultra-compressed communication mode.
---
Respond terse like smart caveman.
SKILL
  local prompt_dir="$BATS_TEST_TMPDIR/prompts-missing-caveman"
  cp -r "$PROMPTS_DIR" "$prompt_dir"
  chmod -R u+w "$prompt_dir"
  rm "$prompt_dir/fragments/caveman-default.md"
  export PROMPTS_DIR="$prompt_dir"
  # FAKE_DRIVER_RESOLVE_CONFLICT is irrelevant here: the missing fragment
  # aborts phase_conflict_resolve before the driver is ever invoked.

  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"fragments/caveman-default.md"* ]]
}

# The caveman-directive tests above pin what the conflict-resolve prompt says,
# not whether the agent can resolve the skill it names. Claude Code finds a
# skill only under DRIVER_SKILLS_DIR, which main populates,
# yet phase_conflict_resolve runs first and may finish the box (issue #2354).
# The fake driver logs "skill discovered" only on a real find (issue #2706).
@test "pre-work rebase conflict: DRIVER_SKILLS_DIR is populated before the conflict-resolve agent runs" {
  setup_rebase_conflict
  export HARNESS_SKILLS_DIR="$BATS_TEST_TMPDIR/harness-skills"
  mkdir -p "$HARNESS_SKILLS_DIR/caveman"
  cat >"$HARNESS_SKILLS_DIR/caveman/SKILL.md" <<'SKILL'
---
name: caveman
description: Ultra-compressed communication mode.
---
Respond terse like smart caveman.
SKILL
  # No FAKE_DRIVER_RESOLVE_CONFLICT, so the stub leaves the rebase unfinished
  # and the conflict-resolve agent is this run's only driver invocation.

  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  grep -q "skill discovered: caveman" "$DRIVER_LOG"
}

# Same proof for the CONFLICT_RESOLVE_PR_URL resolve-only dispatch, where
# phase_conflict_resolve's own `exit 0` ends the box before box ever runs, so
# DRIVER_SKILLS_DIR population cannot depend on it.
@test "CONFLICT_RESOLVE_PR_URL: DRIVER_SKILLS_DIR is populated before the conflict-resolve agent runs" {
  setup_rebase_conflict
  export FAKE_DRIVER_RESOLVE_CONFLICT=1
  export CONFLICT_RESOLVE_PR_URL="https://github.com/owner/repo/pull/7"
  export HARNESS_SKILLS_DIR="$BATS_TEST_TMPDIR/harness-skills"
  mkdir -p "$HARNESS_SKILLS_DIR/caveman"
  cat >"$HARNESS_SKILLS_DIR/caveman/SKILL.md" <<'SKILL'
---
name: caveman
description: Ultra-compressed communication mode.
---
Respond terse like smart caveman.
SKILL

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "skill discovered: caveman" "$DRIVER_LOG"
}

# phase_conflict_resolve runs before main() execs box, so the
# CONFLICT_RESOLVE_PR_URL early exit fires before box assembles the prompt at
# all, not merely before its output is used (issue #2354). A
# nonexistent PROMPTASSEMBLY_REGISTRY_FILE fails any prompt assembly, so a green
# run here proves box is never reached.
@test "CONFLICT_RESOLVE_PR_URL: exits before box ever assembles the prompt" {
  setup_rebase_conflict
  export FAKE_DRIVER_RESOLVE_CONFLICT=1
  export CONFLICT_RESOLVE_PR_URL="https://github.com/owner/repo/pull/7"
  export PROMPTASSEMBLY_REGISTRY_FILE="$BATS_TEST_TMPDIR/does-not-exist-registry.json"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
}

@test "CONFLICT_RESOLVE_PR_URL read-only: bundles resolved branch to outbox instead of force-pushing" {
  # A conflict stops this box before phase_prework_rebase's own publish step,
  # and it exits without running the main agent, so this publish is the only
  # chance to land the resolved branch. It must relay via the outbox the same
  # way the read-only pre-work-rebase case does (issue #1979).
  setup_rebase_conflict
  export FAKE_DRIVER_RESOLVE_CONFLICT=1
  export CONFLICT_RESOLVE_PR_URL="https://github.com/owner/repo/pull/7"
  unset BOX_WRITE_ENABLED
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"

  local before_sha
  before_sha="$(git --git-dir="$REMOTE_ROOT/owner/repo.git" rev-parse "refs/heads/agent/issue-7")"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  local after_sha
  after_sha="$(git --git-dir="$REMOTE_ROOT/owner/repo.git" rev-parse "refs/heads/agent/issue-7")"
  [ "$before_sha" = "$after_sha" ]

  [ -f "$OUTBOX_DIR/seam.bundle" ]
  run git -C "$WORK_DIR" bundle verify "$OUTBOX_DIR/seam.bundle"
  [ "$status" -eq 0 ]
}

# A conflicted file that declares itself generated ("DO NOT EDIT" / "Code
# generated by X from Y") must be resolved by merging its source of truth and
# regenerating the artifact, never by hand-merging its own conflict markers
# (issue #403). The stub's FAKE_DRIVER_RESOLVE_CONFLICT mode regenerates such
# files and still accepts the incoming (theirs) side for ordinary ones.

seed_generated_file_fixture() {
  # Both diverging branches must inherit the same generation contract, so the
  # regen.sh and source.txt/generated.txt baseline lands on main first.
  local seed="$BATS_TEST_TMPDIR/seed-generated"
  git clone -q "https://github.com/owner/repo.git" "$seed"
  cat >"$seed/regen.sh" <<'SCRIPT'
#!/usr/bin/env bash
set -euo pipefail
printf '<!-- Code generated by regen.sh from source.txt. DO NOT EDIT. -->\nGENERATED: %s' \
  "$(cat source.txt)" >generated.txt
SCRIPT
  chmod +x "$seed/regen.sh"
  printf 'base\n' >"$seed/source.txt"
  ( cd "$seed" && bash regen.sh )
  git -C "$seed" add regen.sh source.txt generated.txt
  git -C "$seed" commit -q -m "chore: add generated-file fixture"
  git -C "$seed" push -q origin HEAD:main
}

setup_rebase_conflict_generated() {
  # source.txt and its regenerated artifact diverge on both the agent branch
  # and main, so the rebase conflicts in both files.
  seed_generated_file_fixture

  local prior advance
  prior="$BATS_TEST_TMPDIR/prior-gen"
  advance="$BATS_TEST_TMPDIR/advance-gen"

  git clone -q "https://github.com/owner/repo.git" "$prior"
  git -C "$prior" checkout -q -b "agent/issue-7" "origin/main"
  printf "branch source\n" >"$prior/source.txt"
  ( cd "$prior" && bash regen.sh )
  git -C "$prior" add source.txt generated.txt
  git -C "$prior" commit -q -m "feat: branch modifies source"
  git -C "$prior" push -q origin "agent/issue-7"

  git clone -q "https://github.com/owner/repo.git" "$advance"
  printf "main source\n" >"$advance/source.txt"
  ( cd "$advance" && bash regen.sh )
  git -C "$advance" add source.txt generated.txt
  git -C "$advance" commit -q -m "chore: main modifies source (conflicts)"
  git -C "$advance" push -q origin HEAD:main

  export FAKE_GH_PR_LIST_7="https://github.com/owner/repo/pull/7"
}

# CODE_FORGE=forgejo has PRs, but never on github.com and the Box carries no
# GH_TOKEN, so gh must never be consulted (issue #3942) -- the exact same
# start-fresh-no-gh contract as CODE_FORGE=local, just over a Forgejo remote.
@test "CODE_FORGE=forgejo starts fresh and calls no gh, even with a stale origin branch" {
  local forge_root="$BATS_TEST_TMPDIR/forge"
  mkdir -p "$forge_root/owner"
  git init --bare -q "$forge_root/owner/repo.git"
  local fseed="$BATS_TEST_TMPDIR/forge-seed"
  git clone -q "$forge_root/owner/repo.git" "$fseed"
  (
    cd "$fseed" || exit 1
    echo "# forge repo" >README.md
    git add -A
    git commit -q -m "chore: seed forge remote"
    git push -q origin HEAD:main
  )
  git config --global "url.file://$forge_root/.insteadOf" "https://fjtok@forge.test/"

  local stale_seed="$BATS_TEST_TMPDIR/forge-stale-seed"
  git clone -q "$forge_root/owner/repo.git" "$stale_seed"
  (
    cd "$stale_seed" || exit 1
    git checkout -q -b agent/issue-7
    echo "stale" >stale.txt
    git add -A
    git commit -q -m "chore: stale prior attempt"
    git push -q origin agent/issue-7
  )
  local before_sha
  before_sha="$(git --git-dir="$forge_root/owner/repo.git" rev-parse refs/heads/agent/issue-7)"

  export CODE_FORGE="forgejo"
  export BOX_FORGE_BACKEND=FORGEJO
  export FORGEJO_BASE_URL="https://forge.test"
  export FORGEJO_TOKEN="fjtok"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -s "$GH_LOG" ]
  run git -C "$WORK_DIR" rev-parse --abbrev-ref HEAD
  [ "$status" -eq 0 ]
  [ "$output" = "agent/issue-7" ]
  [ ! -f "$WORK_DIR/stale.txt" ]

  # No force-reset publish: the stale remote branch is left untouched.
  local after_sha
  after_sha="$(git --git-dir="$forge_root/owner/repo.git" rev-parse refs/heads/agent/issue-7)"
  [ "$before_sha" = "$after_sha" ]
}

# CODE_FORGE=git is a plain push-only remote with no PR concept at all (ADR
# 0013), so gh must never be consulted here either (issue #3942).
@test "CODE_FORGE=git starts fresh and never calls gh pr list, even with a stale origin branch" {
  local other_remote="$BATS_TEST_TMPDIR/other-remote.git"
  git init --bare -q "$other_remote"
  local seed="$BATS_TEST_TMPDIR/seed-other"
  git clone -q "$other_remote" "$seed"
  (
    cd "$seed" || exit 1
    echo "# other repo" >README.md
    git add -A
    git commit -q -m "chore: seed other remote"
    git push -q origin HEAD:main
  )

  local stale_seed="$BATS_TEST_TMPDIR/other-stale-seed"
  git clone -q "$other_remote" "$stale_seed"
  (
    cd "$stale_seed" || exit 1
    git checkout -q -b agent/issue-7
    echo "stale" >stale.txt
    git add -A
    git commit -q -m "chore: stale prior attempt"
    git push -q origin agent/issue-7
  )
  local before_sha
  before_sha="$(git --git-dir="$other_remote" rev-parse refs/heads/agent/issue-7)"

  export CODE_FORGE="git"
  export CODE_FORGE_REMOTE_URL="$other_remote"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # clone_repo still runs gh auth setup-git for CODE_FORGE=git; only the PR
  # query is ruled out here.
  run grep -q "pr list" "$GH_LOG"
  [ "$status" -eq 1 ]
  run git -C "$WORK_DIR" rev-parse --abbrev-ref HEAD
  [ "$status" -eq 0 ]
  [ "$output" = "agent/issue-7" ]
  [ ! -f "$WORK_DIR/stale.txt" ]

  # No force-reset publish: the stale remote branch is left untouched.
  local after_sha
  after_sha="$(git --git-dir="$other_remote" rev-parse refs/heads/agent/issue-7)"
  [ "$before_sha" = "$after_sha" ]
}

@test "pre-work rebase conflict on generated file: regenerates instead of hand-merging" {
  setup_rebase_conflict_generated
  export FAKE_DRIVER_RESOLVE_CONFLICT=1

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -d "$WORK_DIR/.git" ]

  ! grep -q '^<<<<<<<' "$WORK_DIR/source.txt"
  ! grep -q '^<<<<<<<' "$WORK_DIR/generated.txt"

  # Both sides' text present means a real merge, not a one-sided pick.
  grep -q 'branch source' "$WORK_DIR/source.txt"
  grep -q 'main source' "$WORK_DIR/source.txt"

  # A fresh regeneration from the resolved source that changes nothing proves
  # the artifact was regenerated, not hand-merged in place.
  local before after
  before="$(cat "$WORK_DIR/generated.txt")"
  ( cd "$WORK_DIR" && bash regen.sh )
  after="$(cat "$WORK_DIR/generated.txt")"
  [ "$before" = "$after" ]
}

