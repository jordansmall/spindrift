# The claude Connector's flake-parts module: registers its row. The row carries
# claude's Driver row (./row.nix): core's own facts plus the `module` wasm. The
# native Strategy stays authoritative; the module runs only when the row's
# `shadow` option is on, until cut-over. It owns no env knobs yet; the claude
# Driver's knobs stay in core.
{ core }:
{ lib, ... }:
{
  # Depends on the core it was handed; the module system dedups by path, so a
  # Consumer that imports the same core module is unaffected.
  imports = [ core.flakeModules.default ];

  spindrift.connectors.claude.driver = import ./row.nix { inherit core lib; };
}
