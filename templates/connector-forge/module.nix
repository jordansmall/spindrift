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
  # core secret must never reach it (lib/flakeModule.nix refuses one). The row
  # names no `credential`: core derives CONNECTORNAME_TOKEN from the row name,
  # and this secret knob with that env shows as the row's `credentialKnob`.
  spindrift.connectors.connectorname.knobs = {
    connectornameConnectorToken = {
      env = "CONNECTORNAME_TOKEN";
      group = "dispatch";
      secret = true;
      doc = "API token the connectorname Connector's backend row authorizes its calls with, the credential core derives from the row name; host-only, never reaches the Box; read on every run whose CODE_FORGE is connectorname";
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
    # The base is not a secret, so any env-schema knob may carry it; the
    # Connector's own keeps it beside its token.
    base = "connectornameConnectorBaseURL";
    # Core derives the clone URL from the forge's base URL; a forge on a fixed
    # host declares a literal `https://host` origin instead.
    gitRemote = "connectornameConnectorBaseURL";
  };
}
