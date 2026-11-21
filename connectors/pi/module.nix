# The pi Connector's flake-parts module: registers its row. The row carries pi's
# Driver row (./row.nix): core's own facts plus the `module` wasm. The native
# Strategy stays authoritative; the module runs only when the row's `shadow` or
# `primary` option is on. It owns no env knobs; the pi Driver's knobs stay in
# core.
{ core }:
{ lib, ... }:
{
  # Depends on the core it was handed; the module system dedups by path, so a
  # Consumer that imports the same core module is unaffected.
  imports = [ core.flakeModules.default ];

  spindrift.connectors.pi.driver = import ./row.nix { inherit core lib; };
}
