# The github Connector's flake-parts module: the Connector answers github's
# reads and writes for every Consumer (ADR 0073 point 13, amendments #6998 and
# #7139), confined to the Route table in ./routes.nix. The backend row and its
# token knob are registered unconditionally, so a Consumer importing this module
# always has the row; the Connector token is needed on every run whose
# CODE_FORGE or ISSUE_TRACKER is github. No native github adapter remains
# (issue #6888), so neither `shadow` nor `primary` applies: a `shadow` row
# fails at runtime.
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

  # A dedicated token, not core's GH_TOKEN: the Connector is untrusted wasm,
  # so a core secret must never reach it (lib/flakeModule.nix refuses one).
  spindrift.connectors.github.knobs.githubConnectorToken = {
    env = "SPINDRIFT_GITHUB_CONNECTOR_TOKEN";
    group = "dispatch";
    secret = true;
    doc = "bearer token the github Connector's backend row authorizes its reads and writes with (read-only under `shadow`, scope list in docs/reference.md#backend-read-shadow; write-capable otherwise, permission table in docs/reference.md#backend-primary-mode); host-only, never reaches the Box; needed on every run whose CODE_FORGE or ISSUE_TRACKER is github, since the Connector answers github's reads and writes for every Consumer";
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
