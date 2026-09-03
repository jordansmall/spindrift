# Drift parity between the tdd fallback fragment and the upstream `/tdd` skill
# (issue #3219). templates/default/prompts/fragments/tdd-unbaked.md is a
# hand-written restatement of the discipline the upstream skill teaches,
# rendered only when that skill is NOT baked into the image; the baked arm,
# tdd-baked.md, just names the skill. The two arms are meant to be
# interchangeable renderings of one discipline, but the skill is pinned
# upstream and will be bumped -- and nothing else in the tree notices when a
# `matt-skills` bump moves the skill's discipline out from under the fallback.
#
# These checks live in Nix rather than beside the Go fragment-parity test
# (cmd/launcher/orchestrator/caveman_default_fragment_parity_test.go) for one
# reason: the upstream SKILL.md is not vendored into this repo. It is read
# straight off the pinned `matt-skills` flake input (nix/dogfood-skills.nix),
# which a Go test has no way to reach.
#
# Like that Go test, this deliberately skips a wholesale verbatim comparison.
# The skill is a multi-section reference (what a good test is, seams,
# anti-patterns, rules of the loop); the fallback is nine lines of imperative
# -- different lengths and registers by design, and a full diff would fail on
# every upstream copy-edit while saying nothing about the discipline. What is
# asserted instead is the load-bearing shared vocabulary: the few phrases the
# fallback restates from the skill, each of which would have to survive any
# rewording that left the discipline itself intact.
{ pkgs, fixtures, ... }:
let
  inherit (pkgs.lib)
    assertMsg
    concatStringsSep
    hasInfix
    toLower
    ;

  # By name, never by list index: nix/dogfood-skills.nix is an ordered list and
  # a reorder would otherwise silently point this check at a different skill.
  # Same pattern as nix/checks/baked-skills.nix's rowByName.
  skillRowByName =
    name:
    let
      matches = builtins.filter (r: r.name == name) fixtures.dogfoodSkills;
    in
    if matches == [ ] then
      throw "nix/checks/tdd-fragment-parity.nix: no dogfood skill named \"${name}\" (nix/dogfood-skills.nix row may have been renamed or dropped)"
    else
      builtins.head matches;

  # The fallback is hard-wrapped at a different width than the skill and picks
  # its own capitalization for the loop's phases (`RED:` against the skill's
  # `Red before green.`), so a raw substring match would go red over line
  # breaks and shouting -- drift signals that have nothing to do with the
  # discipline. Clauses below are chosen not to straddle markdown emphasis, so
  # the skill's `**...**` wrappers need no stripping here.
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

  # Each row is one phrase both texts must contain. Kept small on purpose: a
  # clause earns its place only if losing it from either side would mean the
  # fallback and the skill are teaching different disciplines, not merely
  # phrasing one differently.
  sharedClauses = [
    {
      name = "test-first";
      # The discipline's own name. The skill advertises itself for work done
      # "test-first"; the fallback opens with it, and it is also the phrase the
      # baked anchor keeps when the prose is subtracted.
      clause = "test-first";
    }
    {
      name = "one-slice-at-a-time";
      # The skill's vertical-slice rule ("One slice at a time. One seam, one
      # test, one minimal implementation per cycle.") against the fallback's
      # opening line. This is the clause the skill's horizontal-slicing
      # anti-pattern exists to enforce, so a fallback that lost it would be
      # teaching the batching the skill forbids.
      clause = "one slice at a time";
    }
    {
      name = "failing-test";
      # RED's anchor: the skill's "Write the failing test first" against the
      # fallback's "write ONE failing test" / "before a failing test exists".
      # Without it the fallback is generic advice about testing rather than the
      # red-green loop.
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

  # Phrases that belong only to the unbaked arm's step prose. `refactor` and
  # `never batch` are matched against the anchor's normalized text, so casing
  # of the fallback's `REFACTOR` / `Never batch` does not matter.
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
  # The negative that gives the pair its meaning: baking a skill *subtracts*
  # prose. The baked arm names the skill and stops -- a careless future edit
  # that grew it back into a paragraph would restore exactly the duplication
  # the pair exists to remove, and every clause check above would still pass.
  tdd-fragment-parity-baked-anchor-omits-step-prose =
    assert assertMsg (leakedMarkers == [ ])
      "templates/default/prompts/fragments/tdd-baked.md restates the unbaked arm's step prose (${concatStringsSep ", " leakedMarkers}) -- the baked arm must name the `/tdd` skill and stop, since the skill itself carries that prose in-box; move any wording worth keeping into ${fallbackDesc}.";
    assert assertMsg (builtins.length anchorLines == 1)
      "templates/default/prompts/fragments/tdd-baked.md is ${toString (builtins.length anchorLines)} non-empty lines, want 1 -- the baked arm is an anchor line, not a paragraph; prose belongs in ${fallbackDesc}.";
    assert assertMsg (hasInfix "/tdd" anchorText)
      "templates/default/prompts/fragments/tdd-baked.md no longer names the `/tdd` skill -- the baked arm's entire job is to point at the baked skill, so with the name gone the prompt says nothing about test-first discipline at all.";
    pkgs.runCommand "tdd-fragment-parity-baked-anchor-omits-step-prose" { } "touch $out";
}
