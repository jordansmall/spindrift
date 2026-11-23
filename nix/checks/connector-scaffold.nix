# The tracker Connector scaffold (templates/connector-tracker) must keep
# connectors/jira's layout: the same skeleton file set and, per file, the same
# shape, so `nix run .#new-connector` hands out what a first-party Connector
# looks like. The template is a valid Connector named `connectorname`; mapping
# that token to `jira` lines the two trees up. Compared at evaluation, with
# attr names only (no IFD, nothing built), so jira gaining a skeleton file or a
# module attr fails here until the scaffold follows. Also refuses the word
# "example" anywhere in the template: a generated Connector carries no residue.
{
  pkgs,
  nixpkgs,
  system,
  ...
}:
let
  inherit (pkgs.lib)
    assertMsg
    attrNames
    concatMap
    concatStringsSep
    filter
    hasInfix
    hasPrefix
    hasSuffix
    head
    removePrefix
    sort
    subtractLists
    toLower
    unique
    ;
  inherit (pkgs.lib.filesystem) listFilesRecursive;

  token = "connectorname";
  toJira = builtins.replaceStrings [ token ] [ "jira" ];

  tmplDir = ../../templates/connector-tracker;
  jiraDir = ../../connectors/jira;
  core = import ../../core // {
    inputs.nixpkgs = nixpkgs;
  };

  relFiles = root: map (f: removePrefix "${toString root}/" (toString f)) (listFilesRecursive root);

  # jira's skeleton: what the scaffold mirrors, not jira's own tests or goldens.
  skeletonOf = filter (r: !(hasPrefix "testdata/" r) && (!(hasSuffix ".go" r) || r == "main.go"));

  # Every way the template's file set (token mapped to jira) disagrees with jira's skeleton.
  fileSetProblems =
    tmpl: jira:
    map (r: "${r}: in jira's skeleton but not the template") (subtractLists tmpl jira)
    ++ map (r: "${r}: in the template but not jira's skeleton") (subtractLists jira tmpl);

  sorted = xs: sort (a: b: a < b) xs;
  fileSet = fileSetProblems (sorted (map toJira (relFiles tmplDir))) (
    sorted (skeletonOf (relFiles jiraDir))
  );

  # `got` must hold every name in `required` and none outside `allowed`.
  namesProblems =
    what: required: allowed: got:
    map (n: "${what}: missing ${n}") (subtractLists got required)
    ++ map (n: "${what}: unexpected ${n}") (subtractLists allowed got);
  sameNames =
    what: want: got:
    namesProblems what want want got;

  load = dir: f: import (dir + "/${f}");
  argNames = v: attrNames (builtins.functionArgs v);
  # jira's and the template's `f` must take the same arguments.
  sameArgs = what: f: sameNames what (argNames (f jiraDir)) (argNames (f tmplDir));
  expect = ok: msg: if ok then [ ] else [ msg ];
  forBoth = f: {
    tmpl = f tmplDir;
    jira = f jiraDir;
  };

  rowOf = dir: name: ((load dir "module.nix") { inherit core; } { }).spindrift.connectors.${name};
  rows = {
    tmpl = rowOf tmplDir token;
    jira = rowOf jiraDir "jira";
  };
  allKnobFields = r: unique (concatMap attrNames (builtins.attrValues r.knobs));

  routeAttrs = d: unique (concatMap attrNames (load d "routes.nix"));

  flakes = forBoth (d: (load d "flake.nix"));
  entries = forBoth (d: ((load d "default.nix").lib.connector { inherit core; }));
  outputs = forBoth (d: (load d "flake.nix").outputs { inherit core; });

  structure =
    sameNames "flake.nix inputs" (attrNames flakes.jira.inputs) (attrNames flakes.tmpl.inputs)
    ++ expect (
      flakes.tmpl.inputs.core.url == flakes.jira.inputs.core.url
    ) "flake.nix: inputs.core.url differs from jira's"
    ++ sameNames "flake.nix outputs args" (argNames flakes.jira.outputs) (argNames flakes.tmpl.outputs)
    ++ sameNames "flake.nix output attrs" (attrNames outputs.jira) (attrNames outputs.tmpl)
    ++ sameNames "flake.nix checks" (attrNames outputs.jira.checks.${system}) (
      sorted (map toJira (attrNames outputs.tmpl.checks.${system}))
    )
    ++ sameNames "default.nix connector attrs" (attrNames entries.jira) (attrNames entries.tmpl)
    ++ sameArgs "default.nix lib.connector args" (d: (load d "default.nix").lib.connector)
    ++ sameArgs "package.nix args" (d: load d "package.nix")
    ++ sameArgs "module.nix args" (d: load d "module.nix")
    ++ sameNames "module.nix row attrs" (attrNames rows.jira) (attrNames rows.tmpl)
    # The template's backend and knobs may use fewer of jira's options (token
    # auth, not Basic), never one jira does not.
    ++ namesProblems "module.nix backend" [
      "module"
      "routes"
      "credential"
      "base"
    ] (attrNames rows.jira.backend) (attrNames rows.tmpl.backend)
    ++ namesProblems "module.nix knob fields" [ "env" "group" "doc" ] (allKnobFields rows.jira) (
      allKnobFields rows.tmpl
    )
    ++ sameNames "fragments.nix attrs" (attrNames (load jiraDir "fragments.nix")) (
      attrNames (load tmplDir "fragments.nix")
    )
    ++ (
      let
        shape = d: builtins.toJSON { inherit ((load d "fragments.nix")) name shipped; };
      in
      expect (toJira (shape tmplDir) == shape jiraDir) "fragments.nix: name or shipped differ from jira's"
    )
    ++ namesProblems "routes.nix route attrs" [ "method" "host" "path" ] (routeAttrs jiraDir) (
      routeAttrs tmplDir
    )
    ++ (
      let
        lines = d: filter (l: l != "") (pkgs.lib.splitString "\n" (builtins.readFile (d + "/go.mod")));
        # A replace's target differs by design: jira's is in-repo, the
        # template's names the SDK on GitHub.
        directive = l: if hasPrefix "replace " l then head (pkgs.lib.splitString "=>" l) else l;
      in
      expect (
        map (l: directive (toJira l)) (lines tmplDir) == map directive (lines jiraDir)
      ) "go.mod: directives differ from jira's"
    )
    ++
      concatMap
        (
          s:
          concatMap
            (
              { dir, who }:
              if hasInfix s (builtins.readFile (dir + "/main.go")) then
                [ ]
              else
                [ "main.go (${who}): lacks ${s}" ]
            )
            [
              {
                dir = tmplDir;
                who = "template";
              }
              {
                dir = jiraDir;
                who = "jira";
              }
            ]
        )
        [
          "package main\n"
          "\"spindrift.dev/sdk/tracker\""
          "tracker.Register("
          "\nfunc main() {}\n"
        ]
    ++ (
      let
        heading = d: head (pkgs.lib.splitString "\n" (builtins.readFile (d + "/CHANGELOG.md")));
      in
      expect (heading tmplDir == heading jiraDir) "CHANGELOG.md: first line differs from jira's"
    );

  residue = filter (
    r:
    hasInfix "example" (toLower r)
    || hasInfix "example" (toLower (builtins.readFile (tmplDir + "/${r}")))
  ) (relFiles tmplDir);

  controls = {
    jiraGainsAFile = fileSetProblems [ "a.nix" ] [ "a.nix" "foo.nix" ] != [ ];
    templateGainsAFile = fileSetProblems [ "a.nix" "foo.nix" ] [ "a.nix" ] != [ ];
    sameFiles = fileSetProblems [ "a.nix" ] [ "a.nix" ] == [ ];
    skeletonDropsTestsAndGoldens =
      skeletonOf [
        "main.go"
        "reads.go"
        "testdata/x.md"
        "a.nix"
      ] == [
        "main.go"
        "a.nix"
      ];
    missingAttr = namesProblems "x" [ "a" ] [ "a" "b" ] [ ] != [ ];
    extraAttr = namesProblems "x" [ "a" ] [ "a" ] [ "a" "b" ] != [ ];
    optionalAttrOk = namesProblems "x" [ "a" ] [ "a" "b" ] [ "a" ] == [ ];
  };
  failedControls = filter (n: !controls.${n}) (attrNames controls);
in
{
  connector-scaffold =
    assert assertMsg (
      failedControls == [ ]
    ) "connector-scaffold self-test failed: ${concatStringsSep ", " failedControls}";
    assert assertMsg (
      relFiles tmplDir != [ ]
    ) "connector-scaffold found no template files; the scan is vacuous";
    assert assertMsg (fileSet == [ ])
      "templates/connector-tracker's file set drifted from connectors/jira's skeleton:\n${concatStringsSep "\n" fileSet}";
    assert assertMsg (structure == [ ])
      "templates/connector-tracker's structure drifted from connectors/jira's:\n${concatStringsSep "\n" structure}";
    assert assertMsg (residue == [ ])
      "templates/connector-tracker carries the word \"example\" (a generated Connector must have no residue): ${concatStringsSep ", " residue}";
    pkgs.runCommand "connector-scaffold" { } "touch $out";
}
