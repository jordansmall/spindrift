# The connectorname Connector as data: the Connector's whole tracker path. Core
# turns this into the `spindrift.connectors.connectorname` row, confined to the
# Route table in ./routes.nix. The row authorizes with a Bearer token (the
# default `scheme`, so none is set); for Basic credentials (an account email
# plus API token) add an `identity` knob and set `scheme = "basic"`.
{ core }:
{
  name = "connectorname";
  module = pkgs: "${import ./package.nix { inherit pkgs core; }}/connectorname.wasm";
  routes = import ./routes.nix;
  # The base is not a secret, so any env-schema knob may carry it; the
  # Connector's own keeps it beside its token.
  base = "connectornameConnectorBaseURL";
  # Dedicated knobs, not a core token: the Connector is untrusted wasm, so a
  # core secret must never reach it (lib/flakeModule.nix refuses one). The row
  # names no `credential`: core derives CONNECTORNAME_TOKEN from the row name,
  # and this secret knob with that env shows as the row's `credentialKnob`.
  knobs = {
    connectornameConnectorToken = {
      env = "CONNECTORNAME_TOKEN";
      group = "dispatch";
      secret = true;
      doc = "API token the connectorname Connector's backend row authorizes its calls with, the credential core derives from the row name; host-only, never reaches the Box; read on every ISSUE_TRACKER=connectorname run";
      flakeOption = false;
      boxEnv = false;
    };
    connectornameConnectorBaseURL = {
      env = "SPINDRIFT_CONNECTORNAME_CONNECTOR_BASE_URL";
      group = "dispatch";
      doc = "https base URL of the tracker the connectorname Connector talks to; the `{base}` host of its Routes resolves from it; host-only, never reaches the Box; read on every ISSUE_TRACKER=connectorname run";
      flakeOption = false;
      boxEnv = false;
    };
  };
  # The tracker-axis prompt fragments (issue #6857).
  fragments = import ./fragments.nix;
}
