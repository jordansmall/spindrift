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

  # The column bounds are pinned here against the skill; normalize folds the
  # comparator spacing ("≤ 50" against "≤50"). The fallback's exact two-tier
  # wording is pinned separately by commit-unbaked-fragment-two-tier-subject-limit
  # in nix/checks/prompts.nix (issue #3478).
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
    # The skill states each bound twice, in the "Length and wrapping" section
    # and in its description of the check-msg.sh lint, so both are pinned; a
    # change to either alone must trip the check. The fallback states each once.
    {
      name = "subject-bound-rule";
      skillClause = "aim for 50 or fewer";
      fallbackClause = "subject ≤50";
    }
    {
      name = "subject-bound-lint";
      skillClause = "warning over 50";
      fallbackClause = "subject ≤50";
    }
    {
      name = "subject-ceiling-rule";
      # Skill: "**72 characters is the hard limit**"; the needle stays inside
      # the emphasis so it does not straddle the markdown.
      skillClause = "72 characters is the hard limit";
      fallbackClause = "never exceed 72";
    }
    {
      name = "subject-ceiling-lint";
      skillClause = "error over 72";
      fallbackClause = "never exceed 72";
    }
    {
      name = "body-bound-rule";
      # Skill: "hard-wrap at **72 columns**"; same emphasis caveat as above.
      skillClause = "72 columns";
      fallbackClause = "body ≤72";
    }
    {
      name = "body-bound-lint";
      skillClause = "72-column wrapping";
      fallbackClause = "body ≤72";
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
