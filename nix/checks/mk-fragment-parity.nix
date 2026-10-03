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
    escapeRegex
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
  # Skills and fallbacks also space the comparator differently ("≤ 50", "<=50",
  # "subject≤50"), so every spelling folds to "≤" with no space on either side
  # (issue #3486). replaceStrings tries its patterns in list order and never
  # rescans its output, so " ≤ " must come before " ≤" and "≤ ".
  normalize =
    text:
    let
      words = builtins.filter (w: builtins.isString w && w != "") (builtins.split "[[:space:]]+" text);
    in
    builtins.replaceStrings [ " ≤ " " ≤" "≤ " ] [ "≤" "≤" "≤" ] (
      builtins.replaceStrings [ "<=" ] [ "≤" ] (toLower (concatStringsSep " " words))
    );

  skillText = normalize (skillRowByName skillName).src;
  normalizedFallbackText = normalize fallbackText;

  # `hasInfix` would let "72" match inside "720" or "172", so a digit-appending
  # edit would stay green. The boundary applies only at a digit edge of the needle:
  # word needles keep matching inside punctuation.
  isDigit = s: builtins.match "[0-9]" s != null;
  containsClause =
    needle: text:
    let
      first = builtins.substring 0 1 needle;
      last = builtins.substring (builtins.stringLength needle - 1) 1 needle;
      lead = if isDigit first then "(.*[^0-9])?" else ".*";
      trail = if isDigit last then "([^0-9].*)?" else ".*";
    in
    # An empty needle would hand `substring` a start of -1 and throw opaquely.
    assert assertMsg (needle != "") "${skillName} fragment parity: a clause normalizes to empty";
    builtins.match "${lead}${escapeRegex needle}${trail}" text != null;

  remedy = "either re-sync the fallback with the skill, or -- if the skill's discipline genuinely changed -- update this check's clause list to match.";

  # `skillClause`/`fallbackClause` default to `clause`. Per-side wording exists
  # because the two sides sometimes state one bound with no shared literal
  # (skill "wrapped at 72", fallback "body ≤72"); the number must sit inside
  # both needles so a one-sided bound change still goes red.
  clauseCheck = c: {
    name = "${skillName}-fragment-parity-clause-${c.name}";
    value =
      let
        skillClause = c.skillClause or c.clause;
        fallbackClause = c.fallbackClause or c.clause;
        differs = skillClause != fallbackClause;
        asFallback = pkgs.lib.optionalString differs " (as \"${fallbackClause}\")";
        asSkill = pkgs.lib.optionalString differs " (as \"${skillClause}\")";
      in
      assert assertMsg (containsClause (normalize skillClause) skillText)
        "${skillName} fallback drift: ${skillDesc} no longer states \"${skillClause}\", which ${fallbackDesc} restates${asFallback} -- ${remedy}";
      assert assertMsg (containsClause (normalize fallbackClause) normalizedFallbackText)
        "${skillName} fallback drift: ${fallbackDesc} no longer states \"${fallbackClause}\", which ${skillDesc} teaches${asSkill} -- ${remedy}";
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
