# Plain-Nix entry point of the claude Connector (ADR 0073): the Driver's host
# half, compiled to a wasip1 reactor against the SDK. `core` is handed in, so
# the Connector evaluates against whichever core the caller holds.
{
  lib.connector =
    { core }:
    {
      flakeModule = import ./module.nix { inherit core; };
      # `pkgs` is the caller's, so the build uses its Go toolchain.
      package = pkgs: import ./package.nix { inherit pkgs core; };
    };
}
