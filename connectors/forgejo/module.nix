# The forgejo Connector's flake-parts module: the Forgejo forge half. The
# native forgejo adapter stays authoritative and every binary still uses it for
# every decision. The backend row below declares the Connector and its Route
# table; nothing in the launcher reads forgejo through it yet.
{ core }:
{ ... }:
let
  routes = import ./routes.nix;
in
{
  # Depends on the core it was handed; the module system dedups by path, so a
  # Consumer that imports the same core module is unaffected.
  imports = [ core.flakeModules.default ];

  # A dedicated token, not core's forgejoToken: the Connector is untrusted
  # wasm, so a core secret must never reach it (lib/flakeModule.nix refuses one).
  spindrift.connectors.forgejo.knobs.forgejoConnectorToken = {
    env = "SPINDRIFT_FORGEJO_CONNECTOR_TOKEN";
    group = "dispatch";
    secret = true;
    doc = "bearer token the forgejo Connector's backend row authorizes its calls with (needs read access to the repository's pull requests, commit statuses and branches, and write access to delete branches); host-only, never reaches the Box; unused until the forgejo cut-over";
    flakeOption = false;
    boxEnv = false;
  };

  spindrift.connectors.forgejo.routes = routes;

  spindrift.connectors.forgejo.backend = {
    module = pkgs: "${import ./package.nix { inherit pkgs core; }}/forgejo.wasm";
    inherit routes;
    credential = "forgejoConnectorToken";
    # The instance URL is the native adapter's own FORGEJO_BASE_URL; the base is
    # not a secret, so a core knob may carry it.
    base = "forgejoBaseURL";
  };
}
