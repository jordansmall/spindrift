# The Connector scaffolds (templates/connector-tracker, templates/connector-driver)
# must keep the layout of a first-party Connector of their kind (connectors/jira,
# connectors/opencode): the same skeleton file set and, per file, the same
# shape, so `nix run .#new-connector` hands out what a first-party Connector
# looks like. A template is a valid Connector named `connectorname`; mapping
# that token to the reference's name lines the two trees up. Compared at
# evaluation, with attr names only (no IFD, nothing built), so the reference
# gaining a skeleton file or a module attr fails here until the scaffold
# follows. Also refuses the word "example" anywhere in a template: a generated
# Connector carries no residue.
{
  pkgs,
  nixpkgs,
  system,
  ...
}:
let
  inherit (pkgs.lib)
    any
    assertMsg
    attrNames
    attrValues
    concatMap
    concatStringsSep
    filter
    hasInfix
    hasPrefix
    hasSuffix
    head
    match
    removePrefix
    sort
    subtractLists
    toLower
    unique
    ;
  inherit (pkgs.lib.filesystem) listFilesRecursive;

  token = "connectorname";

  core = import ../../core // {
    inputs.nixpkgs = nixpkgs;
  };
  registry = import ../../lib/drivers/default.nix { inherit (pkgs) lib; };

  relFiles = root: map (f: removePrefix "${toString root}/" (toString f)) (listFilesRecursive root);

  # The reference's skeleton: what the scaffold mirrors, not its own tests or goldens.
  skeletonOf = filter (r: !(hasPrefix "testdata/" r) && (!(hasSuffix ".go" r) || r == "main.go"));

  # Every way the template's file set (token mapped to the reference) disagrees with the reference's skeleton.
  fileSetProblems =
    ref: tmpl: refFiles:
    map (r: "${r}: in ${ref}'s skeleton but not the template") (subtractLists tmpl refFiles)
    ++ map (r: "${r}: in the template but not ${ref}'s skeleton") (subtractLists refFiles tmpl);

  sorted = xs: sort (a: b: a < b) xs;

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
  expect = ok: msg: if ok then [ ] else [ msg ];

  hasCorpusCase = any (r: match "testdata/corpus/[^/]+/pass\\.jsonl" r != null);

  # What a template of one kind must keep from its reference `ref`. `tmplFiles`
  # narrows the template's file set before the compare (a driver template
  # carries a starter corpus the reference's own testdata outgrows); `extra`
  # adds the kind's own pins, given the trees and `outputs`.
  scaffold =
    {
      tmplDir,
      ref,
      tmplFiles ? (fs: fs),
      sdkPackage,
      extra,
    }:
    let
      refDir = ../../connectors + "/${ref}";
      toRef = builtins.replaceStrings [ token ] [ ref ];
      forBoth = f: {
        tmpl = f tmplDir;
        ref = f refDir;
      };
      # The reference's and the template's `f` must take the same arguments.
      sameArgs = what: f: sameNames what (argNames (f refDir)) (argNames (f tmplDir));

      fileSet = fileSetProblems ref (sorted (map toRef (tmplFiles (relFiles tmplDir)))) (
        sorted (skeletonOf (relFiles refDir))
      );

      flakes = forBoth (d: load d "flake.nix");
      entries = forBoth (d: ((load d "default.nix").lib.connector { inherit core; }));
      outputs = forBoth (d: (load d "flake.nix").outputs { inherit core; });

      structure =
        sameNames "flake.nix inputs" (attrNames flakes.ref.inputs) (attrNames flakes.tmpl.inputs)
        ++ expect (
          flakes.tmpl.inputs.core.url == flakes.ref.inputs.core.url
        ) "flake.nix: inputs.core.url differs from ${ref}'s"
        ++ sameNames "flake.nix outputs args" (argNames flakes.ref.outputs) (argNames flakes.tmpl.outputs)
        ++ sameNames "flake.nix output attrs" (attrNames outputs.ref) (attrNames outputs.tmpl)
        ++ sameNames "flake.nix checks" (attrNames outputs.ref.checks.${system}) (
          sorted (map toRef (attrNames outputs.tmpl.checks.${system}))
        )
        ++ sameNames "default.nix connector attrs" (attrNames entries.ref) (attrNames entries.tmpl)
        ++ sameArgs "default.nix lib.connector args" (d: (load d "default.nix").lib.connector)
        ++ sameArgs "package.nix args" (d: load d "package.nix")
        ++ sameArgs "module.nix args" (d: load d "module.nix")
        ++ (
          let
            lines = d: filter (l: l != "") (pkgs.lib.splitString "\n" (builtins.readFile (d + "/go.mod")));
            # A replace's target differs by design: the reference's is in-repo, the
            # template's names the SDK on GitHub.
            directive = l: if hasPrefix "replace " l then head (pkgs.lib.splitString "=>" l) else l;
          in
          expect (
            map (l: directive (toRef l)) (lines tmplDir) == map directive (lines refDir)
          ) "go.mod: directives differ from ${ref}'s"
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
                    dir = refDir;
                    who = ref;
                  }
                ]
            )
            [
              "package main\n"
              "\"spindrift.dev/sdk/${sdkPackage}\""
              "${sdkPackage}.Register("
              "\nfunc main() {}\n"
            ]
        ++ (
          let
            heading = d: head (pkgs.lib.splitString "\n" (builtins.readFile (d + "/CHANGELOG.md")));
          in
          expect (heading tmplDir == heading refDir) "CHANGELOG.md: first line differs from ${ref}'s"
        )
        ++ extra {
          inherit
            ref
            tmplDir
            refDir
            toRef
            ;
        };
    in
    {
      inherit fileSet structure;
      files = relFiles tmplDir;
      residue = filter (
        r:
        hasInfix "example" (toLower r)
        || hasInfix "example" (toLower (builtins.readFile (tmplDir + "/${r}")))
      ) (relFiles tmplDir);
    };

  trackerExtra =
    {
      ref,
      tmplDir,
      refDir,
      toRef,
    }:
    let
      rowOf = dir: name: ((load dir "module.nix") { inherit core; } { }).spindrift.connectors.${name};
      rows = {
        tmpl = rowOf tmplDir token;
        ref = rowOf refDir ref;
      };
      allKnobFields = r: unique (concatMap attrNames (attrValues r.knobs));
      routeAttrs = d: unique (concatMap attrNames (load d "routes.nix"));
    in
    sameNames "module.nix row attrs" (attrNames rows.ref) (attrNames rows.tmpl)
    # The template's backend and knobs may use fewer of jira's options (token
    # auth, not Basic), never one jira does not.
    ++ namesProblems "module.nix backend" [
      "module"
      "routes"
      "credential"
      "base"
    ] (attrNames rows.ref.backend) (attrNames rows.tmpl.backend)
    ++ namesProblems "module.nix knob fields" [ "env" "group" "doc" ] (allKnobFields rows.ref) (
      allKnobFields rows.tmpl
    )
    ++ sameNames "fragments.nix attrs" (attrNames (load refDir "fragments.nix")) (
      attrNames (load tmplDir "fragments.nix")
    )
    ++ (
      let
        shape = d: builtins.toJSON { inherit ((load d "fragments.nix")) name shipped; };
      in
      expect (toRef (shape tmplDir) == shape refDir) "fragments.nix: name or shipped differ from ${ref}'s"
    )
    ++ namesProblems "routes.nix route attrs" [ "method" "host" "path" ] (routeAttrs refDir) (
      routeAttrs tmplDir
    );

  # opencode's row is core's own entry plus `module`; the template's is the
  # whole row, so it is held to the registry's own validators and its closed
  # attribute set (the registry's knownAttrs), not to opencode's.
  # `firstParty` is in no row: mkHarness derives that mark from the batteries
  # wasm, so a row naming it falls outside the set.
  driverExtra =
    { tmplDir, ... }:
    let
      row = (load tmplDir "row.nix") {
        inherit core;
        inherit (pkgs) lib;
      };
      registered =
        ((load tmplDir "module.nix") { inherit core; } { inherit (pkgs) lib; })
        .spindrift.connectors.${token};
      valid = builtins.tryEval (builtins.deepSeq (registry.assertRow token row) true);
    in
    namesProblems "row.nix row attrs" (registry.requiredAttrs ++ [ "module" ]) registry.knownAttrs (
      attrNames row
    )
    ++ expect valid.success "row.nix: refused by the Driver registry's validators (assertRow in lib/drivers/default.nix)"
    ++ expect (
      registered ? driver && attrNames registered.driver == attrNames row
    ) "module.nix: spindrift.connectors.${token}.driver is not row.nix's row";

  trackerScaffold = scaffold {
    tmplDir = ../../templates/connector-tracker;
    ref = "jira";
    sdkPackage = "tracker";
    extra = trackerExtra;
  };
  driverScaffold = scaffold {
    tmplDir = ../../templates/connector-driver;
    ref = "opencode";
    # opencode's own testdata outgrows the template's starter corpus.
    tmplFiles = filter (r: !(hasPrefix "testdata/" r));
    sdkPackage = "driver";
    extra = driverExtra;
  };

  controls = {
    refGainsAFile = fileSetProblems "r" [ "a.nix" ] [ "a.nix" "foo.nix" ] != [ ];
    templateGainsAFile = fileSetProblems "r" [ "a.nix" "foo.nix" ] [ "a.nix" ] != [ ];
    sameFiles = fileSetProblems "r" [ "a.nix" ] [ "a.nix" ] == [ ];
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
    corpusCaseFound = hasCorpusCase [ "testdata/corpus/basic/pass.jsonl" ];
    corpusCaseNeedsPass = !(hasCorpusCase [ "testdata/corpus/basic/result.golden.txt" ]);
    corpusCaseNeedsACaseDir = !(hasCorpusCase [ "testdata/pass.jsonl" ]);
  };
  failedControls = filter (n: !controls.${n}) (attrNames controls);

  check =
    name: dir: s:
    assert assertMsg (
      failedControls == [ ]
    ) "${name} self-test failed: ${concatStringsSep ", " failedControls}";
    assert assertMsg (s.files != [ ]) "${name} found no template files; the scan is vacuous";
    assert assertMsg (
      s.fileSet == [ ]
    ) "${dir}'s file set drifted from its reference's skeleton:\n${concatStringsSep "\n" s.fileSet}";
    assert assertMsg (
      s.structure == [ ]
    ) "${dir}'s structure drifted from its reference's:\n${concatStringsSep "\n" s.structure}";
    assert assertMsg (s.residue == [ ])
      "${dir} carries the word \"example\" (a generated Connector must have no residue): ${concatStringsSep ", " s.residue}";
    pkgs.runCommand name { } "touch $out";
in
{
  connector-scaffold = check "connector-scaffold" "templates/connector-tracker" trackerScaffold;

  connector-scaffold-driver =
    assert assertMsg (hasCorpusCase driverScaffold.files)
      "templates/connector-driver carries no starter corpus (testdata/corpus/<case>/pass.jsonl)";
    check "connector-scaffold-driver" "templates/connector-driver" driverScaffold;
}
