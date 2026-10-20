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
    pkgs.runCommand "fragment-paths" { } "touch $out";
}
