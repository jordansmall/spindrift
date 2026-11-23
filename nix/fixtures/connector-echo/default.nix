# Plain-Nix entry point of the test-only `echo` Driver Connector, named after
# no core Driver; nix/checks/connector-checks-any-name.nix runs
# core.lib.connectorChecks over it. No flakeModule: nothing deploys it.
{
  lib.connector =
    { core }:
    {
      package = pkgs: import ./package.nix { inherit pkgs core; };
    };
}
