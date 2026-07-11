#!/usr/bin/env bats
# Skills discovery, prompt preference, and caveman-default narration (issues #118, #120, #487).

load helper

setup() {
  setup_entrypoint_env
}

# --- skills dir discovery path (issue #118) -----------------------------------
# Claude Code discovers skills from $HOME/.claude/skills/. In the box HOME is
# /home/agent (mkHarness.nix sets HOME=/home/agent for OCI; bwrap.go passes
# --setenv HOME /home/agent). The entrypoint invokes `claude -p` which
# discovers skills from HOME. The fake claude stub mirrors real discovery:
# it scans $HOME/.claude/skills/*.md and logs each file found. The test
# seeds a skill there and asserts the fake claude discovers it, proving the
# full discovery path without requiring a live LLM.
@test "headless agent discovers a skill seeded at HOME/.claude/skills" {
  mkdir -p "$HOME/.claude/skills"
  cat >"$HOME/.claude/skills/test-skill.md" <<'SKILL'
---
name: test-skill
description: A stub skill used only by this test.
---
Do the test thing.
SKILL
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # The fake claude reports each discovered skill; assert this one was found.
  grep -q "skill discovered: test-skill.md" "$CLAUDE_LOG"
}

# --- prompt skill preference (issue #120) -------------------------------------
# When a skill is present at HOME/.claude/skills/, the rendered prompt must
# direct the agent to use it. When absent, the inline guidance stands alone
# with no skill reference — the inline path is the floor, the skill the upgrade.

@test "prompt references available skill when present at HOME/.claude/skills" {
  mkdir -p "$HOME/.claude/skills"
  cat >"$HOME/.claude/skills/tdd.md" <<'SKILL'
---
name: tdd
description: Test-driven development skill.
---
Use TDD.
SKILL
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qi 'tdd' "$CLAUDE_PROMPT_FILE"
}

@test "prompt contains no skill reference when HOME/.claude/skills is empty" {
  # No skills seeded — inline guidance must stand alone; the word "skill"
  # must not appear so agents on skill-free boxes get only the inline path.
  mkdir -p "$HOME/.claude/skills"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -qi '\bskill\b' "$CLAUDE_PROMPT_FILE"
}

@test "prompt advertises /caveman when the caveman skill is baked (issue #486)" {
  # The dogfood Box bakes the pinned upstream caveman skill under the
  # basename caveman.md; discovery is name-driven (basename minus .md), so
  # a skill file at that basename must surface "caveman" in SKILLS_FOUND.
  mkdir -p "$HOME/.claude/skills"
  cat >"$HOME/.claude/skills/caveman.md" <<'SKILL'
---
name: caveman
description: Ultra-compressed communication mode.
---
Respond terse like smart caveman.
SKILL
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qi 'caveman' "$CLAUDE_PROMPT_FILE"
}

# --- caveman-default narration (issue #487) ---------------------------------
# #486 baked the skill; #487 makes the issue-pass prompt actually direct the
# agent to use it for narration by default -- distinct from the generic
# "skills available" mention SKILL_PREAMBLE already renders, which the test
# above already satisfies without this feature.

@test "prompt directs the agent to caveman narration by default when caveman is baked" {
  mkdir -p "$HOME/.claude/skills"
  cat >"$HOME/.claude/skills/caveman.md" <<'SKILL'
---
name: caveman
description: Ultra-compressed communication mode.
---
Respond terse like smart caveman.
SKILL
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qi 'narration' "$CLAUDE_PROMPT_FILE"
  grep -qi 'exempt' "$CLAUDE_PROMPT_FILE"
}

@test "prompt carries no caveman-default narration instruction when caveman is not baked" {
  mkdir -p "$HOME/.claude/skills"
  cat >"$HOME/.claude/skills/tdd.md" <<'SKILL'
---
name: tdd
description: Test-driven development skill.
---
Use TDD.
SKILL
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -qi 'narration' "$CLAUDE_PROMPT_FILE"
}

# The default applies to both agent passes (issue #487): CAVEMAN_STEP is
# substituted into the COMMS section, which fix-prompt.md receives via the
# shared-block injection (issue #455) rather than its own copy -- so this
# exercises _inject_shared_block's runtime _subst call directly, the same
# way the COMMS/CHECK/outcome injection tests above do.
@test "fix pass gets caveman-default narration via the injected COMMS block when caveman is baked" {
  export FIX_PASS="2"
  mkdir -p "$HOME/.claude/skills"
  cat >"$HOME/.claude/skills/caveman.md" <<'SKILL'
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
  grep -qi 'narration' "$CLAUDE_PROMPT_FILE"
  grep -qi 'exempt' "$CLAUDE_PROMPT_FILE"
}

