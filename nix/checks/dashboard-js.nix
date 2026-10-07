# Runs the drill-in's client-logic tests (ADR 0060 amendment, issue #4727).
# Node's built-in runner only: no package.json, npm, or lockfile, so there is
# nothing to keep in sync and nothing that ships to the page.
{ pkgs, ... }:
let
  # The tests read ../web/static relative to themselves, so keep that layout.
  src = pkgs.lib.fileset.toSource {
    root = ../../dashboard;
    fileset = pkgs.lib.fileset.unions [
      ../../dashboard/jstest
      ../../dashboard/web/static
    ];
  };
in
{
  dashboard-js-test = pkgs.runCommand "dashboard-js-test" { nativeBuildInputs = [ pkgs.nodejs ]; } ''
    node --test ${src}/jstest/*.test.js
    touch $out
  '';
}
