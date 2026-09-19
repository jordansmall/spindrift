# Drift parity between the in-repo `/nix-checks` skill and the "## Nix edits"
# section of CLAUDE.md (issue #3448). See nix/checks/tdd-fragment-parity.nix for
# the shared two-surface rationale. There is no verbatim comparison: CLAUDE.md's
# section is longer, cites things the skill never mentions, and wraps narrower,
# so this asserts only the vocabulary any faithful rewording would keep.
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

  # findFirstIndex returns an index relative to the slice it searched, so this
  # re-adds the offset. Used below to slice CLAUDE.md's "## Nix edits" section
  # out of the whole file with pure Nix string handling, no IFD.
  findIndexFrom =
    start: pred: list:
    let
      idx = findFirstIndex pred null (drop start list);
    in
    if idx == null then null else start + idx;

  # This strips backticks and `*` as well, because the two texts mark up the
  # same phrases with different emphasis (`**scoped**` in CLAUDE.md, plain
  # `scoped` in the skill) and several shared clauses below are the emphasized
  # ones, so they cannot dodge emphasis the way tdd-fragment-parity.nix does.
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

  # Not found means "## Nix edits" is the last section, so take the rest of the
  # file rather than throwing. Only a missing opening heading signals the
  # restructure this check has to fail loudly over.
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

  # Each row is one phrase both texts must contain. A clause earns its place
  # only if losing it from either side would mean the skill and CLAUDE.md teach
  # different disciplines, not just the same one in different words. The last
  # two predate issue #3448, so this check guards the whole mirror.
  sharedClauses = [
    {
      name = "print-build-logs-flag";
      # Spelled by its long name so an edit that drops the flag but leaves `-L`
      # in a code block still trips this.
      clause = "--print-build-logs";
    }
    {
      name = "deterministic-failure";
      # Why retrying a failed check unchanged is wasted: the failure follows
      # from the derivation hash, not from how the build was scheduled.
      clause = "a check failure is deterministic";
    }
    {
      name = "never-rerun-unchanged";
      clause = "never re-run a failed check unchanged";
    }
    {
      name = "max-jobs-flag";
      # The flag an agent must not hand-tune, since the Box's nix.conf already
      # bounds parallelism.
      clause = "--max-jobs";
    }
    {
      name = "not-tracked-by-git";
      # A verbatim fragment of Nix's own error message for a file that is not
      # `git add`-ed yet, so an agent can recognize it.
      clause = "is not tracked by git";
    }
    {
      name = "overrides-acceptance-criteria";
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
  # nixConfigFile, and both lore texts quote the number in prose, so a bump
  # there can silently leave one of them citing a stale N. Requiring exactly
  # one whole-line "cores = N" match keeps the scan honest about which line
  # nixConfigFile bakes; a second one would win or lose by position alone.
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
