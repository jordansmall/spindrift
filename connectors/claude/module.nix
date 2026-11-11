# The claude Connector's flake-parts module: registers its row. The row owns no
# env knobs yet; the claude Driver's knobs stay in core.
{ core }:
{
  # Depends on the core it was handed; the module system dedups by path, so a
  # Consumer that imports the same core module is unaffected.
  imports = [ core.flakeModules.default ];

  spindrift.connectors.claude = { };
}
