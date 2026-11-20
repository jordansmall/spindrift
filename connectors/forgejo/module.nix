# The forgejo Connector's flake-parts module: the Forgejo forge and tracker.
# The Connector is always the Code Forge. The native forgejo adapter still
# builds the issue tracker until #7122, unless a Consumer sets the row's
# `primary`, which builds the tracker from the Connector too. The backend row
# below declares the Connector and its Route table.
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

  # A dedicated token, not core's forgejoToken: the Connector is untrusted
  # wasm, so a core secret must never reach it (lib/flakeModule.nix refuses one).
  spindrift.connectors.forgejo.knobs.forgejoConnectorToken = {
    env = "SPINDRIFT_FORGEJO_CONNECTOR_TOKEN";
    group = "dispatch";
    secret = true;
    doc = "bearer token the forgejo Connector's backend row authorizes its calls with (needs read access to the repository, its pull requests, commit statuses and branches, and write access to open, edit, merge and comment on pull requests and to delete branches); host-only, never reaches the Box; needed on every run whose CODE_FORGE is forgejo, and on an ISSUE_TRACKER=forgejo run only when the row sets primary";
    flakeOption = false;
    boxEnv = false;
  };

  spindrift.connectors.forgejo.routes = routes;
  spindrift.connectors.forgejo.fragments = fragments;

  spindrift.connectors.forgejo.backend = {
    module = pkgs: "${import ./package.nix { inherit pkgs core; }}/forgejo.wasm";
    inherit routes;
    credential = "forgejoConnectorToken";
    # The instance URL is the native adapter's own FORGEJO_BASE_URL; the base is
    # not a secret, so a core knob may carry it.
    base = "forgejoBaseURL";
  };
}
