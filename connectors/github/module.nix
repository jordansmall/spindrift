# The github Connector's flake-parts module: the Connector's tracker read path.
# The native github adapter stays authoritative and every binary still uses it;
# the row only declares the Route table the Connector's http_request Host call
# is confined to.
{ core }:
{ ... }:
{
  # Depends on the core it was handed; the module system dedups by path, so a
  # Consumer that imports the same core module is unaffected.
  imports = [ core.flakeModules.default ];

  spindrift.connectors.github.routes = import ./routes.nix;
}
