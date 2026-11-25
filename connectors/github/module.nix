# The github Connector's flake-parts module: the Connector answers github's
# reads and writes for every Consumer (ADR 0073 point 13, amendments #6998 and
# #7139), confined to the Route table in ./routes.nix. The backend row's
# credential is core's GH_TOKEN. The row is registered unconditionally, so a
# Consumer importing this module always has it; GH_TOKEN is needed on every run
# whose CODE_FORGE or ISSUE_TRACKER is github. No native github adapter remains
# (issue #6888), so neither `shadow` nor `primary` applies: the row refuses both
# at evaluation (issue #7456).
{ core }:
_:
let
  routes = import ./routes.nix;
in
{
  # Root's `flakeModules.default` already carries this module; the key lets a
  # Consumer that also imports it through `lib.connector` dedup instead of
  # failing with "registered twice".
  key = "spindrift-connector-github";

  # Depends on the core it was handed; the module system dedups by path, so a
  # Consumer that imports the same core module is unaffected.
  imports = [ core.flakeModules.default ];

  spindrift.connectors.github.routes = routes;

  spindrift.connectors.github.backend = {
    module = pkgs: "${import ./package.nix { inherit pkgs core; }}/github.wasm";
    inherit routes;
    # Core's GH_TOKEN, which the row's own backend may name
    # (lib/connector-names.nix): the host attaches it, so the untrusted wasm
    # never holds it.
    credential = "ghToken";
    # Git and the web live on github.com while the Routes name api.github.com;
    # the GH_HOST rule still refuses any other host.
    gitRemote = "https://github.com";
  };
}
