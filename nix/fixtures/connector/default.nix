# Plain-Nix entry point of the fixture Connector (ADR 0073), shaped like a real
# Connector subflake and usable as a third-party template. `core` is handed in
# rather than imported, so the one Connector evaluates against whichever core
# the caller holds.
{
  lib.connector =
    { core }:
    {
      flakeModule = import ./module.nix { inherit core; };
      # `pkgs` is the caller's, so the build uses its Go toolchain.
      package = pkgs: import ./package.nix { inherit pkgs core; };
    };
}
