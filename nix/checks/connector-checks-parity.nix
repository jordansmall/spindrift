# Issue #7009: every Connector subflake must expose its conformance suite
# (core.lib.connectorChecks) as `checks.<system>.connector-<name>-conformance`,
# so a Connector added under connectors/ cannot ship without one. Outputs are
# evaluated with the in-tree core and only attr names are forced, never builds.
{
  pkgs,
  nixpkgs,
  system,
  ...
}:
let
  inherit (pkgs.lib) assertMsg concatStringsSep filterAttrs;

  hasConformance =
    name: outputs: ((outputs.checks or { }).${system} or { }) ? "connector-${name}-conformance";

  # `inputs.nixpkgs` is what a subflake reads off `core` that plain `import`
  # of core/default.nix does not carry.
  core = import ../../core // {
    inputs.nixpkgs = nixpkgs;
  };
  connectorsDir = ../../connectors;
  connectorNames = builtins.attrNames (
    filterAttrs (_: t: t == "directory") (builtins.readDir connectorsDir)
  );
  missing = builtins.filter (
    n: !hasConformance n ((import (connectorsDir + "/${n}/flake.nix")).outputs { inherit core; })
  ) connectorNames;

  # Without the accepting case an always-false predicate would pass every
  # rejection vacuously.
  controls = {
    no-checks = {
      outputs = { };
      expect = false;
    };
    no-conformance = {
      outputs.checks.${system}.connector-x-go-test = null;
      expect = false;
    };
    other-system-only = {
      outputs.checks.not-a-system.connector-x-conformance = null;
      expect = false;
    };
    wrong-name = {
      outputs.checks.${system}.connector-y-conformance = null;
      expect = false;
    };
    conformance = {
      outputs.checks.${system}.connector-x-conformance = null;
      expect = true;
    };
  };
  controlFailures = builtins.attrNames (
    filterAttrs (_: c: hasConformance "x" c.outputs != c.expect) controls
  );
in
{
  connector-checks-parity =
    assert assertMsg (
      controlFailures == [ ]
    ) "connector-checks-parity self-test failed: ${concatStringsSep ", " controlFailures}";
    assert assertMsg (
      connectorNames != [ ]
    ) "connector-checks-parity found no Connectors; the scan is vacuous";
    assert assertMsg (missing == [ ])
      "Connector(s) without checks.${system}.connector-<name>-conformance: ${concatStringsSep ", " missing}; give the subflake a `checks` output from core.lib.connectorChecks (issue #7009)";
    pkgs.runCommand "connector-checks-parity" { } "touch $out";
}
