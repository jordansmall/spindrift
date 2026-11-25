# The forgejo Connector as data: the Connector is both the Code Forge and the
# issue tracker (#7122), so `primary` is refused on the row. Core turns this
# into the `spindrift.connectors.forgejo` row, confined to the Route table in
# ./routes.nix.
{ core }:
{
  name = "forgejo";
  module = pkgs: "${import ./package.nix { inherit pkgs core; }}/forgejo.wasm";
  routes = import ./routes.nix;
  # Core's FORGEJO_TOKEN, which a row may name for its own backend: the host
  # attaches it, so the untrusted wasm never holds it.
  credential = "forgejoToken";
  # The instance URL is the core FORGEJO_BASE_URL knob; the base is not a
  # secret, so a core knob may carry it.
  base = "forgejoBaseURL";
  # Core derives the clone URL from the same instance URL.
  gitRemote = "forgejoBaseURL";
  # The tracker- and forge-axis prompt fragments (issue #6867).
  fragments = import ./fragments.nix;
}
