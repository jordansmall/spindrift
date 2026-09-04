# Drift parity between the commit fallback fragment and the upstream `/commit`
# skill (issue #3222, following the tdd pattern from #3219). See
# nix/checks/tdd-fragment-parity.nix for the full rationale comment; this file
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

  # Kept to two clauses, both load-bearing: losing either from the skill would
  # mean the fallback is teaching a format the skill no longer specifies.
  # "hard line wraps"/"hard-wrapped" and "50/72" column counts were considered
  # and dropped -- the skill spells the wrap rule as "hard line wraps" (header)
  # and separately "hard-wrap at 72 columns" (bullet), and the column bounds
  # use "≤ 50" (spaced) against the fallback's "≤50" (unspaced), so neither
  # phrase is genuinely shared vocabulary once normalized; a pin on either
  # would go red on a copy-edit that changed nothing about the discipline.
  sharedClauses = [
    {
      name = "conventional-commits-v1-0-0";
      # The format and version this skill exists to enforce. The skill states
      # it in its opening sentence; the fallback restates it verbatim as the
      # first thing it says. Losing this from either side means the fallback
      # is no longer pinned to the same spec version the skill teaches.
      clause = "conventional commits v1.0.0";
    }
    {
      name = "hard-wrap";
      # The skill's own hyphenated form ("hard-wrap at 72 columns", distinct
      # from the header's unhyphenated "hard line wraps") is also the
      # fallback's word for the same rule ("hard-wrapped"). Without it the
      # fallback would be silent on whether wrapping is enforced at all.
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
