# Drift parity between templates/default/prompts/fragments/tdd-unbaked.md and the
# upstream `/tdd` skill (issue #3219). nix/checks/mk-fragment-parity.nix carries
# the full rationale; this file repeats only what differs.
{ mkFragmentParity, ... }:
let
  fallbackText = builtins.readFile ../../templates/default/prompts/fragments/tdd-unbaked.md;
  anchorText = builtins.readFile ../../templates/default/prompts/fragments/tdd-baked.md;

  skillDesc = "the upstream tdd SKILL.md (pinned `matt-skills` flake input, read via nix/dogfood-skills.nix)";
  fallbackDesc = "templates/default/prompts/fragments/tdd-unbaked.md";
  anchorDesc = "templates/default/prompts/fragments/tdd-baked.md";

  # Each row is one phrase both texts must contain. A clause earns its place only
  # if losing it from either side would mean the fallback and the skill teach
  # different disciplines, not merely phrase one differently.
  sharedClauses = [
    {
      name = "test-first";
      clause = "test-first";
    }
    {
      name = "one-slice-at-a-time";
      # The skill's vertical-slice rule. A fallback that lost it would teach the
      # batching the skill's horizontal-slicing anti-pattern forbids.
      clause = "one slice at a time";
    }
    {
      name = "failing-test";
      # Without this phrase the fallback is generic advice about testing rather
      # than the red-green loop.
      clause = "failing test";
    }
  ];

  # Phrases that belong only to the unbaked arm's step prose. The check matches them
  # against the anchor's normalized text, so the fallback's `REFACTOR` casing does
  # not matter.
  stepProseMarkers = [
    "red:"
    "green:"
    "refactor"
    "failing test"
    "never batch"
  ];
in
(mkFragmentParity {
  skillName = "tdd";
  sourceFile = "nix/checks/tdd-fragment-parity.nix";
  inherit
    skillDesc
    fallbackText
    fallbackDesc
    anchorText
    anchorDesc
    sharedClauses
    stepProseMarkers
    ;
  proseKind = "step prose";
  carriedKind = "prose";
  discipline = "test-first discipline";
}).checks
