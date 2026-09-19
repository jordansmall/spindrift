# Drift parity between templates/default/prompts/fragments/tdd-unbaked.md and the
# upstream `/tdd` skill (issue #3219). It lives in Nix, not beside the Go
# fragment-parity test, because the upstream SKILL.md is not vendored: a Go test
# cannot reach the pinned `matt-skills` flake input. A verbatim diff would fail on
# every upstream copy-edit, so this check asserts only the shared vocabulary.
{ pkgs, fixtures, ... }:
let
  inherit (pkgs.lib)
    assertMsg
    concatStringsSep
    hasInfix
    toLower
    ;

  # By name, never by list index: nix/dogfood-skills.nix is an ordered list and a
  # reorder would otherwise point this check at a different skill.
  skillRowByName =
    name:
    let
      matches = builtins.filter (r: r.name == name) fixtures.dogfoodSkills;
    in
    if matches == [ ] then
      throw "nix/checks/tdd-fragment-parity.nix: no dogfood skill named \"${name}\" (nix/dogfood-skills.nix row may have been renamed or dropped)"
    else
      builtins.head matches;

  # The fallback hard-wraps at a different width than the skill and capitalizes the
  # loop's phases differently, so a raw substring match would fail over line breaks
  # and case. Clauses below are chosen not to straddle markdown emphasis, so the
  # skill's `**...**` wrappers need no stripping here.
  normalize =
    text:
    let
      words = builtins.filter (w: builtins.isString w && w != "") (builtins.split "[[:space:]]+" text);
    in
    toLower (concatStringsSep " " words);

  skillText = normalize (skillRowByName "tdd").src;
  fallbackText = normalize (
    builtins.readFile ../../templates/default/prompts/fragments/tdd-unbaked.md
  );
  anchorText = builtins.readFile ../../templates/default/prompts/fragments/tdd-baked.md;

  skillDesc = "the upstream tdd SKILL.md (pinned `matt-skills` flake input, read via nix/dogfood-skills.nix)";
  fallbackDesc = "templates/default/prompts/fragments/tdd-unbaked.md";
  remedy = "either re-sync the fallback with the skill, or -- if the skill's discipline genuinely changed -- update this check's clause list to match.";

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

  clauseCheck = c: {
    name = "tdd-fragment-parity-clause-${c.name}";
    value =
      let
        needle = normalize c.clause;
      in
      assert assertMsg (hasInfix needle skillText)
        "tdd fallback drift: ${skillDesc} no longer states \"${c.clause}\", which ${fallbackDesc} restates -- ${remedy}";
      assert assertMsg (hasInfix needle fallbackText)
        "tdd fallback drift: ${fallbackDesc} no longer states \"${c.clause}\", which ${skillDesc} teaches -- ${remedy}";
      pkgs.runCommand "tdd-fragment-parity-clause-${c.name}" { } "touch $out";
  };

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
  normalizedAnchor = normalize anchorText;
  leakedMarkers = builtins.filter (m: hasInfix m normalizedAnchor) stepProseMarkers;
  anchorLines = builtins.filter (l: builtins.isString l && normalize l != "") (
    builtins.split "\n" anchorText
  );
in
builtins.listToAttrs (map clauseCheck sharedClauses)
// {
  # Baking a skill subtracts prose: the baked arm names the skill and stops. An
  # edit that grew it back into a paragraph would restore the duplication the pair
  # exists to remove, and every clause check above would still pass.
  tdd-fragment-parity-baked-anchor-omits-step-prose =
    assert assertMsg (leakedMarkers == [ ])
      "templates/default/prompts/fragments/tdd-baked.md restates the unbaked arm's step prose (${concatStringsSep ", " leakedMarkers}) -- the baked arm must name the `/tdd` skill and stop, since the skill itself carries that prose in-box; move any wording worth keeping into ${fallbackDesc}.";
    assert assertMsg (builtins.length anchorLines == 1)
      "templates/default/prompts/fragments/tdd-baked.md is ${toString (builtins.length anchorLines)} non-empty lines, want 1 -- the baked arm is an anchor line, not a paragraph; prose belongs in ${fallbackDesc}.";
    assert assertMsg (hasInfix "/tdd" anchorText)
      "templates/default/prompts/fragments/tdd-baked.md no longer names the `/tdd` skill -- the baked arm's entire job is to point at the baked skill, so with the name gone the prompt says nothing about test-first discipline at all.";
    pkgs.runCommand "tdd-fragment-parity-baked-anchor-omits-step-prose" { } "touch $out";
}
