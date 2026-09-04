# Drift parity between the code-review fallback fragment and the upstream
# `/code-review` skill (issue #3222, following the tdd pattern from #3219).
# See nix/checks/tdd-fragment-parity.nix for the full rationale comment; this
# file repeats only what differs.
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
      throw "nix/checks/code-review-fragment-parity.nix: no dogfood skill named \"${name}\" (nix/dogfood-skills.nix row may have been renamed or dropped)"
    else
      builtins.head matches;

  normalize =
    text:
    let
      words = builtins.filter (w: builtins.isString w && w != "") (builtins.split "[[:space:]]+" text);
    in
    toLower (concatStringsSep " " words);

  skillText = normalize (skillRowByName "code-review").src;
  # Issue #3226 moved the four hunt dimensions out of the gated fallback
  # fragment and into review-prompt.md as unconditional inline text (they
  # must render whether or not the skill is baked), so the drift target
  # follows the prose to where it now lives.
  fallbackText = normalize (builtins.readFile ../../templates/default/prompts/review-prompt.md);
  anchorText = builtins.readFile ../../templates/default/prompts/fragments/code-review-baked.md;

  skillDesc = "the upstream code-review SKILL.md (pinned `matt-skills` flake input, read via nix/dogfood-skills.nix)";
  fallbackDesc = "templates/default/prompts/review-prompt.md";
  remedy = "either re-sync the fallback with the skill, or -- if the skill's discipline genuinely changed -- update this check's clause list to match.";

  # Kept to two clauses. The skill's Standards/Spec two-axis model and
  # review-prompt.md's four hunt dimensions (SPEC/CORRECTNESS/SECURITY/
  # STANDARDS & SMELLS) are genuinely different shapes -- spindrift's
  # CORRECTNESS and SECURITY dimensions have no counterpart in the skill at
  # all, by design (the skill only aggregates two sub-agent reports;
  # spindrift's single reviewer hunts more ground inline) -- so most of
  # review-prompt.md's prose has no shared vocabulary to pin against. These
  # two survive because they name the same finding class in both texts, not
  # just a shared topic word.
  sharedClauses = [
    {
      name = "scope-creep";
      # The Spec axis's own term for unrequested behaviour: the skill's Spec
      # sub-agent brief and the fallback's SPEC dimension both use this exact
      # phrase for the same finding. Losing it from either side means SPEC
      # would stop flagging unrequested changes as its own named category.
      clause = "scope creep";
    }
    {
      name = "code-smells";
      # The skill's Fowler smell baseline and the fallback's STANDARDS &
      # SMELLS dimension both name the discipline "code smells" verbatim.
      # Losing it from either side would mean the Standards axis stops
      # hunting smells as a distinct thing from documented-standard
      # violations.
      clause = "code smells";
    }
  ];

  clauseCheck = c: {
    name = "code-review-fragment-parity-clause-${c.name}";
    value =
      let
        needle = normalize c.clause;
      in
      assert assertMsg (hasInfix needle skillText)
        "code-review fallback drift: ${skillDesc} no longer states \"${c.clause}\", which ${fallbackDesc} restates -- ${remedy}";
      assert assertMsg (hasInfix needle fallbackText)
        "code-review fallback drift: ${fallbackDesc} no longer states \"${c.clause}\", which ${skillDesc} teaches -- ${remedy}";
      pkgs.runCommand "code-review-fragment-parity-clause-${c.name}" { } "touch $out";
  };

  # Phrases that belong only to review-prompt.md's always-inline dimension
  # prose, never to the gated baked arm.
  stepProseMarkers = [
    "hunt every dimension"
    "scope creep"
    "code smells"
    "standards & smells"
  ];
  normalizedAnchor = normalize anchorText;
  leakedMarkers = builtins.filter (m: hasInfix m normalizedAnchor) stepProseMarkers;
  anchorLines = builtins.filter (l: builtins.isString l && normalize l != "") (
    builtins.split "\n" anchorText
  );
in
builtins.listToAttrs (map clauseCheck sharedClauses)
// {
  code-review-fragment-parity-baked-anchor-omits-step-prose =
    assert assertMsg (leakedMarkers == [ ])
      "templates/default/prompts/fragments/code-review-baked.md restates the unbaked arm's dimension prose (${concatStringsSep ", " leakedMarkers}) -- the baked arm must name the `/code-review` skill and stop, since the skill itself carries that discipline in-box; move any wording worth keeping into ${fallbackDesc}.";
    assert assertMsg (builtins.length anchorLines == 1)
      "templates/default/prompts/fragments/code-review-baked.md is ${toString (builtins.length anchorLines)} non-empty lines, want 1 -- the baked arm is an anchor line, not a paragraph; prose belongs in ${fallbackDesc}.";
    assert assertMsg (hasInfix "/code-review" anchorText)
      "templates/default/prompts/fragments/code-review-baked.md no longer names the `/code-review` skill -- the baked arm's entire job is to point at the baked skill, so with the name gone the prompt says nothing about review discipline at all.";
    pkgs.runCommand "code-review-fragment-parity-baked-anchor-omits-step-prose" { } "touch $out";
}
