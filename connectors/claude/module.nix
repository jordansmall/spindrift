# The claude Connector's flake-parts module: registers its row. The row carries
# claude's Driver row (./row.nix): core's own facts plus the `module` wasm. The
# claude Driver has no native Strategy (issue #6799): this Connector is the Driver
# for every Consumer, so `primary` and `shadow` are refused on this row. It owns
# no env knobs yet; the claude Driver's knobs stay in core.
{ core }:
{ lib, ... }:
{
  # Depends on the core it was handed; the module system dedups by path, so a
  # Consumer that imports the same core module is unaffected.
  imports = [ core.flakeModules.default ];

  spindrift.connectors.claude.driver = import ./row.nix { inherit core lib; };
}
