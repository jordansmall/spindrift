# Plain-Nix entry point of the test-only `echo` Driver Connector, named after
# no core Driver; nix/checks/connector-checks-any-name.nix runs
# core.lib.connectorChecks over it. Nothing deploys it, so nothing registers
# its data.
{
  lib.connector =
    { core }:
    {
      data = import ./data.nix { inherit core; };
      package = pkgs: import ./package.nix { inherit pkgs core; };
    };
}
