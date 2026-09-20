# Drift parity between the commit fallback fragment and the upstream `/commit`
# skill (issue #3222, following the tdd pattern from #3219).
# nix/checks/mk-fragment-parity.nix carries the full rationale; this file
# repeats only what differs.
{ mkFragmentParity, ... }:
let
  fallbackText = builtins.readFile ../../templates/default/prompts/fragments/commit-unbaked.md;
  anchorText = builtins.readFile ../../templates/default/prompts/fragments/commit-baked.md;

  skillDesc = "the upstream commit SKILL.md (pinned `jordan-skills` flake input, read via nix/dogfood-skills.nix)";
  fallbackDesc = "templates/default/prompts/fragments/commit-unbaked.md";
  anchorDesc = "templates/default/prompts/fragments/commit-baked.md";

  # Only two clauses are pinned. The skill and the fallback spell the wrap rule
  # and the column bounds differently ("hard line wraps" against "hard-wrap at
  # 72 columns", "≤ 50" against "≤50"), so pinning either would go red on a
  # copy-edit that changed nothing about the discipline. The fallback side's
  # own column bounds are pinned separately, against its own wording rather
  # than the skill's, by commit-unbaked-fragment-two-tier-subject-limit in
  # nix/checks/prompts.nix (issue #3478); #3486 tracks pinning them here,
  # against the skill.
  sharedClauses = [
    {
      name = "conventional-commits-v1-0-0";
      # Losing this from either side means the fallback is no longer pinned to
      # the same spec version the skill teaches.
      clause = "conventional commits v1.0.0";
    }
    {
      name = "hard-wrap";
      # The hyphenated form is the shared vocabulary ("hard-wrap at 72 columns"
      # in the skill, "hard-wrapped" in the fallback); the header's
      # unhyphenated "hard line wraps" is not.
      clause = "hard-wrap";
    }
  ];

  # Phrases that belong only to the unbaked arm's format-rule prose.
  stepProseMarkers = [
    "conventional commits v1.0.0"
    "hard-wrap"
    "subject"
    "self-evident"
  ];
in
(mkFragmentParity {
  skillName = "commit";
  sourceFile = "nix/checks/commit-fragment-parity.nix";
  inherit
    skillDesc
    fallbackText
    fallbackDesc
    anchorText
    anchorDesc
    sharedClauses
    stepProseMarkers
    ;
  proseKind = "format-rule prose";
  carriedKind = "prose";
  discipline = "commit-message discipline";
}).checks
