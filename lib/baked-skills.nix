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
    # code-comments still bakes into every image and stays invocable via
    # /code-comments; the gate is a bake probe only now, not a fragment
    # gate -- #3505 inlined the policy body into the prompts themselves.
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
  # Three pstack-derived principles, dogfood-only like nix-checks above but by
  # construction rather than choice: harnessSkills bakes a harnessOwned row
  # from templates/default/skills/<name>/SKILL.md in this repo, so a skill read
  # from a pinned input cannot be one. Promoting one means vendoring it first.
  {
    name = "principle-fix-root-causes";
    goVar = "principleFixRootCausesSkillBaked";
    field = "PrincipleFixRootCausesSkillBaked";
    gate = "PRINCIPLE_FIX_ROOT_CAUSES_BAKED";
  }
  {
    name = "principle-laziness-protocol";
    goVar = "principleLazinessProtocolSkillBaked";
    field = "PrincipleLazinessProtocolSkillBaked";
    gate = "PRINCIPLE_LAZINESS_PROTOCOL_BAKED";
  }
  {
    name = "principle-redesign-from-first-principles";
    goVar = "principleRedesignFromFirstPrinciplesSkillBaked";
    field = "PrincipleRedesignFromFirstPrinciplesSkillBaked";
    gate = "PRINCIPLE_REDESIGN_FROM_FIRST_PRINCIPLES_BAKED";
  }
]
