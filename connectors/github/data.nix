# The github Connector as data: the Connector answers github's reads and writes
# for every Consumer (ADR 0073 point 13, amendments #6998 and #7139), confined
# to the Route table in ./routes.nix. Core turns it into the
# `spindrift.connectors.github` row. The row is registered unconditionally, so a
# Consumer importing the Connector always has it; GH_TOKEN is needed on every
# run whose CODE_FORGE or ISSUE_TRACKER is github. No native github adapter
# remains (issue #6888), so neither `shadow` nor `primary` applies: the row
# refuses both at evaluation (issue #7456).
{ core }:
{
  name = "github";
  module = pkgs: "${import ./package.nix { inherit pkgs core; }}/github.wasm";
  routes = import ./routes.nix;
  # Core's GH_TOKEN, which the row's own backend may name
  # (lib/connector-names.nix): the host attaches it, so the untrusted wasm
  # never holds it.
  credential = "ghToken";
  # Git and the web live on github.com while the Routes name api.github.com;
  # the GH_HOST rule still refuses any other host.
  gitRemote = "https://github.com";
}
