# The connectorname Connector's flake-parts module: registers its Driver row.
# The Box loads the Driver in-process only when the batteries flake's own build
# of this wasm is the row's `module`; mkHarness derives that mark, a row cannot
# set it.
{ core }:
{ lib, ... }:
{
  # Depends on the core it was handed; the module system dedups by path, so a
  # Consumer that imports the same core module is unaffected.
  imports = [ core.flakeModules.default ];

  spindrift.connectors.connectorname.driver = import ./row.nix { inherit core lib; };
}
