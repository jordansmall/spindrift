# Drift parity between the in-repo `/nix-checks` skill and the "## Nix edits"
# section of CLAUDE.md (issue #3448). See nix/checks/tdd-fragment-parity.nix
# for the shared two-surface parity rationale; this file repeats only what
# differs.
#
# Both texts are in-repo (unlike tdd-fragment-parity.nix's pinned upstream
# skill), so both are read straight off disk with builtins.readFile -- no
# fixtures indirection needed. Like tdd-fragment-parity.nix, this deliberately
# skips a wholesale verbatim comparison: CLAUDE.md's "## Nix edits" section is
# longer and cites things (issue #3448, lib/image.nix's nixConfigFile) the
# skill never mentions, and the skill is hard-wrapped narrower -- so what is
# asserted instead is the load-bearing shared vocabulary: phrases that would
# have to survive any rewording that left the discipline itself intact.
{ pkgs, ... }:
let
  inherit (pkgs.lib)
    assertMsg
    concatStringsSep
    drop
    hasInfix
    hasPrefix
    replaceStrings
    toLower
    ;
  inherit (pkgs.lib.lists) findFirstIndex;

  # Finds the index (relative to the original list) of the first element from
  # `start` satisfying pred. `lib.lists.findFirstIndex` already does the
  # search; it only needs the list pre-sliced with `drop start` and the
  # offset re-added, since indices it returns are relative to that slice.
  # Used below to slice CLAUDE.md's "## Nix edits" section out of the whole
  # file without IFD or a runCommand-with-awk -- pure Nix string handling
  # only.
  findIndexFrom =
    start: pred: list:
    let
      idx = findFirstIndex pred null (drop start list);
    in
    if idx == null then null else start + idx;

  # Unlike tdd-fragment-parity.nix's normalize, this one also strips
  # backticks and `*` characters: the skill and CLAUDE.md mark up the same
  # phrases with different emphasis (`**scoped**` in CLAUDE.md vs plain
  # `scoped` in the skill), and the tdd check's trick of picking clauses that
  # dodge emphasis entirely doesn't scale here -- several of the shared
  # clauses below are exactly the emphasized ones.
  normalize =
    text:
    let
      stripped = replaceStrings [ "`" "*" ] [ "" "" ] text;
      words = builtins.filter (w: builtins.isString w && w != "") (
        builtins.split "[[:space:]]+" stripped
      );
    in
    toLower (concatStringsSep " " words);

  skillText = normalize (builtins.readFile ../../skills/nix-checks/SKILL.md);

  claudeText = builtins.readFile ../../CLAUDE.md;
  claudeLines = builtins.filter builtins.isString (builtins.split "\n" claudeText);

  nixEditsHeadingIdx =
    let
      idx = findIndexFrom 0 (l: l == "## Nix edits") claudeLines;
    in
    if idx == null then
      throw "nix/checks/nix-checks-lore-parity.nix: CLAUDE.md heading \"## Nix edits\" not found -- has the section been renamed or removed?"
    else
      idx;

  # Not found means "## Nix edits" is the last section -- take the rest of
  # the file rather than throwing, since only the opening heading missing is
  # the sign of a restructure this check needs to fail loudly over.
  nextHeadingIdx =
    let
      idx = findIndexFrom (nixEditsHeadingIdx + 1) (l: hasPrefix "## " l) claudeLines;
    in
    if idx == null then builtins.length claudeLines else idx;

  nixEditsLines = builtins.genList (i: builtins.elemAt claudeLines (nixEditsHeadingIdx + i)) (
    nextHeadingIdx - nixEditsHeadingIdx
  );

  nixEditsText = normalize (concatStringsSep "\n" nixEditsLines);

  skillDesc = "skills/nix-checks/SKILL.md";
  claudeDesc = "CLAUDE.md's \"## Nix edits\" section";
  remedy = "mirror the missing clause into whichever surface lost it.";

  # Each row is one phrase both texts must contain. Kept small on purpose: a
  # clause earns its place only if losing it from either side would mean the
  # skill and CLAUDE.md are teaching different disciplines, not merely
  # phrasing one differently. The last two predate issue #3448 -- included so
  # this check guards the whole mirror, not only this issue's additions.
  sharedClauses = [
    {
      name = "print-build-logs-flag";
      # The `-L` flag both texts tell an agent to pass, spelled out by its
      # long name so a future edit that drops the flag but keeps `-L` in a
      # code block alone still trips this.
      clause = "--print-build-logs";
    }
    {
      name = "deterministic-failure";
      # The reason retrying a failed check unchanged is wasted: the failure
      # is a property of the derivation hash, not the scheduler.
      clause = "a check failure is deterministic";
    }
    {
      name = "never-rerun-unchanged";
      # The rule that follows from determinism -- the one both texts exist to
      # stop an agent from doing.
      clause = "never re-run a failed check unchanged";
    }
    {
      name = "max-jobs-flag";
      # Paired with `-L`: the flag an agent must NOT hand-tune, since the
      # Box's nix.conf already bounds parallelism.
      clause = "--max-jobs";
    }
    {
      name = "not-tracked-by-git";
      # Pre-existing lore: the exact fragment of Nix's own error message when
      # a new file isn't `git add`-ed yet, so an agent can recognize it.
      clause = "is not tracked by git";
    }
    {
      name = "overrides-acceptance-criteria";
      # Pre-existing lore: the scoped-target rule's precedence over whatever
      # an issue's acceptance criteria ask for.
      clause = "overrides any acceptance criteria";
    }
  ];

  clauseCheck = c: {
    name = "nix-checks-lore-parity-clause-${c.name}";
    value =
      let
        needle = normalize c.clause;
      in
      assert assertMsg (hasInfix needle skillText)
        "nix-checks lore drift: ${skillDesc} no longer states \"${c.clause}\", which ${claudeDesc} restates -- ${remedy}";
      assert assertMsg (hasInfix needle nixEditsText)
        "nix-checks lore drift: ${claudeDesc} no longer states \"${c.clause}\", which ${skillDesc} teaches -- ${remedy}";
      pkgs.runCommand "nix-checks-lore-parity-clause-${c.name}" { } "touch $out";
  };

  # The Box's baked cores bound is single-sourced in lib/image.nix's
  # nixConfigFile (both the OCI image and the bwrap runner consume that same
  # file); both lore surfaces quote the number in prose, so a future bump to
  # the baked bound can silently leave one or both surfaces citing a stale N.
  # Scans every line of lib/image.nix for a whole-line "cores = N" match;
  # requiring exactly one keeps the scan honest about which line
  # nixConfigFile actually bakes -- a second such line anywhere in the file
  # would otherwise win or lose by position alone.
  imageNixText = builtins.readFile ../../lib/image.nix;
  imageNixLines = builtins.filter builtins.isString (builtins.split "\n" imageNixText);
  coresLineMatches = builtins.filter (
    l: builtins.match "[[:space:]]*cores = [0-9]+[[:space:]]*" l != null
  ) imageNixLines;
  coresValue =
    if coresLineMatches == [ ] then
      throw "nix/checks/nix-checks-lore-parity.nix: no \"cores = N\" line found anywhere in lib/image.nix -- has nixConfigFile been renamed or restructured?"
    else if builtins.length coresLineMatches > 1 then
      throw "nix/checks/nix-checks-lore-parity.nix: found ${toString (builtins.length coresLineMatches)} \"cores = N\" lines in lib/image.nix -- this check can no longer tell which one nixConfigFile bakes."
    else
      builtins.head (
        builtins.match "[[:space:]]*cores = ([0-9]+)[[:space:]]*" (builtins.head coresLineMatches)
      );

  coresNeedle = "cores = ${coresValue}";
in
builtins.listToAttrs (map clauseCheck sharedClauses)
// {
  nix-checks-lore-cores-matches-nix-conf =
    assert assertMsg (hasInfix coresNeedle skillText)
      "nix-checks lore drift: ${skillDesc} does not quote \"cores = ${coresValue}\", the value lib/image.nix's nixConfigFile actually bakes -- update the skill's prose to match the baked bound.";
    assert assertMsg (hasInfix coresNeedle nixEditsText)
      "nix-checks lore drift: ${claudeDesc} does not quote \"cores = ${coresValue}\", the value lib/image.nix's nixConfigFile actually bakes -- update CLAUDE.md's prose to match the baked bound.";
    pkgs.runCommand "nix-checks-lore-cores-matches-nix-conf" { } "touch $out";
}
