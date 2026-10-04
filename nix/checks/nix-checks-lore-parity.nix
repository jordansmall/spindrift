# Drift parity between the in-repo `/nix-checks` skill and the "## Nix edits"
# section of CLAUDE.md (issue #3448). See nix/checks/mk-fragment-parity.nix for
# the shared two-surface rationale. There is no verbatim comparison: CLAUDE.md's
# section is longer, cites things the skill never mentions, and wraps narrower,
# so this asserts only the vocabulary any faithful rewording would keep.
{
  pkgs,
  imageNixCores,
  imageNixMaxJobs,
  ...
}:
let
  inherit (pkgs.lib)
    assertMsg
    concatMapStringsSep
    concatStringsSep
    drop
    escapeRegex
    hasPrefix
    replaceStrings
    toLower
    ;
  inherit (pkgs.lib.lists) findFirstIndex;

  # hasInfix would let a numeric needle like "--cores 1" match "--cores 16",
  # so a one-sided bump of just the digit slips past parity silently (issue
  # #3495). This is hasInfix plus "the next character, if any, is not a
  # digit". Only the trailing side is guarded: a digit before the needle
  # ("11--cores 1") still matches, which stays harmless only while every
  # needle starts with a non-digit, as each one does today.
  hasDigitSafeInfix =
    needle: text: builtins.match (".*" + escapeRegex needle + "([^0-9].*)?") text != null;

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
  # ones, so they cannot dodge emphasis the way the fragment-parity call sites
  # do -- those pick clauses that never straddle a `**...**` wrapper, which is
  # why mk-fragment-parity.nix's own `normalize` strips no emphasis at all.
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
  # different disciplines, not just the same one in different words. The
  # "not-tracked-by-git" and "overrides-acceptance-criteria" rows predate issue
  # #3448, so this check guards the whole mirror.
  sharedClauses = [
    {
      name = "print-build-logs-flag";
      # Spelled by its long name so an edit that drops the flag but leaves `-L`
      # in a code block still trips this.
      clause = "--print-build-logs";
    }
    {
      name = "build-failed";
      # The bare line a failed check prints without `-L`, which costs a second
      # turn on `nix log`. Bracketed so unrelated "the build failed" prose
      # cannot stand in for it.
      clause = "[build failed]";
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
      name = "oom-cores-carve-out";
      # Pinned because a surface that loses the carve-out falls back to the
      # blanket ban and leaves a killed scoped check with no remedy at all
      # (issue #3452).
      clause = "--cores 1";
    }
    {
      name = "git-add-new-file";
      # The remedy for the error `not-tracked-by-git` pins (issue #714): that
      # row arms an agent to recognize the failure, this one to avoid it
      # (issue #4265).
      clause = "git add any new file";
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
    {
      name = "list-check-attrs";
      # How an agent finds the single check attr the narrow tier builds
      # (issue #4408).
      clause = "--apply builtins.attrNames";
    }
    {
      name = "full-gate-once-per-pass";
      # Caps the full gate at one run per pass, so iteration stays on the
      # narrow tier.
      clause = "once per pass, just before the pass's final commit";
    }
    {
      name = "rebase-rerun-condition";
      # Without the condition an agent re-runs the full gate after every
      # rebase, or never.
      clause = "only if the rebase hit conflicts or brought in changes to files the branch touches";
    }
    {
      name = "workers-targeted-checks-only";
      # Keeps workers off the full gate, which the coordinator owns (issue #4408).
      clause = "worker subagents run targeted checks only — its coordinator agent owns the full gate";
    }
  ];

  clauseCheck = c: {
    name = "nix-checks-lore-parity-clause-${c.name}";
    value =
      let
        needle = normalize c.clause;
      in
      assert assertMsg (hasDigitSafeInfix needle skillText)
        "nix-checks lore drift: ${skillDesc} no longer states \"${c.clause}\", which ${claudeDesc} restates -- ${remedy}";
      assert assertMsg (hasDigitSafeInfix needle nixEditsText)
        "nix-checks lore drift: ${claudeDesc} no longer states \"${c.clause}\", which ${skillDesc} teaches -- ${remedy}";
      pkgs.runCommand "nix-checks-lore-parity-clause-${c.name}" { } "touch $out";
  };

  # Lore texts quote the baked cores and max-jobs bounds in prose, so a bump
  # can leave one stale.
  coresNeedle = "cores = ${imageNixCores}";
  maxJobsNeedle = "max-jobs = ${imageNixMaxJobs}";

  # Raw markdown, run through normalize first so these cover the same path the
  # live surfaces take.
  digitSafeInfixCases = [
    {
      needle = "--cores 1";
      text = "so `--cores 1`\n is the one";
      matches = true;
    }
    {
      needle = "--cores 1";
      text = "use **`--cores 1`**.";
      matches = true;
    }
    {
      needle = "--cores 1";
      text = "use --cores 1";
      matches = true;
    }
    {
      needle = "--cores 1";
      text = "so `--cores 16` is the one";
      matches = false;
    }
    {
      needle = "--cores 1";
      text = "--cores 10 and --cores 2";
      matches = false;
    }
    {
      needle = "--cores 1";
      text = "--cores 16 and later --cores 1.";
      matches = true;
    }
    {
      needle = "cores = 4";
      text = "pins `cores = 4` (lib/image.nix)";
      matches = true;
    }
    {
      needle = "cores = 4";
      text = "pins `cores = 48`";
      matches = false;
    }
  ];
in
builtins.listToAttrs (map clauseCheck sharedClauses)
// {
  nix-checks-lore-cores-matches-nix-conf =
    assert assertMsg (hasDigitSafeInfix coresNeedle skillText)
      "nix-checks lore drift: ${skillDesc} does not quote \"cores = ${imageNixCores}\", the value lib/image.nix's nixConfigFile actually bakes -- update the skill's prose to match the baked bound.";
    assert assertMsg (hasDigitSafeInfix coresNeedle nixEditsText)
      "nix-checks lore drift: ${claudeDesc} does not quote \"cores = ${imageNixCores}\", the value lib/image.nix's nixConfigFile actually bakes -- update CLAUDE.md's prose to match the baked bound.";
    pkgs.runCommand "nix-checks-lore-cores-matches-nix-conf" { } "touch $out";

  nix-checks-lore-max-jobs-matches-nix-conf =
    assert assertMsg (hasDigitSafeInfix maxJobsNeedle skillText)
      "nix-checks lore drift: ${skillDesc} does not quote \"max-jobs = ${imageNixMaxJobs}\", the value lib/image.nix's nixConfigFile actually bakes -- update the skill's prose to match the baked bound.";
    assert assertMsg (hasDigitSafeInfix maxJobsNeedle nixEditsText)
      "nix-checks lore drift: ${claudeDesc} does not quote \"max-jobs = ${imageNixMaxJobs}\", the value lib/image.nix's nixConfigFile actually bakes -- update CLAUDE.md's prose to match the baked bound.";
    pkgs.runCommand "nix-checks-lore-max-jobs-matches-nix-conf" { } "touch $out";

  # The live texts never exercise the multi-digit case, so pin it here for
  # both call-site shapes, a clause row and coresNeedle. The name stays
  # outside clauseCheck's "-clause-" namespace, so no future sharedClauses
  # row can shadow it.
  nix-checks-lore-digit-safe-infix =
    let
      failed = builtins.filter (
        c: hasDigitSafeInfix c.needle (normalize c.text) != c.matches
      ) digitSafeInfixCases;
    in
    assert assertMsg (failed == [ ])
      "nix-checks-lore-digit-safe-infix: hasDigitSafeInfix gave the wrong answer for ${
        concatMapStringsSep ", " (c: "${builtins.toJSON c.needle} in ${builtins.toJSON c.text}") failed
      }";
    pkgs.runCommand "nix-checks-lore-digit-safe-infix" { } "touch $out";
}
