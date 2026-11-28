# lib/fragment-paths.nix declares the shipped per-backend fragment files
# independent of disk, so a deleted or renamed one fails the shipped-tree
# checks instead of silently shrinking the expected set (issue #5726).
# Eval-level except the two fallback checks at the bottom, which diff the
# built prompt dir.
{
  pkgs,
  nixpkgs,
  system,
  fixtures,
  rootFlakeModule,
  githubConnectorData,
  forgejoConnectorData,
  ...
}:
let
  inherit (pkgs.lib)
    assertMsg
    filter
    hasInfix
    hasSuffix
    mapAttrsToList
    mkForce
    ;
  names = import ../../lib/connector-names.nix;

  fragmentPaths = import ../../lib/fragment-paths.nix;
  shippedDir = ../../templates/default/prompts/fragments;

  problems = fragmentPaths.shippedProblemsIn;

  # A filtered copy at eval time, no derivation: the shipped tree minus one file.
  withoutFile =
    suffix:
    builtins.path {
      name = "fragments-without-${builtins.replaceStrings [ "/" ] [ "-" ] suffix}";
      path = shippedDir;
      filter = p: _: !(hasSuffix "/${suffix}" p);
    };

  missing = problems (withoutFile "github/fix-ci-read.md");

  # The guards on `shipped` itself, pinned over a doctored copy of it. Dropping
  # or adding a declared file also surfaces strays or missing files, so each
  # filters for its own entry.
  inherit (fragmentPaths) shipped;
  withShipped = s: fragmentPaths.problemsWith s shippedDir;
  noEntry = filter (hasInfix "fix-ci-read.md: axis row has no entry") (
    withShipped (builtins.removeAttrs shipped [ "fix-ci-read.md" ])
  );
  notFolder =
    filter (hasInfix "jira/fix-ci-read.md: declared, but jira is not a folder of axis forge")
      (
        withShipped (
          shipped
          // {
            "fix-ci-read.md" = [
              "github"
              "jira"
            ];
          }
        )
      );
  staleKey = filter (hasInfix "gone.md: entry in") (withShipped (shipped // { "gone.md" = [ ]; }));

  # A committed fixture, not a derivation: reading a build output at eval time
  # would be import-from-derivation. Declared files read as missing here, so
  # the stray assertion filters for its own entry.
  strayDir = ../../tests/testdata/fragment-paths-stray;
  stray = filter (hasInfix "github/not-a-declared-row.md: present") (problems strayDir);

  # A Connector's fragment folder (issue #6857): a fixture dir laid out as
  # <folder>/<fragment>. scout-issue-read.md is a tracker row no folder ships,
  # and local is a folder of the tracker axis, so the declaration is valid.
  fixtureDir = ../fixtures/connector-fragments;
  fixtureConnector = {
    name = "acme";
    dir = fixtureDir;
    shipped."scout-issue-read.md" = [ "local" ];
  };
  inherit (fragmentPaths) withConnectors;
  withFixture = withConnectors [ fixtureConnector ];
  scoutRow = builtins.head (
    filter (r: r.fragment == "scout-issue-read.md" && (r.axis or "") != "") (
      import ../../lib/fragments.nix
    )
  );
  connectorProblems = c: fragmentPaths.connectorProblemsWith shipped [ c ];
  withDecl = decl: connectorProblems (fixtureConnector // { shipped = decl; });
  fixtureScoutPath = "local/scout-issue-read.md";

  connectorOk = connectorProblems fixtureConnector;
  connectorWrongAxis =
    filter
      (hasInfix "jira/fix-ci-read.md: declared by the acme Connector, but jira is not a folder of axis forge")
      (withDecl {
        "fix-ci-read.md" = [ "jira" ];
      });
  connectorNoRow =
    filter (hasInfix "nope.md: declared by the acme Connector, but names no axis row")
      (withDecl {
        "nope.md" = [ "local" ];
      });
  connectorMissing =
    filter (hasInfix "forgejo/scout-issue-read.md: declared by the acme Connector but missing under")
      (withDecl {
        "scout-issue-read.md" = [
          "local"
          "forgejo"
        ];
      });
  connectorStray = filter (hasInfix "local/scout-issue-read.md: present under") (withDecl { });
  connectorTopLevel = filter (hasInfix "issue-read.md: present at the top") (
    connectorProblems (fixtureConnector // { dir = ../fixtures/connector-fragments-toplevel; })
  );
  connectorDupCore = filter (hasInfix "github/fix-ci-read.md: declared by both") (withDecl {
    "fix-ci-read.md" = [ "github" ];
  });
  connectorDupPair = filter (hasInfix "local/scout-issue-read.md: declared by both acme and other") (
    fragmentPaths.connectorProblemsWith shipped [
      fixtureConnector
      (fixtureConnector // { name = "other"; })
    ]
  );
  connectorEscape = withDecl {
    "../y.md" = [ "local" ];
    "scout-issue-read.md" = [ "../x" ];
  };
  escapesNamed =
    filter (hasInfix "../y.md") connectorEscape != [ ]
    && filter (hasInfix "../x") connectorEscape != [ ];
  throws = v: !(builtins.tryEval (builtins.deepSeq v v)).success;

  forkDir = ../fixtures/connector-fragments-fork-forgejo;
  jiraForkDir = ../fixtures/connector-fragments-fork-jira;
  harnessWith =
    connectorFragments:
    import ../../lib/mkHarness.nix {
      inherit nixpkgs system connectorFragments;
      defaults = {
        codeForge = "forgejo";
        issueTracker = "forgejo";
      };
      packages = p: [ p.hello ];
    };

  # A flake-level Consumer's fragment contributions, mapped as
  # lib/flakeModule.nix's `connectorFragments` does: flake outputs expose no
  # promptDir, so the harness is rebuilt from the debug config.
  coreModule = ../../lib/flakeModule.nix;
  fragmentsOf =
    modules:
    filter (f: f != null) (
      mapAttrsToList (_: r: r.fragments)
        (fixtures.connectorConsumerVia modules).debug.config.spindrift.connectors
    );
  forgejoFork = {
    name = "forgejo";
    dir = forkDir;
    shipped."fix-ci-read.md" = [ "forgejo" ];
  };
  forkData = forgejoConnectorData // {
    fragments = forgejoFork;
  };
  inherit (fixtures) fails hasAll;

  # A third-party Connector's own folder (issue #7647): named after the
  # Connector, served on each axis its backend row declares.
  thirdPartyDir =
    (import ../fixtures/connector-fragments-rows.nix {
      name = "acme";
      axes = [
        "tracker"
        "forge"
      ];
    }).dir;
  trackerOwn = [
    "filer-dedup.md"
    "pr-body-issue-ref.md"
    "pr-body-no-issue-ref.md"
  ];
  ownShipped =
    frags:
    builtins.listToAttrs (
      map (f: {
        name = f;
        value = [ "acme" ];
      }) frags
    );
  thirdParty = axes: frags: {
    name = "acme";
    dir = thirdPartyDir;
    inherit axes;
    shipped = ownShipped frags;
  };
  tpProblems = c: fragmentPaths.connectorProblemsWith shipped [ c ];
  tpOk = tpProblems (thirdParty [ "tracker" "forge" ] (trackerOwn ++ [ "fix-ci-read.md" ]));
  tpUndeclared =
    filter
      (hasInfix "acme/pr-body-issue-ref.md: declared by the acme Connector, but acme is not a folder of axis tracker")
      (tpProblems (thirdParty [ "forge" ] (trackerOwn ++ [ "fix-ci-read.md" ])));
  tpMissing =
    filter
      (hasInfix "acme/pr-body-no-issue-ref.md: the acme Connector declares axis tracker, but ships no pr-body-no-issue-ref.md")
      (
        tpProblems (
          thirdParty [ "tracker" "forge" ] [ "filer-dedup.md" "pr-body-issue-ref.md" "fix-ci-read.md" ]
        )
      );
  # The pure rule on its own: a row shipping nothing is named once per missing
  # non-shared fragment of the axis.
  tpUnserved = fragmentPaths.unservedProblems {
    name = "acme";
    axes = [ "tracker" ];
    shipped = { };
  };
  tpUnservedNamed = builtins.all (
    f: builtins.any (hasInfix "acme/${f}: the acme Connector declares axis tracker") tpUnserved
  ) trackerOwn;
  # The same two through mkHarness, which joins the row's axes onto the
  # Connector's contribution.
  tpRow = axes: {
    module = p: "${fixtures.connectorWasm}/network.wasm";
    routes = [
      {
        method = "GET";
        host = "api.fixture.test";
        path = "/repos";
      }
    ];
    inherit axes;
    displayName = "Acme";
  };
  tpHarness =
    axes: frags:
    import ../../lib/mkHarness.nix {
      inherit nixpkgs system;
      packages = p: [ p.hello ];
      backendRows.acme = tpRow axes;
      connectorFragments = [
        (removeAttrs (thirdParty [ ] frags) [ "axes" ])
      ];
    };
  allOwn = trackerOwn ++ [ "fix-ci-read.md" ];
  # Forced through the prompt dir, which every Box's fragments go through.
  tpHarnessFails =
    axes: frags:
    throws (tpHarness axes frags).internals.promptDir.drvPath
    || throws (tpHarness axes frags).internals.effectiveConnectorFragments;
  # A third-party row that registers no fragments at all is never a
  # contribution, so mkHarness checks it separately.
  tpUnregisteredFails =
    axes:
    throws
      (import ../../lib/mkHarness.nix {
        inherit nixpkgs system;
        packages = p: [ p.hello ];
        backendRows.acme = tpRow axes;
      }).internals.effectiveConnectorFragments;
in
{
  fragment-paths =
    assert assertMsg (problems shippedDir == [ ])
      "fragment-paths: the shipped tree disagrees with lib/fragment-paths.nix's `shipped`: ${builtins.toJSON (problems shippedDir)}";
    assert assertMsg
      (builtins.length missing == 1 && hasInfix "github/fix-ci-read.md: declared" (builtins.head missing))
      "fragment-paths: a declared per-backend file missing from the tree must yield one problem naming github/fix-ci-read.md, got: ${builtins.toJSON missing}";
    assert assertMsg (builtins.length stray == 1)
      "fragment-paths: an undeclared file in a backend folder must yield one problem naming it, got: ${builtins.toJSON stray}";
    assert assertMsg (builtins.length noEntry == 1)
      "fragment-paths: an axis row with no `shipped` entry must yield one problem naming it, got: ${builtins.toJSON noEntry}";
    assert assertMsg (builtins.length notFolder == 1)
      "fragment-paths: a declared folder the axis does not read must yield one problem naming it, got: ${builtins.toJSON notFolder}";
    assert assertMsg (builtins.length staleKey == 1)
      "fragment-paths: a `shipped` key naming no axis row must yield one problem naming it, got: ${builtins.toJSON staleKey}";
    assert assertMsg (connectorOk == [ ])
      "fragment-paths: a well-formed Connector fragment folder must yield no problems, got: ${builtins.toJSON connectorOk}";
    assert assertMsg (builtins.length connectorWrongAxis == 1)
      "fragment-paths: a Connector folder off the row's axis must yield one problem naming path and Connector, got: ${builtins.toJSON connectorWrongAxis}";
    assert assertMsg (builtins.length connectorNoRow == 1)
      "fragment-paths: a Connector fragment naming no axis row must yield one problem, got: ${builtins.toJSON connectorNoRow}";
    assert assertMsg (builtins.length connectorMissing == 1)
      "fragment-paths: a declared Connector file missing from its dir must yield one problem, got: ${builtins.toJSON connectorMissing}";
    assert assertMsg (builtins.length connectorStray == 1)
      "fragment-paths: an undeclared file under a Connector folder must yield one problem, got: ${builtins.toJSON connectorStray}";
    assert assertMsg (builtins.length connectorTopLevel == 1)
      "fragment-paths: a top-level file in a Connector dir must yield one problem, got: ${builtins.toJSON connectorTopLevel}";
    assert assertMsg (builtins.length connectorDupCore == 1)
      "fragment-paths: a path declared by core and a Connector must yield one problem, got: ${builtins.toJSON connectorDupCore}";
    assert assertMsg (builtins.length connectorDupPair == 1)
      "fragment-paths: a path declared by two Connectors must yield one problem naming both, got: ${builtins.toJSON connectorDupPair}";
    assert assertMsg escapesNamed
      "fragment-paths: a Connector folder or fragment that is not a plain path component must be named, got: ${builtins.toJSON connectorEscape}";
    assert assertMsg (
      builtins.elem fixtureScoutPath (withFixture.pathsOf scoutRow)
      && !builtins.elem fixtureScoutPath (fragmentPaths.pathsOf scoutRow)
    ) "fragment-paths: pathsOf must list a Connector's declared path only when the Connector is passed";
    assert assertMsg (
      builtins.elem fixtureScoutPath (withFixture.pathsIn shippedDir scoutRow)
      && !builtins.elem fixtureScoutPath (fragmentPaths.pathsIn shippedDir scoutRow)
    ) "fragment-paths: pathsIn must list a Connector's declared path when its file is present";
    assert assertMsg
      (
        toString (withFixture.fileOf shippedDir fixtureScoutPath)
        == toString (fixtureDir + "/${fixtureScoutPath}")
        &&
          toString (withFixture.fileOf shippedDir "github/fix-ci-read.md")
          == toString (shippedDir + "/github/fix-ci-read.md")
        &&
          toString (fragmentPaths.fileOf shippedDir fixtureScoutPath)
          == toString (shippedDir + "/${fixtureScoutPath}")
      )
      "fragment-paths: fileOf must resolve a Connector-declared path into the Connector's dir and any other path into fragmentsDir";
    assert assertMsg (throws (
      (withConnectors [
        (fixtureConnector // { shipped."fix-ci-read.md" = [ "jira" ]; })
      ]).pathsOf
        scoutRow
    )) "fragment-paths: pathsOf over a Connector with problems must fail evaluation";
    assert assertMsg (
      !throws (withFixture.pathsOf scoutRow)
    ) "fragment-paths: pathsOf over a well-formed Connector must evaluate";
    assert assertMsg (tpOk == [ ])
      "fragment-paths: a third-party Connector's own folder on its declared axes must yield no problems, got: ${builtins.toJSON tpOk}";
    assert assertMsg (builtins.length tpUndeclared == 1)
      "fragment-paths: a third-party Connector's own folder on an undeclared axis must be refused, got: ${builtins.toJSON tpUndeclared}";
    assert assertMsg (builtins.length tpMissing == 1)
      "fragment-paths: a declared axis's missing non-shared fragment must be refused naming Connector and fragment, got: ${builtins.toJSON tpMissing}";
    assert assertMsg (
      !tpHarnessFails [ "tracker" "forge" ] allOwn
    ) "fragment-paths: mkHarness must accept a third-party Connector's own folder on its declared axes";
    assert assertMsg (tpHarnessFails [ "forge" ] allOwn)
      "fragment-paths: mkHarness must refuse a third-party Connector's fragment on an axis its row does not declare";
    assert assertMsg (tpHarnessFails
      [ "tracker" "forge" ]
      [ "filer-dedup.md" "pr-body-issue-ref.md" "fix-ci-read.md" ]
    ) "fragment-paths: mkHarness must refuse a declared axis's missing required fragment";
    assert assertMsg (builtins.length tpUnserved == 3 && tpUnservedNamed)
      "fragment-paths: unservedProblems must name the Connector and each missing tracker fragment, got: ${builtins.toJSON tpUnserved}";
    assert assertMsg (tpUnregisteredFails [ "tracker" ])
      "fragment-paths: mkHarness must refuse a third-party row declaring an axis while contributing no fragments";
    assert assertMsg (!tpUnregisteredFails [ ])
      "fragment-paths: effectiveConnectorFragments must not refuse a third-party row that declares no axis and contributes no fragments";
    pkgs.runCommand "fragment-paths" { } "touch $out";

  # A harness registering no Connector module must still ship forgejo's
  # fragments (issue #6867): mkHarness falls back to the batteries flake's.
  # jira's ship only where the Consumer registers its module.
  fragment-paths-batteries-fallback =
    let
      harness = harnessWith [ ];
    in
    pkgs.runCommand "fragment-paths-batteries-fallback" { } ''
      diff -r ${../../connectors/forgejo/fragments/forgejo} ${harness.internals.promptDir}/fragments/forgejo
      test ! -e ${harness.internals.promptDir}/fragments/jira/issue-blocked-comment-relay.md
      touch $out
    '';

  # A Consumer's entry replaces the Connector's file at each path it declares.
  # The forgejo halves guard that the batteries fallback never declares the
  # same path twice, under another name or under its own; the jira half guards
  # that a registration under another name evaluates (jira is no fallback).
  fragment-paths-fork-dedupe =
    let
      forgejoFork = harnessWith [
        {
          name = "acme-forgejo";
          dir = forkDir;
          shipped."fix-ci-read.md" = [ "forgejo" ];
        }
      ];
      forgejoSameName = harnessWith [
        {
          name = "forgejo";
          dir = forkDir;
          shipped."fix-ci-read.md" = [ "forgejo" ];
        }
      ];
      jiraFork = harnessWith [
        {
          name = "acme-jira";
          dir = jiraForkDir;
          shipped."issue-blocked-comment-relay.md" = [ "jira" ];
        }
      ];
      forgejoDir = "${forgejoFork.internals.promptDir}/fragments/forgejo";
      sameNameDir = "${forgejoSameName.internals.promptDir}/fragments/forgejo";
    in
    assert assertMsg (
      !throws forgejoFork.internals.promptDir.drvPath
    ) "fragment-paths: a Consumer forking forgejo's fragment under another name must evaluate";
    assert assertMsg (
      !throws forgejoSameName.internals.promptDir.drvPath
    ) "fragment-paths: a Consumer registering forgejo's fragment under the name forgejo must evaluate";
    assert assertMsg (
      !throws jiraFork.internals.promptDir.drvPath
    ) "fragment-paths: a Consumer registering jira's fragment under another name must evaluate";
    pkgs.runCommand "fragment-paths-fork-dedupe" { } ''
      fallbackIntact() {
        cmp ${forkDir}/forgejo/fix-ci-read.md "$1"/fix-ci-read.md
        for f in ${../../connectors/forgejo/fragments/forgejo}/*; do
          b=$(basename "$f")
          [ "$b" = fix-ci-read.md ] || cmp "$f" "$1"/"$b"
        done
      }
      fallbackIntact ${forgejoDir}
      fallbackIntact ${sameNameDir}
      cmp ${jiraForkDir}/jira/issue-blocked-comment-relay.md ${jiraFork.internals.promptDir}/fragments/jira/issue-blocked-comment-relay.md
      touch $out
    '';

  # Issue #7293, route (a): a Consumer of the batteries root module registers
  # forgejo as data, and its fragments ship the Connector's folder unchanged.
  fragment-paths-forgejo-batteries =
    let
      harness = harnessWith (fragmentsOf [ rootFlakeModule ]);
    in
    pkgs.runCommand "fragment-paths-forgejo-batteries" { } ''
      diff -r ${../../connectors/forgejo/fragments/forgejo} ${harness.internals.promptDir}/fragments/forgejo
      touch $out
    '';

  # Issue #7293, route (b): a Consumer of core alone forks forgejo's fragments
  # by registering its data with a `fragments` of its own; the fork's file
  # replaces the Connector's at its path and the rest still ship.
  fragment-paths-forgejo-fork-data =
    let
      contributions = fragmentsOf [
        coreModule
        {
          spindrift.connectorData = [
            githubConnectorData
            forkData
          ];
        }
      ];
      harness = harnessWith contributions;
      dir = "${harness.internals.promptDir}/fragments/forgejo";
    in
    assert assertMsg (
      builtins.length contributions == 1
    ) "fragment-paths: the fork's data must register exactly one fragment contribution";
    pkgs.runCommand "fragment-paths-forgejo-fork-data" { } ''
      cmp ${forkDir}/forgejo/fix-ci-read.md ${dir}/fix-ci-read.md
      for f in ${../../connectors/forgejo/fragments/forgejo}/*; do
        b=$(basename "$f")
        [ "$b" = fix-ci-read.md ] || cmp "$f" ${dir}/"$b"
      done
      touch $out
    '';

  # The module routes #7293 documented are gone: a row field is not a
  # Consumer's to set, and the batteries root already registers forgejo.
  fragment-paths-forgejo-fork-refused =
    let
      rowsOf = modules: (fixtures.connectorConsumerVia modules).debug.config.spindrift.connectors;
      forced = [
        rootFlakeModule
        {
          _file = "fork.nix";
          spindrift.connectors.forgejo.fragments = mkForce forgejoFork;
        }
      ];
      second = [
        rootFlakeModule
        {
          _file = "fork.nix";
          spindrift.connectorData = [ forkData ];
        }
      ];
      forcedMessage = names.rowOutsideDataError "forgejo" "fork.nix" [ "fragments" ];
      secondMessage = names.duplicateError "forgejo" [
        { file = rootFlakeModule._file; }
        { file = "fork.nix"; }
      ];
    in
    assert assertMsg (
      !(fails (rowsOf [ rootFlakeModule ]))
    ) "control: the batteries root alone must evaluate";
    assert assertMsg (fails (rowsOf forced))
      "fragment-paths: mkForce on spindrift.connectors.forgejo.fragments beside the batteries module must fail evaluation";
    assert assertMsg (hasAll forcedMessage [
      "Connector 'forgejo'"
      "fork.nix"
      "'fragments'"
      "spindrift.connectorData"
    ]) "fragment-paths: the row refusal must name the Connector, file and key; got ${forcedMessage}";
    assert assertMsg (fails (
      rowsOf second
    )) "fragment-paths: a second forgejo data entry beside the batteries one must fail evaluation";
    assert assertMsg
      (hasAll secondMessage [
        "Connector 'forgejo'"
        "registered twice"
        "fork.nix"
      ])
      "fragment-paths: the duplicate refusal must name the Connector and both files; got ${secondMessage}";
    pkgs.runCommand "fragment-paths-forgejo-fork-refused" { } "touch $out";
}
