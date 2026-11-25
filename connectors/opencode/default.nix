# Plain-Nix entry point of the opencode Connector (ADR 0073): the Driver's host
# half, compiled to a wasip1 reactor against the SDK. `core` is handed in, so
# the Connector evaluates against whichever core the caller holds.
{
  lib.connector =
    { core }:
    let
      data = import ./data.nix { inherit core; };
    in
    {
      inherit data;
      # `pkgs` is the caller's, so the build uses its Go toolchain.
      package = pkgs: import ./package.nix { inherit pkgs core; };
    };
}
