# The baked-skill name list (issue #2532): every skill entrypoint.sh probes at
# DRIVER_SKILLS_DIR/<name>/SKILL.md before the driver-exec assemble-prompt call.
# The probe stays at runtime so an operator skills mount can shadow the baked
# set. Single-sourcing the names here lets regen (nix/regen.nix) render every
# downstream copy from the row alone.

# goVar, field, and gate name those copies: assembleprompt_cmd.go's flag
# variable, promptassembly's Env field, and its Gates map key, which the
# fragment registry's own gate column also reads (issue #622). The flag name
# derives from name as "<name>-skill-baked". harnessOwned marks the rows
# lib/image.nix bakes into every image unconditionally (issues #2489, #2490).
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
    # issue #3220: the CHECK section's guidance, moved out of the
    # always-rendered prompt.
    name = "check-hygiene";
    goVar = "checkHygieneSkillBaked";
    field = "CheckHygieneSkillBaked";
    gate = "CHECK_HYGIENE_BAKED";
    harnessOwned = true;
  }
  {
    # issue #3221: the CODE COMMENTS section's comment-discipline rule, moved
    # out of the always-rendered prompt.
    name = "code-comments";
    goVar = "codeCommentsSkillBaked";
    field = "CodeCommentsSkillBaked";
    gate = "CODE_COMMENTS_BAKED";
    harnessOwned = true;
  }
  {
    # issue #3223: the dogfood-only Nix check lore. Deliberately not
    # harnessOwned, since nix/dogfood-skills.nix bakes it into spindrift's own
    # image alone, not into every Consumer image.
    name = "nix-checks";
    goVar = "nixChecksSkillBaked";
    field = "NixChecksSkillBaked";
    gate = "NIX_CHECKS_BAKED";
  }
]
