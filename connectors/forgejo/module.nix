# The forgejo Connector's flake-parts module: the Connector is both the Code
# Forge and the issue tracker (#7122), so `primary` is refused on the row. The
# backend row below declares the Connector and its Route table.
{ core }:
{ ... }:
let
  routes = import ./routes.nix;
  fragments = import ./fragments.nix;
in
{
  # Root's `flakeModules.default` already carries this module; the key lets a
  # Consumer that also imports it through `lib.connector` dedup instead of
  # failing with "registered twice".
  key = "spindrift-connector-forgejo";

  # Depends on the core it was handed; the module system dedups by path, so a
  # Consumer that imports the same core module is unaffected.
  imports = [ core.flakeModules.default ];

  spindrift.connectors.forgejo.routes = routes;
  spindrift.connectors.forgejo.fragments = fragments;

  spindrift.connectors.forgejo.backend = {
    module = pkgs: "${import ./package.nix { inherit pkgs core; }}/forgejo.wasm";
    inherit routes;
    # Core's FORGEJO_TOKEN, which a row may name for its own backend: the host
    # attaches it, so the untrusted wasm never holds it.
    credential = "forgejoToken";
    # The instance URL is the core FORGEJO_BASE_URL knob; the base is not a
    # secret, so a core knob may carry it.
    base = "forgejoBaseURL";
    # Core derives the clone URL from the same instance URL.
    gitRemote = "forgejoBaseURL";
  };
}
