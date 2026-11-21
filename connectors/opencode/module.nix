# The opencode Connector's flake-parts module: registers its row. The native
# opencode Strategy stays authoritative: the row (./row.nix) is core's own row
# plus `module` (the wasm), and is neither `primary` nor `shadow` by default, so
# a Consumer opts into `shadow` or `primary` to run the Connector. The Driver's
# in-box half, credential rules and knobs are unchanged and stay in core.
{ core }:
{ lib, ... }:
{
  # Depends on the core it was handed; the module system dedups by path, so a
  # Consumer that imports the same core module is unaffected.
  imports = [ core.flakeModules.default ];

  spindrift.connectors.opencode.driver = import ./row.nix { inherit core lib; };
}
