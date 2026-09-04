# The baked-skill name list (issue #2532): every skill entrypoint.sh probes
# at DRIVER_SKILLS_DIR/<name>/SKILL.md before the driver-exec assemble-prompt
# call, single-sourced here beside the fragment registry's own *_BAKED gate
# rows (lib/fragments.nix's CAVEMAN_BAKED/TDD_BAKED/COMMIT_BAKED/
# CODE_REVIEW_BAKED, issue #622) instead of four hand-mirrored copies across
# agent/entrypoint.sh's probe list, driver-exec/assembleprompt_cmd.go's flags,
# and promptassembly's Env fields/Gates map. Growing this list to include
# auto-format/auto-lint (the harness-owned skills lib/image.nix's
# harnessSkills bakes unconditionally, issues #2489/#2490) needs no consumer
# edit -- regen (nix/regen.nix) renders every downstream copy from the row
# alone. The runtime probe itself (checking the file actually landed at
# DRIVER_SKILLS_DIR/<name>/SKILL.md, so an operator skills mount can still
# shadow the baked set) is unchanged; only the name list feeding it is
# rooted here.
#
# Each row:
#   name  - the skill directory basename under DRIVER_SKILLS_DIR (the same
#           `name` field dogfood-skills.nix/image.nix's harnessSkills rows
#           use). The driver-exec `assemble-prompt` CLI flag name --
#           forwarded by entrypoint.sh only when the probe finds
#           DRIVER_SKILLS_DIR/<name>/SKILL.md -- derives from it as
#           "${name}-skill-baked"; it isn't a separate field since it never
#           varies independently of name.
#   goVar - the local Go variable name assembleprompt_cmd.go's flag parsing
#           binds the flag to.
#   field - the promptassembly.Env struct field name.
#   gate  - the Gates()-returned map key -- the same name the fragment
#           registry's own `gate` column reads via "${!_fgate}" bash-side.
#   harnessOwned - true only on the harness-owned rows lib/image.nix's
#           harnessSkills bakes into every image unconditionally (issues
#           #2489, #2490); absent (defaults false) on every other row.
[
  {
    name = "caveman";
    goVar = "cavemanSkillBaked";
    field = "CavemanSkillBaked";
    gate = "CAVEMAN_BAKED";
  }
  {
    name = "tdd";
    goVar = "tddSkillBaked";
    field = "TDDSkillBaked";
    gate = "TDD_BAKED";
  }
  {
    name = "commit";
    goVar = "commitSkillBaked";
    field = "CommitSkillBaked";
    gate = "COMMIT_BAKED";
  }
  {
    name = "code-review";
    goVar = "codeReviewSkillBaked";
    field = "CodeReviewSkillBaked";
    gate = "CODE_REVIEW_BAKED";
  }
  {
    name = "auto-format";
    goVar = "autoFormatSkillBaked";
    field = "AutoFormatSkillBaked";
    gate = "AUTO_FORMAT_BAKED";
    harnessOwned = true;
  }
  {
    name = "auto-lint";
    goVar = "autoLintSkillBaked";
    field = "AutoLintSkillBaked";
    gate = "AUTO_LINT_BAKED";
    harnessOwned = true;
  }
  {
    # issue #3220: the CHECK section's log-reading, foreground-gate, and
    # killed-build guidance, relocated out of the always-rendered prompt.
    name = "check-hygiene";
    goVar = "checkHygieneSkillBaked";
    field = "CheckHygieneSkillBaked";
    gate = "CHECK_HYGIENE_BAKED";
    harnessOwned = true;
  }
  {
    # issue #3221: the CODE COMMENTS section's five-line comment-discipline
    # rule, relocated out of the always-rendered prompt the same way #3220
    # relocated CHECK's log-reading/foreground-gate/killed-build guidance.
    name = "code-comments";
    goVar = "codeCommentsSkillBaked";
    field = "CodeCommentsSkillBaked";
    gate = "CODE_COMMENTS_BAKED";
    harnessOwned = true;
  }
  {
    # issue #3223: the dogfood-only Nix check lore (scoped target over full
    # flake check, git-add-before-first-build, devShell/pinned-toolchain
    # preference) -- unlike its neighbours above, deliberately NOT
    # harnessOwned since it's baked only into spindrift's own dogfood image
    # (nix/dogfood-skills.nix), not into every Consumer image.
    name = "nix-checks";
    goVar = "nixChecksSkillBaked";
    field = "NixChecksSkillBaked";
    gate = "NIX_CHECKS_BAKED";
  }
]
