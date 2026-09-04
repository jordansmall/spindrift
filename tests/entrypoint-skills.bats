#!/usr/bin/env bats
# Skills discovery, prompt preference, and caveman-default narration (issues #118, #120, #487).

load helper

setup() {
  setup_entrypoint_env
  # nix/checks/bats.nix exports SKILLS_TEMPLATE_DIR for the sandboxed runs,
  # where the repo tree isn't next to $BATS_TEST_DIRNAME; the fallback keeps a
  # bare `bats tests/` run working.
  skills_template_dir="${SKILLS_TEMPLATE_DIR:-$BATS_TEST_DIRNAME/../templates/default/skills}"
}

# --- skills dir discovery path (issue #118) -----------------------------------
# Claude Code discovers skills from $HOME/.claude/skills/. In the box HOME is
# /home/agent (mkHarness.nix sets HOME=/home/agent for OCI; bwrap.go passes
# --setenv HOME /home/agent). The entrypoint invokes `claude -p` which
# discovers skills from HOME. The fake claude stub mirrors real discovery:
# it scans $HOME/.claude/skills/*/SKILL.md and logs each skill dir found. The
# test seeds a skill there and asserts the fake claude discovers it, proving
# the full discovery path without requiring a live LLM.
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
  # The fake claude reports each discovered skill by its directory name; assert
  # this one was found.
  grep -q "skill discovered: test-skill" "$DRIVER_LOG"
}

# A bind mount placed directly onto DRIVER_SKILLS_DIR (how SPINDRIFT_SKILLS_DIR's
# runtime override works) always REPLACES its entire contents -- there's no
# union mount available in bwrap or a plain OCI volume mount. So a harness-owned
# skill baked at HARNESS_SKILLS_DIR would otherwise vanish entirely under an
# operator override. entrypoint.sh instead COPIES both HARNESS_SKILLS_DIR and
# OPERATOR_SKILLS_DIR into DRIVER_SKILLS_DIR before the discovery scan runs --
# copying is naturally additive/mergeable, mounts are not (issue #2489).
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

# issue #3220: /check-hygiene joins auto-format/auto-lint as a harness-owned
# skill -- its body ships in-repo (lib/image.nix's harnessSkills reads it
# straight from templates/default/skills) and the generated probe span
# forwards --check-hygiene-skill-baked once the Box has it, both of which
# follow from the lib/baked-skills.nix row alone.
@test "harness-owned check-hygiene skill ships a body and a baked probe (issue #3220)" {
  local skill="$skills_template_dir/check-hygiene/SKILL.md"
  [ -s "$skill" ]
  grep -qF 'name: check-hygiene' "$skill"
  grep -qF -- '--check-hygiene-skill-baked' "$ENTRYPOINT"
}

@test "check-hygiene skill carries the relocated log and killed-build guidance" {
  # issue #713: the #640 incident agent backgrounded the check build anyway
  # and polled for a NIXEXIT marker file. A SIGKILLed/OOM'd build never
  # writes that marker, so an unbounded poll for it hangs forever instead of
  # surfacing the kill as a failure. The primary rule stays inline in CHECK
  # ("never background it"); issue #3220 moved this defensive fallback, and
  # the bounded-log-reading discipline, into the harness-owned skill body.
  local skill="$skills_template_dir/check-hygiene/SKILL.md"
  grep -qi 'never `cat`' "$skill"
  grep -qi 'vanished' "$skill"
  grep -qi 'exit marker' "$skill"
  grep -qi 'bound the wait' "$skill"
}

# --- prompt skill preference (issue #120) -------------------------------------
# When a skill is present at HOME/.claude/skills/, the rendered prompt must
# direct the agent to use it. When absent, the inline guidance stands alone
# with no skill reference — the inline path is the floor, the skill the upgrade.

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
  # No skills seeded — inline guidance must stand alone; the word "skill"
  # must not appear so agents on skill-free boxes get only the inline path.
  mkdir -p "$HOME/.claude/skills"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -qi '\bskill\b' "$DRIVER_PROMPT_FILE"
}

@test "prompt advertises /caveman when the caveman skill is baked (issue #486)" {
  # The dogfood Box bakes the pinned upstream caveman skill as the directory
  # caveman/SKILL.md; discovery is name-driven (the skill dir basename), so a
  # skill at that path must surface "caveman" in SKILLS_FOUND.
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

# --- caveman-default narration (issue #487) ---------------------------------
# #486 baked the skill; #487 makes the issue-pass prompt actually direct the
# agent to use it for narration by default -- distinct from the generic
# "skills available" mention SKILL_PREAMBLE already renders, which the test
# above already satisfies without this feature.

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

# The default applies to both agent passes (issue #487): CAVEMAN_STEP is
# substituted into the COMMS section, which fix-prompt.md receives via the
# shared-block injection (issue #455) rather than its own copy -- so this
# exercises _inject_shared_block's runtime _subst call directly, the same
# way the COMMS/CHECK/outcome injection tests above do.
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


# --- per-skill placement: /tdd at IMPLEMENT, /commit at COMMIT ---------------
# The generic SKILL_PREAMBLE lists every baked skill; these steps additionally
# place the skill at the exact section whose inline guidance it owns, gated on
# that skill being baked. /commit is still an additive deferral; /tdd is an
# exactly-one-on pair since issue #3219 -- baking it replaces the inline
# red/green/refactor fallback with a bare anchor line rather than adding to it.

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
  # tdd-unbaked.md's inline steps are subtracted, not merely superseded.
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
  # commit-unbaked.md's inline format rules are subtracted, not merely
  # superseded.
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

# The /commit anchor sits in the COMMIT section, part of the CHECK/COMMIT
# block fix-prompt.md receives via the shared-block injection (issue #455),
# so a warm fix pass favors /commit too when the skill is baked.
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
