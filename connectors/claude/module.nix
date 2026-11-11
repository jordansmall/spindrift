# The claude Connector's flake-parts module: registers its row. The row carries
# claude's Driver row, the same one core ships; the native Strategy stays
# authoritative and no `module` is set until shadow mode and cut-over. It owns
# no env knobs yet; the claude Driver's knobs stay in core.
{ core }:
{ lib, ... }:
{
  # Depends on the core it was handed; the module system dedups by path, so a
  # Consumer that imports the same core module is unaffected.
  imports = [ core.flakeModules.default ];

  spindrift.connectors.claude.driver = (core.lib.driverEntries { inherit lib; }).claude;
}
