# VERSIONING.md's internal carve-out delegates the consumer-visible half of a
# prompt-template change (removed variables, deleted fragments) to a
# MIGRATING.md entry under "What breaks in an override prompt directory", so
# the two must not drift apart (issue #3237).
{ pkgs, ... }:
{
  versioning-prompt-template-carve-out =
    let
      inherit (pkgs.lib)
        assertMsg
        concatStringsSep
        hasInfix
        splitString
        trim
        ;
      # The one phrase both halves turn on: VERSIONING.md must cite it,
      # MIGRATING.md must carry it as a heading.
      phrase = "What breaks in an override prompt directory";
      heading = "### ${phrase}";
      versioningDoc = builtins.readFile ../../VERSIONING.md;
      # VERSIONING.md is hard-wrapped prose, so the cited heading phrase can
      # straddle a line break; collapse all whitespace (including newlines) to
      # a single space before testing so the check doesn't dictate the wrap.
      # Same shape as the shared `normalize` in nix/checks/mk-fragment-parity.nix
      # (also carried locally by nix-checks-lore-parity.nix), minus its
      # `toLower` — the phrase is a heading here, so case is part of what the
      # check pins.
      normalize =
        text:
        let
          words = builtins.filter (w: builtins.isString w && w != "") (builtins.split "[[:space:]]+" text);
        in
        concatStringsSep " " words;
      versioningNormalized = normalize versioningDoc;
      # MIGRATING.md is ~80KB and grows with every entry, close enough to the
      # 100+KB size where hasInfix's regex backtracks and segfaults (see
      # nix/checks/changelog.nix) that it is checked line by line. Its
      # heading is unwrapped, so no normalize pass is needed on this side.
      migratingLines = splitString "\n" (builtins.readFile ../../MIGRATING.md);
    in
    # Self-test pins normalize's whitespace-collapsing before the real file
    # below relies on it.
    assert assertMsg (hasInfix "line one line two" (
      normalize "line one\nline two"
    )) "normalize must collapse a newline so a phrase split across a line break matches";
    assert assertMsg (
      !hasInfix "totally unrelated phrase" (normalize "line one\nline two")
    ) "normalize must not make an unrelated phrase match";
    assert assertMsg (hasInfix phrase versioningNormalized)
      ''VERSIONING.md's prompt-template carve-out no longer cites MIGRATING.md's "${phrase}" heading'';
    assert assertMsg (builtins.any (
      line: trim line == heading
    ) migratingLines) "the heading VERSIONING.md cites is gone from MIGRATING.md: ${heading}";
    pkgs.runCommand "versioning-prompt-template-carve-out" { } "touch $out";
}
