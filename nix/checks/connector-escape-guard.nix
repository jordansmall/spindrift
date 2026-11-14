# ADR 0073 decision 11: a Connector's Nix must not reach outside its own
# directory. First-party Connectors reach core only through the `core`
# argument, as a third-party Connector must. Their one privilege is the
# first-party mark, which only lets the Box load a Driver Connector in-process;
# the Launcher has no first-party exception (ADR 0073, #7019). Fails when any
# *.nix under connectors/<name>/ (or the fixture Connector) holds a relative
# path literal that resolves above that root.
# Known limits: text-based, so comments and strings are matched too (an
# over-report, the safe direction). Blind to absolute paths (an absolute
# flake `path:/…` input included) and <nixpkgs> lookups, a path after `/` or
# `}` (an interpolation tail or URL), paths built by `+`, symlinks (a linked
# file or Connector directory pointing outside), and anything that is not
# *.nix.
# Not default.nix's commentReadersFindings `refRe`: that matches only `./` and
# repo-root-depth `../` prefixes, so it misses bare `x/../y` and escapes of
# any other depth.
{
  pkgs,
  dotDotPath,
  dotPath,
  ...
}:
let
  inherit (pkgs.lib)
    assertMsg
    concatStringsSep
    concatLists
    concatMap
    elem
    filter
    filterAttrs
    foldl'
    hasSuffix
    init
    isList
    mapAttrs
    mapAttrs'
    mapAttrsToList
    nameValuePair
    optionalAttrs
    pathExists
    removePrefix
    splitString
    ;
  inherit (pkgs.lib.filesystem) listFilesRecursive;

  # Path-like text, a superset of Nix's relative paths: a segment, then one or
  # more `/`-led segments (the last may be empty, so a trailing `../` counts,
  # and so do `a//b` and a trailing `/`, which Nix rejects). The leading char
  # excludes `/` and path chars, so the tail of a URL or `${x}/..` never starts
  # a match.
  pathLiteralRe = "(^|[^A-Za-z0-9._+/-])([A-Za-z0-9._+-]+(/[A-Za-z0-9._+-]*)+)";

  # True when `ref`, resolved from the directory components `dir`, pops past
  # the root.
  escapes =
    dir: ref:
    let
      step =
        acc: c:
        if acc.out then
          acc
        else if c == ".." then
          if acc.stack == [ ] then acc // { out = true; } else acc // { stack = init acc.stack; }
        else
          acc // { stack = acc.stack ++ [ c ]; };
      parts = filter (c: c != "" && c != ".") (splitString "/" ref);
    in
    (foldl' step {
      stack = dir;
      out = false;
    } parts).out;

  # Takes [ { rel; text; } ], rel relative to the Connector root.
  findings = concatMap (
    { rel, text }:
    let
      dir = init (splitString "/" rel);
      refs = map (m: builtins.elemAt m 1) (filter isList (builtins.split pathLiteralRe text));
    in
    map (ref: "${rel}: ${ref}") (filter (escapes dir) refs)
  );

  # Takes { <Connector root, repo-relative> = files; }.
  hitsOf =
    scanned:
    concatLists (mapAttrsToList (label: files: map (h: "${label}/${h}") (findings files)) scanned);

  # Self-tests feed hand-made records, so no on-disk fixture escapes.
  mkFile = rel: text: { inherit rel text; };
  selfTests = {
    escapesFromSubdir = {
      got = findings [ (mkFile "x/default.nix" "import ${dotDotPath 2}core") ];
      want = [ "x/default.nix: ../../core" ];
    };
    escapesFromRoot = {
      got = findings [ (mkFile "a.nix" "x = ${dotPath ".."};") ];
      want = [ "a.nix: ./.." ];
    };
    siblingPasses = {
      got = findings [ (mkFile "a.nix" "import ${dotPath "b.nix"}") ];
      want = [ ];
    };
    selfDirPasses = {
      got = findings [ (mkFile "a.nix" "src = ${dotPath "."};") ];
      want = [ ];
    };
    detourInsideRootPasses = {
      got = findings [ (mkFile "a.nix" "import ${dotPath "sub"}/${dotDotPath 1}x.nix") ];
      want = [ ];
    };
    nestedUpwardInsideRootPasses = {
      got = findings [ (mkFile "sub/a.nix" "import ${dotDotPath 1}b.nix") ];
      want = [ ];
    };
    nestedUpwardPastRootFails = {
      got = findings [ (mkFile "sub/a.nix" "import ${dotDotPath 2}b.nix") ];
      want = [ "sub/a.nix: ../../b.nix" ];
    };
    bareEscapeFails = {
      got = findings [ (mkFile "sub/a.nix" "import x/${dotDotPath 3}y.nix") ];
      want = [ "sub/a.nix: x/../../../y.nix" ];
    };
    bareInsideRootPasses = {
      got = findings [ (mkFile "sub/b.nix" "import x/${dotDotPath 1}y.nix") ];
      want = [ ];
    };
    interpolationTailIsABlindSpot = {
      got = findings [ (mkFile "a.nix" "\${x}/${dotDotPath 2}") ];
      want = [ ];
    };
    hitsNameTheConnector = {
      got = hitsOf {
        "connectors/foo" = [ (mkFile "sub/a.nix" "import ${dotDotPath 2}core") ];
        "connectors/bar" = [ (mkFile "a.nix" "import ${dotPath "b.nix"}") ];
      };
      want = [ "connectors/foo/sub/a.nix: ../../core" ];
    };
    urlTailIsABlindSpot = {
      got = findings [ (mkFile "a.nix" "url = \"https://h/a/${dotDotPath 3}\";") ];
      want = [ ];
    };
  };
  selfTestFailures = filter (n: selfTests.${n}.got != selfTests.${n}.want) (
    builtins.attrNames selfTests
  );

  # Written as `+` so the tree need not hold connectors/ yet (#6751 adds it).
  connectorsDir = ../.. + "/connectors";
  fixtureDir = ../fixtures/connector;

  roots =
    (optionalAttrs (pathExists connectorsDir) (
      mapAttrs' (name: _: nameValuePair "connectors/${name}" (connectorsDir + "/${name}")) (
        filterAttrs (_: type: type == "directory") (builtins.readDir connectorsDir)
      )
    ))
    // {
      "nix/fixtures/connector" = fixtureDir;
    };

  filesOf =
    root:
    map (f: {
      rel = removePrefix "${toString root}/" (toString f);
      text = builtins.readFile f;
    }) (filter (f: hasSuffix ".nix" (toString f)) (listFilesRecursive root));

  scanned = mapAttrs (_: filesOf) roots;
  hits = hitsOf scanned;
in
{
  connector-escape-guard =
    assert assertMsg (selfTestFailures == [ ])
      "connector-escape-guard self-test failed: ${
        concatStringsSep ", " (
          map (
            n: "${n}: got ${builtins.toJSON selfTests.${n}.got}, want ${builtins.toJSON selfTests.${n}.want}"
          ) selfTestFailures
        )
      }";
    assert assertMsg (elem "module.nix" (
      map (f: f.rel) scanned."nix/fixtures/connector"
    )) "connector-escape-guard scanned no fixture module.nix; the scan is vacuous";
    assert assertMsg (hits == [ ])
      "a Connector's Nix reaches outside its own directory (ADR 0073 decision 11):\n${concatStringsSep "\n" hits}";
    pkgs.runCommand "connector-escape-guard" { } "touch $out";
}
