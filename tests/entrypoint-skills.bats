#!/usr/bin/env bats
# Skills discovery, prompt preference, and caveman-default narration (issues #118, #120, #487).

load helper

setup() {
  setup_entrypoint_env
  # nix/checks/bats.nix exports SKILLS_TEMPLATE_DIR for the sandboxed runs,
  # where the repo tree isn't next to $BATS_TEST_DIRNAME. The fallback keeps a
  # bare `bats tests/` run working.
  skills_template_dir="${SKILLS_TEMPLATE_DIR:-$BATS_TEST_DIRNAME/../templates/default/skills}"
  # _populate_driver_skills_dir copies HARNESS_SKILLS_DIR (/agent/skills) and
  # OPERATOR_SKILLS_DIR into DRIVER_SKILLS_DIR before every SKILLS_FOUND scan,
  # so on a Box that bakes its own skills the "not baked" assertions below
  # would see skills no test staged (issue #2059). A test that wants a baked
  # harness skill re-exports these itself.
  export HARNESS_SKILLS_DIR="$BATS_TEST_TMPDIR/no-harness-skills"
  export OPERATOR_SKILLS_DIR="$BATS_TEST_TMPDIR/no-operator-skills"
}

# Claude Code discovers skills from $HOME/.claude/skills/, and in the box HOME
# is /home/agent. The fake claude stub mirrors that scan and logs each skill
# dir it finds, so the discovery path is testable without a live LLM (#118).
@test "headless agent discovers a skill seeded at HOME/.claude/skills" {
  mkdir -p "$HOME/.claude/skills/test-skill"
  cat >"$HOME/.claude/skills/test-skill/SKILL.md" <<'SKILL'
---
name: test-skill
description: A stub skill used only by this test.
---
Do the test thing.
SKILL
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "skill discovered: test-skill" "$DRIVER_LOG"
}

# A bind mount onto DRIVER_SKILLS_DIR (how SPINDRIFT_SKILLS_DIR's runtime
# override works) replaces its entire contents, and neither bwrap nor a plain
# OCI volume mount offers a union mount. entrypoint.sh therefore copies both
# HARNESS_SKILLS_DIR and OPERATOR_SKILLS_DIR into DRIVER_SKILLS_DIR before the
# discovery scan: copying merges, mounting does not (issue #2489).
@test "harness-owned skill survives an operator skills override (issue #2489)" {
  export HARNESS_SKILLS_DIR="$BATS_TEST_TMPDIR/harness-skills"
  mkdir -p "$HARNESS_SKILLS_DIR/auto-format"
  cat >"$HARNESS_SKILLS_DIR/auto-format/SKILL.md" <<'SKILL'
---
name: auto-format
description: Auto-format the files changed in this run before committing.
---
Before committing, auto-format the files you changed.
SKILL

  export OPERATOR_SKILLS_DIR="$BATS_TEST_TMPDIR/operator-skills"
  mkdir -p "$OPERATOR_SKILLS_DIR/my-skill"
  cat >"$OPERATOR_SKILLS_DIR/my-skill/SKILL.md" <<'SKILL'
---
name: my-skill
description: An operator-supplied skill.
---
Do the operator thing.
SKILL

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "skill discovered: auto-format" "$DRIVER_LOG"
  grep -q "skill discovered: my-skill" "$DRIVER_LOG"
}

# Both the in-repo body and the probe flag follow from the skill's single
# lib/baked-skills.nix row (issue #3220).
@test "harness-owned check-hygiene skill ships a body and a baked probe (issue #3220)" {
  local skill="$skills_template_dir/check-hygiene/SKILL.md"
  [ -s "$skill" ]
  grep -qF 'name: check-hygiene' "$skill"
  grep -qF -- '--check-hygiene-skill-baked' "$ENTRYPOINT"
}

@test "check-hygiene skill carries the relocated log and killed-build guidance" {
  # issue #713: the #640 agent backgrounded the check build and polled for a
  # NIXEXIT marker. A SIGKILLed build never writes that marker, so the poll
  # hangs forever instead of reporting the kill. CHECK keeps the primary rule
  # inline; issue #3220 moved this fallback into the skill body.
  local skill="$skills_template_dir/check-hygiene/SKILL.md"
  grep -qi 'never `cat`' "$skill"
  grep -qi 'vanished' "$skill"
  grep -qi 'exit marker' "$skill"
  grep -qi 'bound the wait' "$skill"
}

# Body and probe flag both follow from one lib/baked-skills.nix row (#3221).
@test "harness-owned code-comments skill ships a body and a baked probe (issue #3221)" {
  local skill="$skills_template_dir/code-comments/SKILL.md"
  [ -s "$skill" ]
  grep -qF 'name: code-comments' "$skill"
  grep -qF -- '--code-comments-skill-baked' "$ENTRYPOINT"
}

# The inline guidance is the floor and a baked skill is the upgrade: when a
# skill is present the prompt must point at it, and when none is the prompt
# must not mention skills at all (issue #120).

@test "prompt references available skill when present at HOME/.claude/skills" {
  mkdir -p "$HOME/.claude/skills/tdd"
  cat >"$HOME/.claude/skills/tdd/SKILL.md" <<'SKILL'
---
name: tdd
description: Test-driven development skill.
---
Use TDD.
SKILL
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qi 'tdd' "$DRIVER_PROMPT_FILE"
}

@test "prompt contains no skill reference when HOME/.claude/skills is empty" {
  mkdir -p "$HOME/.claude/skills"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -qi '\bskill\b' "$DRIVER_PROMPT_FILE"
}

@test "prompt advertises /caveman when the caveman skill is baked (issue #486)" {
  # Discovery is driven by the skill directory's basename, not the SKILL.md
  # front matter, so the dir must be named caveman to reach SKILLS_FOUND.
  mkdir -p "$HOME/.claude/skills/caveman"
  cat >"$HOME/.claude/skills/caveman/SKILL.md" <<'SKILL'
---
name: caveman
description: Ultra-compressed communication mode.
---
Respond terse like smart caveman.
SKILL
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qi 'caveman' "$DRIVER_PROMPT_FILE"
}

# #486 baked the skill; #487 makes the issue-pass prompt direct the agent to
# narrate with it by default. That is a separate assertion from the generic
# "skills available" mention SKILL_PREAMBLE renders, which the test above
# already satisfies without this feature.

@test "prompt directs the agent to caveman narration by default when caveman is baked" {
  mkdir -p "$HOME/.claude/skills/caveman"
  cat >"$HOME/.claude/skills/caveman/SKILL.md" <<'SKILL'
---
name: caveman
description: Ultra-compressed communication mode.
---
Respond terse like smart caveman.
SKILL
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qi 'narration' "$DRIVER_PROMPT_FILE"
  grep -qi 'exempt' "$DRIVER_PROMPT_FILE"
}

@test "prompt carries no caveman-default narration instruction when caveman is not baked" {
  mkdir -p "$HOME/.claude/skills/tdd"
  cat >"$HOME/.claude/skills/tdd/SKILL.md" <<'SKILL'
---
name: tdd
description: Test-driven development skill.
---
Use TDD.
SKILL
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -qi 'narration' "$DRIVER_PROMPT_FILE"
}

# The default applies to both agent passes (issue #487). CAVEMAN_STEP is
# substituted into the COMMS section, which fix-prompt.md receives through the
# shared-block injection (issue #455) rather than its own copy, so this
# exercises _inject_shared_block's runtime _subst call.
@test "fix pass gets caveman-default narration via the injected COMMS block when caveman is baked" {
  export FIX_PASS="2"
  mkdir -p "$HOME/.claude/skills/caveman"
  cat >"$HOME/.claude/skills/caveman/SKILL.md" <<'SKILL'
---
name: caveman
description: Ultra-compressed communication mode.
---
Respond terse like smart caveman.
SKILL
  export COMMS_CONTRACT_FILE="$BATS_TEST_TMPDIR/comms-contract.md"
  printf '# COMMS\n\n%sbody text\n' '${CAVEMAN_STEP}' >"$COMMS_CONTRACT_FILE"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qi 'narration' "$DRIVER_PROMPT_FILE"
  grep -qi 'exempt' "$DRIVER_PROMPT_FILE"
}


# SKILL_PREAMBLE lists every baked skill; these steps also place a skill at
# the section whose inline guidance it owns. /commit is an additive deferral.
# /tdd is an exactly-one-on pair since issue #3219: baking it replaces the
# inline red/green/refactor fallback instead of adding to it.

@test "prompt anchors the test-first workflow to /tdd when the tdd skill is baked" {
  mkdir -p "$HOME/.claude/skills/tdd"
  cat >"$HOME/.claude/skills/tdd/SKILL.md" <<'SKILL'
---
name: tdd
description: Test-driven development skill.
---
Use TDD.
SKILL
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'Work test-first: run `/tdd` for each slice.' "$DRIVER_PROMPT_FILE"
  # tdd-unbaked.md's inline steps are subtracted, not superseded.
  ! grep -qF 'RED: write ONE failing test' "$DRIVER_PROMPT_FILE"
}

@test "prompt carries the inline test-first fallback when the tdd skill is not baked" {
  mkdir -p "$HOME/.claude/skills/caveman"
  cat >"$HOME/.claude/skills/caveman/SKILL.md" <<'SKILL'
---
name: caveman
description: Ultra-compressed communication mode.
---
Respond terse like smart caveman.
SKILL
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'RED: write ONE failing test' "$DRIVER_PROMPT_FILE"
  ! grep -qF 'Work test-first: run `/tdd` for each slice.' "$DRIVER_PROMPT_FILE"
}

@test "prompt anchors on /commit when the commit skill is baked" {
  mkdir -p "$HOME/.claude/skills/commit"
  cat >"$HOME/.claude/skills/commit/SKILL.md" <<'SKILL'
---
name: commit
description: Conventional commit messages.
---
Write conventional commits.
SKILL
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'Use the `/commit` skill to write every commit message.' "$DRIVER_PROMPT_FILE"
  # commit-unbaked.md's inline format rules are subtracted, not superseded.
  ! grep -qi 'hard-wrapped (subject' "$DRIVER_PROMPT_FILE"
}

@test "prompt carries the inline Conventional Commits format rules when the commit skill is not baked" {
  mkdir -p "$HOME/.claude/skills/caveman"
  cat >"$HOME/.claude/skills/caveman/SKILL.md" <<'SKILL'
---
name: caveman
description: Ultra-compressed communication mode.
---
Respond terse like smart caveman.
SKILL
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qi 'hard-wrapped (subject' "$DRIVER_PROMPT_FILE"
  ! grep -qF 'Use the `/commit` skill to write every commit message.' "$DRIVER_PROMPT_FILE"
}

# The /commit anchor sits in the COMMIT section, part of the CHECK/COMMIT block
# fix-prompt.md receives through the shared-block injection (issue #455), so a
# warm fix pass favors /commit too when the skill is baked.
@test "fix pass gets the /commit anchor via the injected CHECK/COMMIT block when commit is baked" {
  export FIX_PASS="2"
  mkdir -p "$HOME/.claude/skills/commit"
  cat >"$HOME/.claude/skills/commit/SKILL.md" <<'SKILL'
---
name: commit
description: Conventional commit messages.
---
Write conventional commits.
SKILL
  export CHECK_CONTRACT_FILE="$BATS_TEST_TMPDIR/check-contract.md"
  printf '# CHECK\n\n%s%sStrict Conventional Commits.\n' '${COMMIT_BAKED_STEP}' '${COMMIT_UNBAKED_STEP}' >"$CHECK_CONTRACT_FILE"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'Use the `/commit` skill to write every commit message.' "$DRIVER_PROMPT_FILE"
}
