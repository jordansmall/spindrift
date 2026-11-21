# The forgejo Connector's flake-parts module: the Forgejo forge and tracker.
# The Connector is always the Code Forge and, since the native forgejo tracker
# was deleted (#7122), the issue tracker too; `primary` is refused on the row.
# The backend row below declares the Connector and its Route table.
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
    doc = "bearer token the forgejo Connector's backend row authorizes its calls with (needs read access to the repository, its pull requests, commit statuses and branches, and write access to open, edit, merge and comment on pull requests and to delete branches); host-only, never reaches the Box; needed on every run whose CODE_FORGE is forgejo, and on every run whose ISSUE_TRACKER is forgejo";
    flakeOption = false;
    boxEnv = false;
  };

  spindrift.connectors.forgejo.routes = routes;
  spindrift.connectors.forgejo.fragments = fragments;

  spindrift.connectors.forgejo.backend = {
    module = pkgs: "${import ./package.nix { inherit pkgs core; }}/forgejo.wasm";
    inherit routes;
    credential = "forgejoConnectorToken";
    # The instance URL is the core FORGEJO_BASE_URL knob; the base is not a
    # secret, so a core knob may carry it.
    base = "forgejoBaseURL";
  };
}
