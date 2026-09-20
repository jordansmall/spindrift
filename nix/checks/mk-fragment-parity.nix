# Shared machinery behind the fragment-parity checks (issue #3240). Each call
# site (commit, tdd, code-review) pins its own clause list, prose markers, and
# skill; this file owns only what all three do identically.
#
# These checks live in Nix, not beside the Go fragment-parity test, because
# the upstream SKILL.md is not vendored: a Go test cannot reach the pinned
# skills flake input. A verbatim diff would fail on every upstream copy-edit,
# so each check asserts only the shared vocabulary.
{ pkgs, fixtures }:
{
  # `skillName` serves four roles at once -- derivation prefix, dogfood-row
  # lookup key, drift-message prefix, and the `/<name>` anchor token -- because
  # they coincide for all three call sites; a skill whose dogfood row name ever
  # differs from its slash-command name is what splits it. `sourceFile` and
  # `anchorDesc` stay explicit for the inverse reason: a name-derived default
  # would go stale on exactly the rename `sourceFile` exists to attribute.
  skillName,
  sourceFile,
  skillDesc,
  fallbackText,
  fallbackDesc,
  anchorText,
  anchorDesc,
  sharedClauses,
  stepProseMarkers,
  # Two nouns, not one: `proseKind` is what the baked arm must not restate,
  # `carriedKind` what the skill carries in-box instead. They differ at
  # code-review, whose dimension prose #3226 moved out of the skill
  # (code-review-fragment-parity.nix:14-16), so collapsing them back into one
  # param reverts that site's assert message.
  proseKind,
  carriedKind,
  discipline,
}:
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
      throw "${sourceFile}: no dogfood skill named \"${name}\" (nix/dogfood-skills.nix row may have been renamed or dropped)"
    else
      builtins.head matches;

  # Every fallback hard-wraps at a different width than its skill and capitalizes
  # shared phrases differently, so a raw substring match would fail over line
  # breaks and case. Each call site's clauses are chosen not to straddle markdown
  # emphasis, so the skills' `**...**` wrappers need no stripping here.
  normalize =
    text:
    let
      words = builtins.filter (w: builtins.isString w && w != "") (builtins.split "[[:space:]]+" text);
    in
    toLower (concatStringsSep " " words);

  skillText = normalize (skillRowByName skillName).src;
  normalizedFallbackText = normalize fallbackText;

  remedy = "either re-sync the fallback with the skill, or -- if the skill's discipline genuinely changed -- update this check's clause list to match.";

  clauseCheck = c: {
    name = "${skillName}-fragment-parity-clause-${c.name}";
    value =
      let
        needle = normalize c.clause;
      in
      assert assertMsg (hasInfix needle skillText)
        "${skillName} fallback drift: ${skillDesc} no longer states \"${c.clause}\", which ${fallbackDesc} restates -- ${remedy}";
      assert assertMsg (hasInfix needle normalizedFallbackText)
        "${skillName} fallback drift: ${fallbackDesc} no longer states \"${c.clause}\", which ${skillDesc} teaches -- ${remedy}";
      pkgs.runCommand "${skillName}-fragment-parity-clause-${c.name}" { } "touch $out";
  };

  normalizedAnchor = normalize anchorText;
  leakedMarkers = builtins.filter (m: hasInfix m normalizedAnchor) stepProseMarkers;
  anchorLines = builtins.filter (l: builtins.isString l && normalize l != "") (
    builtins.split "\n" anchorText
  );
in
{
  checks = builtins.listToAttrs (map clauseCheck sharedClauses) // {
    # Baking a skill subtracts prose: the baked arm names the skill and stops. An
    # edit that grew it back into a paragraph would restore the duplication the pair
    # exists to remove, and every clause check above would still pass.
    "${skillName}-fragment-parity-baked-anchor-omits-step-prose" =
      assert assertMsg (leakedMarkers == [ ])
        "${anchorDesc} restates the unbaked arm's ${proseKind} (${concatStringsSep ", " leakedMarkers}) -- the baked arm must name the `/${skillName}` skill and stop, since the skill itself carries that ${carriedKind} in-box; move any wording worth keeping into ${fallbackDesc}.";
      assert assertMsg (builtins.length anchorLines == 1)
        "${anchorDesc} is ${toString (builtins.length anchorLines)} non-empty lines, want 1 -- the baked arm is an anchor line, not a paragraph; prose belongs in ${fallbackDesc}.";
      assert assertMsg (hasInfix "/${skillName}" anchorText)
        "${anchorDesc} no longer names the `/${skillName}` skill -- the baked arm's entire job is to point at the baked skill, so with the name gone the prompt says nothing about ${discipline} at all.";
      pkgs.runCommand "${skillName}-fragment-parity-baked-anchor-omits-step-prose" { } "touch $out";
  };
  inherit anchorLines;
}
