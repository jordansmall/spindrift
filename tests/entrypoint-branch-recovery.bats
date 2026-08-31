#!/usr/bin/env bats
# Pre-work rebase and conflict resolution, including generated files (issues #215, #216, #403).

load helper

setup() {
  setup_entrypoint_env
}

# --- pre-work rebase (issue #215) -------------------------------------------
# Before the agent starts, the box must rebase the working branch onto the
# latest origin/BASE_BRANCH so the agent works against current main rather
# than the state of origin at clone time.

@test "entrypoint rebases prior work onto latest origin/BASE_BRANCH before agent starts" {
  # Simulate a prior run: agent/issue-7 was pushed with a commit, then main
  # advanced with a non-conflicting change while the branch was in flight.
  local prior="$BATS_TEST_TMPDIR/prior"
  git clone -q "https://github.com/owner/repo.git" "$prior"
  git -C "$prior" checkout -b "agent/issue-7" "origin/main"
  echo "branch work" > "$prior/branch.txt"
  git -C "$prior" add branch.txt
  git -C "$prior" commit -q -m "feat: prior run work"
  git -C "$prior" push -q origin "agent/issue-7"

  # Advance main with a non-conflicting commit (simulates a refactor landing
  # on main while the branch was in flight).
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

  # After the pre-work rebase the working branch must be on top of the latest
  # main: it should have both the prior branch work and the main advance.
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
  # Same prior-work/main-advance setup as the read-write case above, but with
  # BOX_WRITE_ENABLED unset (issue #1979): the box holds no push-capable
  # token, so publishing the rebased branch must relay via the outbox bundle
  # instead of a direct force-push that would 403.
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

  # The remote branch must be untouched -- no direct push in read-only mode.
  local after_sha
  after_sha="$(git --git-dir="$REMOTE_ROOT/owner/repo.git" rev-parse "refs/heads/agent/issue-7")"
  [ "$before_sha" = "$after_sha" ]

  # The rebased tree must instead be relayed via the outbox bundle.
  [ -f "$OUTBOX_DIR/seam.bundle" ]
  run git -C "$WORK_DIR" bundle verify "$OUTBOX_DIR/seam.bundle"
  [ "$status" -eq 0 ]
}

@test "entrypoint bundling a rebase with no commits ahead of base is a no-op, not a failure" {
  # The adopted branch has no work of its own yet (its tip already equals
  # origin/main) -- the rebase is a no-op fast-forward, so the outbox range
  # origin/BASE_BRANCH..BRANCH is empty. `git bundle create` refuses to write
  # an empty bundle and exits non-zero; the read-write push path already
  # tolerates this (a force-with-lease push of an unchanged ref no-ops), so
  # the read-only bundle path must tolerate it the same way rather than
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
  # Simulate a prior run that modified README.md on the branch, then main
  # landed a conflicting change to the same file.
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

# --- pre-work rebase conflict resolution (issue #216) -------------------------
# When a pre-work rebase conflict occurs, an agent is spawned to resolve it.
# Only genuinely unresolvable conflicts fail the box.

setup_rebase_conflict() {
  # Helper: push a conflicting README.md change from a prior run, then advance
  # main with a different conflicting change, and open a fake PR.
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
  # Working dir must exist (clone succeeded and rebase completed).
  [ -d "$WORK_DIR/.git" ]
  # The main agent prompt must have been passed to claude.
  grep -q "Implement GitHub issue #7" "$DRIVER_PROMPT_FILE"
  # FAKE_DRIVER_RESOLVE_CONFLICT stays exported for the whole run, so the main
  # agent invocation sees it too, with no rebase left in progress -- it must
  # fall through to a real outcome (issue #1607's resume-once recovery would
  # otherwise kick in on the silent no-op this used to be).
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=.*status=ready' <<<"$output"
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 2 ]
}

# Blocking review finding A (issue #2975 slice 3): _write_env_handoff used to
# feed MAX_BUDGET_TOKENS/MAX_BUDGET_USD to `jq --argjson`, which requires
# valid JSON -- a malformed value made that jq call itself fail, and under
# entrypoint.sh's `set -euo pipefail` that killed the whole box run before
# phase_conflict_resolve's rebase-fixup pass ever finished. driver-exec
# env-handoff instead parses these leniently (degrading a malformed value to
# 0), so the same malformed input must no longer take the run down.
@test "pre-work rebase conflict: malformed MAX_BUDGET_TOKENS does not crash the pre-Handoff pass" {
  setup_rebase_conflict
  export FAKE_DRIVER_RESOLVE_CONFLICT=1
  export MAX_BUDGET_TOKENS="not-a-number"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
}

@test "pre-work rebase conflict: unresolvable conflict exits non-zero" {
  setup_rebase_conflict
  # No FAKE_DRIVER_RESOLVE_CONFLICT — stub does not complete the rebase.

  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"pre-work rebase"* ]]
}

# The unresolvable-conflict test above is the one place in this suite where
# only a single driver invocation ever happens (the run exits before ever
# reaching the main agent), so $DRIVER_PROMPT_FILE unambiguously holds
# conflict-resolve-prompt.md's own rendered content -- every other test here
# reaches phase_prompt_assembly too, whose own driver invocation overwrites
# the same fixed-path capture file. Pins phase_conflict_resolve's own
# CAVEMAN_STEP/SKILL_PREAMBLE precompute (issue #2706): unlike
# phase_prompt_assembly, this prompt renders through the bash-only `_subst`
# path, so nothing else populates these two vars for this call site.
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
  # No FAKE_DRIVER_RESOLVE_CONFLICT — stub does not complete the rebase.

  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  grep -q "Default to the \`/caveman\` skill" "$DRIVER_PROMPT_FILE"
  grep -q "Skills available:" "$DRIVER_PROMPT_FILE"
  grep -q "Skills available: caveman" "$DRIVER_PROMPT_FILE"
}

@test "pre-work rebase conflict: unresolvable conflict prompt has no caveman directive or literal tokens by default" {
  setup_rebase_conflict
  export HARNESS_SKILLS_DIR="$BATS_TEST_TMPDIR/no-harness-skills"
  # No FAKE_DRIVER_RESOLVE_CONFLICT — stub does not complete the rebase.

  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]

  run grep -q "Default to the \`/caveman\` skill" "$DRIVER_PROMPT_FILE"
  [ "$status" -ne 0 ]

  run grep -q '\${CAVEMAN_STEP}' "$DRIVER_PROMPT_FILE"
  [ "$status" -ne 0 ]

  run grep -q '\${SKILL_PREAMBLE}' "$DRIVER_PROMPT_FILE"
  [ "$status" -ne 0 ]
}

# Unlike CAVEMAN_STEP/SKILL_PREAMBLE above, CODE_COMMENTS_STEP (issue #2880)
# is set unconditionally in phase_conflict_resolve -- no caveman/skills gate
# -- because resolving a rebase conflict always edits code, so the
# comment-discipline rule always applies. This test uses the same minimal
# no-harness-skills setup as the "by default" test above to prove the rule
# renders even when nothing is baked.
@test "pre-work rebase conflict: unresolvable conflict prompt carries code-comments rule unconditionally" {
  setup_rebase_conflict
  export HARNESS_SKILLS_DIR="$BATS_TEST_TMPDIR/no-harness-skills"
  # No FAKE_DRIVER_RESOLVE_CONFLICT — stub does not complete the rebase.

  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  grep -q "proportional to the size of the change" "$DRIVER_PROMPT_FILE"

  run grep -q '\${CODE_COMMENTS_STEP}' "$DRIVER_PROMPT_FILE"
  [ "$status" -ne 0 ]
}

# CODE_COMMENTS_STEP's precompute (line ~937) is an unguarded
# `_subst "${PROMPTS_DIR}/fragments/code-comments.md"` read under
# `set -euo pipefail`, with no `-f` existence check first (unlike the
# CAVEMAN_STEP/SKILL_PREAMBLE precomputes just above it, which are gated on
# a file check and so degrade gracefully). A SPINDRIFT_PROMPT_DIR override
# (docs/reference.md) that forgets fragments/code-comments.md therefore
# aborts the whole box the moment it hits a rebase conflict, rather than
# rendering the conflict-resolve prompt without the rule. Mirrors the
# "entrypoint does not require filer-prompt.md" test's copy-then-remove
# PROMPTS_DIR override pattern (tests/entrypoint-prompt-fragments.bats).
@test "pre-work rebase conflict: PROMPTS_DIR override missing fragments/code-comments.md aborts the run" {
  setup_rebase_conflict
  local prompt_dir="$BATS_TEST_TMPDIR/prompts-missing-code-comments"
  cp -r "$PROMPTS_DIR" "$prompt_dir"
  chmod -R u+w "$prompt_dir"
  rm "$prompt_dir/fragments/code-comments.md"
  export PROMPTS_DIR="$prompt_dir"
  # No FAKE_DRIVER_RESOLVE_CONFLICT — irrelevant here: the missing fragment
  # aborts phase_conflict_resolve before the driver is ever invoked.

  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"fragments/code-comments.md"* ]]
}

@test "CONFLICT_RESOLVE_PR_URL: exits after resolving without running main agent" {
  setup_rebase_conflict
  export FAKE_DRIVER_RESOLVE_CONFLICT=1
  export CONFLICT_RESOLVE_PR_URL="https://github.com/owner/repo/pull/7"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # Main agent must NOT have been invoked — the issue prompt should be absent.
  ! grep -q "Implement GitHub issue #7" "$DRIVER_PROMPT_FILE"
}

# Complements the two "carries caveman directive"/"has no caveman directive"
# tests above: those pin what CAVEMAN_STEP/SKILL_PREAMBLE render into the
# conflict-resolve prompt text, but not whether the skill that text tells the
# agent to invoke is actually resolvable when that agent runs. Claude Code
# discovers a skill only from DRIVER_SKILLS_DIR ($HOME/.claude/skills in this
# harness, mirrors tests/entrypoint-skills.bats), which phase_prompt_assembly
# populates -- but phase_conflict_resolve now runs (and, on either of its two
# early-exit paths, may finish the whole box) before phase_prompt_assembly
# ever does (issue #2354 slice 3). The fake driver logs "skill discovered:
# <name>" only when it actually finds the skill under DRIVER_SKILLS_DIR at
# its own invocation time, so this proves the population happened in time
# for the conflict-resolve agent specifically, not merely that the prompt
# text mentions the skill (issue #2706).
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
  # No FAKE_DRIVER_RESOLVE_CONFLICT — stub does not complete the rebase, so
  # the conflict-resolve agent is the only driver invocation this run makes.

  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  grep -q "skill discovered: caveman" "$DRIVER_LOG"
}

# Same proof as the test above, for the CONFLICT_RESOLVE_PR_URL resolve-only
# dispatch mode: phase_prompt_assembly never runs at all for this dispatch
# (phase_conflict_resolve's own `exit 0` ends the box first), so this is the
# one path where DRIVER_SKILLS_DIR population must not depend on
# phase_prompt_assembly running afterward -- it never gets the chance to.
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

# Pins the hoist (issue #2354 slice 3): phase_conflict_resolve's call site
# now runs in main() BEFORE phase_prompt_assembly, so the CONFLICT_RESOLVE_PR_URL
# early exit fires before driver-exec assemble-prompt is ever invoked at all --
# not merely before its output is used. Pointing PROMPTASSEMBLY_REGISTRY_FILE
# at a nonexistent path makes any assemble-prompt call fail loudly (the verb
# requires --registry to exist, and entrypoint.sh's bare call has no error
# handling of its own, so a nonzero exit there would propagate straight
# through `set -euo pipefail` and abort the whole run non-zero). A green
# `status -eq 0` here is only possible if the verb is never called.
@test "CONFLICT_RESOLVE_PR_URL: exits before phase_prompt_assembly ever invokes driver-exec assemble-prompt" {
  setup_rebase_conflict
  export FAKE_DRIVER_RESOLVE_CONFLICT=1
  export CONFLICT_RESOLVE_PR_URL="https://github.com/owner/repo/pull/7"
  export PROMPTASSEMBLY_REGISTRY_FILE="$BATS_TEST_TMPDIR/does-not-exist-registry.json"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
}

@test "CONFLICT_RESOLVE_PR_URL read-only: bundles resolved branch to outbox instead of force-pushing" {
  # This box never reaches phase_prework_rebase's own publish step (a
  # conflict was hit) and exits without running the main agent afterward
  # (line 396-401), so this publish is the only chance to land the resolved
  # branch at all -- it must relay via the outbox the same way the read-only
  # pre-work-rebase case does (issue #1979).
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

# --- pre-work rebase conflict on a generated file (issue #403) ---------------
# A conflicted file that declares itself generated ("DO NOT EDIT" / "Code
# generated by X from Y") must be resolved by merging in its source of truth
# and regenerating the artifact, never by hand-merging its own conflict
# markers. The fake `claude` stub's FAKE_DRIVER_RESOLVE_CONFLICT mode encodes
# this: generated files are regenerated from a merged source; ordinary files
# still fall back to accepting the incoming (theirs) side.

seed_generated_file_fixture() {
  # Push a regen.sh + baseline source.txt/generated.txt pair to main so both
  # diverging branches inherit the same generation contract.
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
  # Helper: diverge source.txt (and its regenerated artifact) on both the
  # agent branch and main so rebasing conflicts in both files.
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

@test "pre-work rebase conflict on generated file: regenerates instead of hand-merging" {
  setup_rebase_conflict_generated
  export FAKE_DRIVER_RESOLVE_CONFLICT=1

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -d "$WORK_DIR/.git" ]

  # No conflict markers left in either file.
  ! grep -q '^<<<<<<<' "$WORK_DIR/source.txt"
  ! grep -q '^<<<<<<<' "$WORK_DIR/generated.txt"

  # The source of truth carries both sides' intent — a real merge, not a
  # one-sided pick.
  grep -q 'branch source' "$WORK_DIR/source.txt"
  grep -q 'main source' "$WORK_DIR/source.txt"

  # The generated artifact matches regenerating fresh from the resolved
  # source — proof it was regenerated, not hand-merged in place.
  local before after
  before="$(cat "$WORK_DIR/generated.txt")"
  ( cd "$WORK_DIR" && bash regen.sh )
  after="$(cat "$WORK_DIR/generated.txt")"
  [ "$before" = "$after" ]
}

