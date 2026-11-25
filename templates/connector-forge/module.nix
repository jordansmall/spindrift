# The connectorname Connector's flake-parts module: the Connector's whole forge
# path. The row authorizes with a Bearer token (the default `scheme`, so none is
# set).
{ core }:
{ ... }:
let
  routes = import ./routes.nix;
in
{
  # Depends on the core it was handed; the module system dedups by path, so a
  # Consumer that imports the same core module is unaffected.
  imports = [ core.flakeModules.default ];

  # Dedicated knobs, not a core token: the Connector is untrusted wasm, so a
  # core secret must never reach it (lib/flakeModule.nix refuses one).
  spindrift.connectors.connectorname.knobs = {
    connectornameConnectorToken = {
      env = "SPINDRIFT_CONNECTORNAME_CONNECTOR_TOKEN";
      group = "dispatch";
      secret = true;
      doc = "API token the connectorname Connector's backend row authorizes its calls with; host-only, never reaches the Box; read on every run whose CODE_FORGE is connectorname";
      flakeOption = false;
      boxEnv = false;
    };
    connectornameConnectorBaseURL = {
      env = "SPINDRIFT_CONNECTORNAME_CONNECTOR_BASE_URL";
      group = "dispatch";
      doc = "https base URL of the forge the connectorname Connector talks to; the `{base}` host of its Routes resolves from it; host-only, never reaches the Box; read on every run whose CODE_FORGE is connectorname";
      flakeOption = false;
      boxEnv = false;
    };
  };

  spindrift.connectors.connectorname.routes = routes;

  spindrift.connectors.connectorname.fragments = import ./fragments.nix;

  spindrift.connectors.connectorname.backend = {
    module = pkgs: "${import ./package.nix { inherit pkgs core; }}/connectorname.wasm";
    inherit routes;
    credential = "connectornameConnectorToken";
    # The base is not a secret, so any env-schema knob may carry it; the
    # Connector's own keeps it beside its credential.
    base = "connectornameConnectorBaseURL";
    # Core derives the clone URL from the forge's base URL; a forge on a fixed
    # host declares a literal `https://host` origin instead.
    gitRemote = "connectornameConnectorBaseURL";
  };
}
