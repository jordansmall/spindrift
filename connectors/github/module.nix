# The github Connector's flake-parts module: the Connector's tracker read path.
# The native github adapter stays authoritative unless the row opts in: the
# backend row below does not replace it by itself. The row's `shadow` option
# samples the Connector's reads beside the native ones, and `primary` makes the
# Connector answer them (ADR 0073 point 13, amendment #6998), confined to the
# Route table in ./routes.nix.
{ core }:
{ ... }:
let
  routes = import ./routes.nix;
in
{
  # Depends on the core it was handed; the module system dedups by path, so a
  # Consumer that imports the same core module is unaffected.
  imports = [ core.flakeModules.default ];

  # A dedicated token, not core's boxGhToken: the Connector is untrusted wasm,
  # so a core secret must never reach it (lib/flakeModule.nix refuses one).
  spindrift.connectors.github.knobs.githubConnectorToken = {
    env = "SPINDRIFT_GITHUB_CONNECTOR_TOKEN";
    group = "dispatch";
    secret = true;
    doc = "bearer token the github Connector's backend row authorizes its shadow or primary reads and, under primary, writes with (read-only under `shadow`, scope list in docs/reference.md#backend-read-shadow; write-capable under `primary`, permission table in docs/reference.md#backend-primary-mode); host-only, never reaches the Box; needed only when the row's `shadow` or `primary` option is on";
    flakeOption = false;
    boxEnv = false;
  };

  spindrift.connectors.github.routes = routes;

  spindrift.connectors.github.backend = {
    module = pkgs: "${import ./package.nix { inherit pkgs core; }}/github.wasm";
    inherit routes;
    credential = "githubConnectorToken";
  };
}
