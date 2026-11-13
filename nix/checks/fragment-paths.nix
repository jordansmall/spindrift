# lib/fragment-paths.nix declares the shipped per-backend fragment files
# independent of disk, so a deleted or renamed one fails the shipped-tree
# checks instead of silently shrinking the expected set (issue #5726).
# Eval-level: every assert runs at evaluation, the derivation only carries the
# result.
{ pkgs, ... }:
let
  inherit (pkgs.lib)
    assertMsg
    filter
    hasInfix
    hasSuffix
    ;

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

  missing = problems (withoutFile "forgejo/fix-ci-read.md");

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
in
{
  fragment-paths =
    assert assertMsg (problems shippedDir == [ ])
      "fragment-paths: the shipped tree disagrees with lib/fragment-paths.nix's `shipped`: ${builtins.toJSON (problems shippedDir)}";
    assert assertMsg
      (
        builtins.length missing == 1 && hasInfix "forgejo/fix-ci-read.md: declared" (builtins.head missing)
      )
      "fragment-paths: a declared per-backend file missing from the tree must yield one problem naming forgejo/fix-ci-read.md, got: ${builtins.toJSON missing}";
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
    pkgs.runCommand "fragment-paths" { } "touch $out";
}
