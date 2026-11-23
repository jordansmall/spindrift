# core.lib.connectorChecks runs tracker and forge conformance for a Connector
# of any name: the test-only fixture Connectors in
# nix/fixtures/connector-conformance.nix are named after no core backend row,
# and each builds its `connector-<name>-conformance`.
{
  pkgs,
  nixpkgs,
  system,
  ...
}:
let
  fixtures = import ../fixtures/connector-conformance.nix { core = import ../../core; };
  conformance = pkgs.lib.mapAttrsToList (
    name: connector:
    (import ../../lib/connector-checks.nix {
      inherit nixpkgs name connector;
      systems = [ system ];
      src = ../fixtures/connector;
    }).${system}."connector-${name}-conformance"
  ) fixtures;
  # A backend Connector named like a core directory but with no row still gets
  # generic conformance: evaluating its check must not throw (it is not built).
  namedLikeCoreDir =
    (import ../../lib/connector-checks.nix {
      inherit nixpkgs;
      name = "pi";
      connector = builtins.head (builtins.attrValues fixtures);
      systems = [ system ];
      src = ../fixtures/connector;
    }).${system}."connector-pi-conformance";
in
{
  connector-checks-generic =
    assert pkgs.lib.assertMsg (builtins.tryEval (builtins.seq namedLikeCoreDir.drvPath true)).success
      "connectorChecks: a backend Connector named pi must not need a core backend row";
    pkgs.linkFarmFromDrvs "connector-checks-generic" conformance;
}
