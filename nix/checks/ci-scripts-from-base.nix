# Pins ci.yml's `changes` and `ci-ok` jobs to the PR base's copy of the CI
# scripts (issue #5155): the scripts sit outside .github/workflows/, so a PR
# running its own copy could rewrite the required check's verdict. Three
# properties: each job's base-swap loop runs the full swap and names every
# script the job runs, every `.github/scripts/` use goes through `bash` (a
# swapped-in script has no executable bit), and `changes` keeps full history.
# Every rule is mutation tested: each fixture trips exactly the rules it names.
{ pkgs, ... }:
let
  inherit (pkgs.lib)
    any
    assertMsg
    attrNames
    concatLists
    concatMap
    concatStringsSep
    drop
    elem
    elemAt
    filter
    genList
    hasInfix
    hasPrefix
    hasSuffix
    init
    last
    length
    replaceStrings
    splitString
    subtractLists
    take
    trim
    ;
  inherit (pkgs.lib.lists) findFirstIndex;
  ciSrc = builtins.readFile ../../.github/workflows/ci.yml;

  # Scripts each job must pin; a superset in ci.yml passes.
  pinned = {
    changes = [
      "ci-changes.sh"
      "ci-ok.sh"
      "ci-go-tokens.go"
      "ci-comment-readers"
    ];
    ci-ok = [
      "ci-changes.sh"
      "ci-ok.sh"
    ];
  };

  # A job's text: from its top-level key to the next one. A missing job is ""
  # so its rules fail instead of throwing.
  job =
    name: src:
    let
      parts = splitString "\n  ${name}:\n" src;
      lines = splitString "\n" (elemAt parts 1);
      end = findFirstIndex (l: builtins.match "  [^ #].*" l != null) (length lines) lines;
    in
    if length parts < 2 then "" else concatStringsSep "\n" (take end lines);

  swapCmd = ''git show "$BASE_SHA:.github/scripts/$f" >".github/scripts/$f"'';

  # The name lists of the real base-swap loops. A `for f in ...; do` counts
  # only when its body (up to the first `done`) runs the full swapCmd, so a
  # decoy loop, or one that checks the base ref but redirects the output
  # elsewhere, pins nothing.
  swapLoops =
    text:
    let
      lines = splitString "\n" text;
    in
    concatLists (
      genList (
        i:
        let
          m = builtins.match "[[:space:]]*for f in (.*); do" (elemAt lines i);
          rest = drop (i + 1) lines;
          body = take (findFirstIndex (
            l: builtins.match "[[:space:]]*done" l != null
          ) (length rest) rest) rest;
        in
        if m != null && any (hasInfix swapCmd) body then m else [ ]
      ) (length lines)
    );
  loopNames = text: concatMap (splitString " ") (swapLoops text);

  # Every use of the scripts dir must follow `bash `, a `git show`/`cat-file`
  # ref or a redirect into it; a comment line is not a use. Inverting the rule
  # (instead of listing the bad prefixes) catches every spelling of a direct run.
  byPathOffenders =
    src:
    let
      pieces = splitString ".github/scripts/" src;
      before = init pieces;
      allowed =
        p:
        hasSuffix "bash " p
        || hasSuffix "$BASE_SHA:" p
        || hasSuffix ''>"'' p
        || hasPrefix "#" (trim (last (splitString "\n" p)));
    in
    map (p: trim (last (splitString "\n" p)) + ".github/scripts/") (filter (p: !(allowed p)) before);

  rules =
    src:
    let
      changesJob = job "changes" src;
      pinnedRule =
        name:
        let
          missing = subtractLists (loopNames (job name src)) pinned.${name};
        in
        {
          ok = missing == [ ];
          msg = "ci.yml's `${name}` job no longer runs the full base swap for ${concatStringsSep ", " missing} — a PR could rewrite its own required-check verdict or reclassify code as docs.";
        };
      offenders = byPathOffenders src;
    in
    {
      by-path = {
        ok = offenders == [ ];
        msg = "ci.yml runs a CI script by path instead of through `bash` — a script the base swap recreated has no executable bit: ${concatStringsSep "; " offenders}";
      };
      # A shallow fetch of the base marks it a graft with no parents, so once
      # main moves past a PR's fork point `BASE...HEAD` has no merge base.
      full-history = {
        ok = hasInfix "fetch-depth: 0" changesJob && !(hasInfix "--depth" changesJob);
        msg = "ci.yml's `changes` job no longer keeps full history — `git diff BASE...HEAD` fails with 'no merge base' on any PR behind main.";
      };
      pinned-changes = pinnedRule "changes";
      pinned-ci-ok = pinnedRule "ci-ok";
    };
  failedRules =
    src:
    let
      rs = rules src;
    in
    filter (n: !rs.${n}.ok) (attrNames rs);

  # The hasInfix assert keeps a renamed line from leaving its fixture a
  # silent no-op.
  mutate =
    from: to:
    assert assertMsg (hasInfix from ciSrc) "fixture is stale: ci.yml no longer contains `${from}`.";
    replaceStrings [ from ] [ to ] ciSrc;

  inJob =
    jobName: edit:
    let
      text = job jobName ciSrc;
    in
    replaceStrings [ text ] [ (edit text) ] ciSrc;

  okCall = "bash .github/scripts/ci-ok.sh ";
  changesCall = "kind=$(bash .github/scripts/ci-changes.sh ";
  byPath = from: to: {
    name = "run by path: ${to}";
    rules = [ "by-path" ];
    src = mutate from to;
  };

  # Rebuilt from ci.yml's own loop lines, so a superset there never stales them.
  loopLine = cap: "for f in ${cap}; do";
  dropName = name: cap: concatStringsSep " " (filter (n: n != name) (splitString " " cap));
  withoutName =
    jobName: name:
    let
      text = job jobName ciSrc;
      caps = swapLoops text;
    in
    assert assertMsg (elem name (
      loopNames text
    )) "fixture is stale: ci.yml's `${jobName}` swap loop no longer names ${name}.";
    inJob jobName (t: replaceStrings (map loopLine caps) (map (c: loopLine (dropName name c)) caps) t);
  dropFixtures = concatMap (
    j:
    map (n: {
      name = "drop ${n} from ${j}'s loop";
      rules = [ "pinned-${j}" ];
      src = withoutName j n;
    }) pinned.${j}
  ) (attrNames pinned);

  # A leading decoy loop must not stand in for the ci-ok swap loop; its body
  # checks the base ref but never swaps.
  decoy = inJob "ci-ok" (
    t:
    let
      cap = elemAt (swapLoops t) 0;
    in
    replaceStrings
      [ "          ${loopLine cap}" ]
      [
        (concatStringsSep "\n" [
          "          ${loopLine cap}"
          "            git cat-file -e \"$BASE_SHA:.github/scripts/$f\""
          "          done"
          "          ${loopLine (dropName "ci-ok.sh" cap)}"
        ])
      ]
      t
  );
  handFixtures = [
    (byPath okCall "./.github/scripts/ci-ok.sh ")
    (byPath okCall "\"$GITHUB_WORKSPACE\"/.github/scripts/ci-ok.sh ")
    (byPath okCall "\${{ github.workspace }}/.github/scripts/ci-ok.sh ")
    (byPath okCall ".github/scripts/ci-ok.sh ")
    (byPath okCall "exec .github/scripts/ci-ok.sh ")
    (byPath changesCall "kind=$(./.github/scripts/ci-changes.sh ")
    (byPath changesCall "kind=$(\"$GITHUB_WORKSPACE\"/.github/scripts/ci-changes.sh ")
    {
      name = "swap redirected away from the scripts dir";
      rules = [ "pinned-ci-ok" ];
      src = inJob "ci-ok" (
        replaceStrings [ swapCmd ] [ ''git show "$BASE_SHA:.github/scripts/$f" >"/tmp/$f"'' ]
      );
    }
    {
      name = "full history dropped";
      rules = [ "full-history" ];
      src = mutate "fetch-depth: 0" "fetch-depth: 1";
    }
    {
      name = "bare path, two-space indent";
      rules = [ "by-path" ];
      src = mutate "\n          ${okCall}" "\n  .github/scripts/ci-ok.sh ";
    }
    {
      name = "decoy loop before the ci-ok swap loop";
      rules = [ "pinned-ci-ok" ];
      src = decoy;
    }
  ];

  fixtures =
    handFixtures
    ++ dropFixtures
    ++ [
      {
        name = "comment naming a script";
        rules = [ ];
        src = mutate okCall "# see .github/scripts/ci-ok.sh\n          ${okCall}";
      }
    ];

  fixtureOk =
    f:
    let
      actual = failedRules f.src;
    in
    assertMsg (actual == f.rules)
      "ci-scripts-from-base: fixture `${f.name}` expected failed rules [${concatStringsSep ", " f.rules}], got [${concatStringsSep ", " actual}].";
in
{
  ci-scripts-from-base =
    let
      failed = failedRules ciSrc;
      rs = rules ciSrc;
    in
    assert assertMsg (failed == [ ]) (concatStringsSep "\n" (map (n: rs.${n}.msg) failed));
    assert builtins.all fixtureOk fixtures;
    pkgs.runCommand "ci-scripts-from-base" { } "touch $out";
}
