# Drift parity between the commit fallback fragment and the upstream `/commit`
# skill (issue #3222, following the tdd pattern from #3219).
# nix/checks/tdd-fragment-parity.nix carries the full rationale; this file
# repeats only what differs.
{ pkgs, fixtures, ... }:
let
  inherit (pkgs.lib)
    assertMsg
    concatStringsSep
    hasInfix
    toLower
    ;

  skillRowByName =
    name:
    let
      matches = builtins.filter (r: r.name == name) fixtures.dogfoodSkills;
    in
    if matches == [ ] then
      throw "nix/checks/commit-fragment-parity.nix: no dogfood skill named \"${name}\" (nix/dogfood-skills.nix row may have been renamed or dropped)"
    else
      builtins.head matches;

  normalize =
    text:
    let
      words = builtins.filter (w: builtins.isString w && w != "") (builtins.split "[[:space:]]+" text);
    in
    toLower (concatStringsSep " " words);

  skillText = normalize (skillRowByName "commit").src;
  fallbackText = normalize (
    builtins.readFile ../../templates/default/prompts/fragments/commit-unbaked.md
  );
  anchorText = builtins.readFile ../../templates/default/prompts/fragments/commit-baked.md;

  skillDesc = "the upstream commit SKILL.md (pinned `jordan-skills` flake input, read via nix/dogfood-skills.nix)";
  fallbackDesc = "templates/default/prompts/fragments/commit-unbaked.md";
  remedy = "either re-sync the fallback with the skill, or -- if the skill's discipline genuinely changed -- update this check's clause list to match.";

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

  clauseCheck = c: {
    name = "commit-fragment-parity-clause-${c.name}";
    value =
      let
        needle = normalize c.clause;
      in
      assert assertMsg (hasInfix needle skillText)
        "commit fallback drift: ${skillDesc} no longer states \"${c.clause}\", which ${fallbackDesc} restates -- ${remedy}";
      assert assertMsg (hasInfix needle fallbackText)
        "commit fallback drift: ${fallbackDesc} no longer states \"${c.clause}\", which ${skillDesc} teaches -- ${remedy}";
      pkgs.runCommand "commit-fragment-parity-clause-${c.name}" { } "touch $out";
  };

  # Phrases that belong only to the unbaked arm's format-rule prose.
  stepProseMarkers = [
    "conventional commits v1.0.0"
    "hard-wrap"
    "subject"
    "self-evident"
  ];
  normalizedAnchor = normalize anchorText;
  leakedMarkers = builtins.filter (m: hasInfix m normalizedAnchor) stepProseMarkers;
  anchorLines = builtins.filter (l: builtins.isString l && normalize l != "") (
    builtins.split "\n" anchorText
  );
in
builtins.listToAttrs (map clauseCheck sharedClauses)
// {
  commit-fragment-parity-baked-anchor-omits-step-prose =
    assert assertMsg (leakedMarkers == [ ])
      "templates/default/prompts/fragments/commit-baked.md restates the unbaked arm's format-rule prose (${concatStringsSep ", " leakedMarkers}) -- the baked arm must name the `/commit` skill and stop, since the skill itself carries that prose in-box; move any wording worth keeping into ${fallbackDesc}.";
    assert assertMsg (builtins.length anchorLines == 1)
      "templates/default/prompts/fragments/commit-baked.md is ${toString (builtins.length anchorLines)} non-empty lines, want 1 -- the baked arm is an anchor line, not a paragraph; prose belongs in ${fallbackDesc}.";
    assert assertMsg (hasInfix "/commit" anchorText)
      "templates/default/prompts/fragments/commit-baked.md no longer names the `/commit` skill -- the baked arm's entire job is to point at the baked skill, so with the name gone the prompt says nothing about commit-message discipline at all.";
    pkgs.runCommand "commit-fragment-parity-baked-anchor-omits-step-prose" { } "touch $out";
}
