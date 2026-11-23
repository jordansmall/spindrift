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
in
{
  connector-checks-generic = pkgs.linkFarmFromDrvs "connector-checks-generic" conformance;
}
