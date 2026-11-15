# Pinned dogfood exception (issue #6998, ADR 0073): the dogfood runs these
# backend Connectors in primary mode, answering every read the native backend of
# the same name would. Dropping a name here restores native reads without a
# code revert, though its nix/fixtures.nix mirror row moves with it. A row is
# never in both this list and nix/dogfood-shadow.nix (flake.nix asserts it);
# flake.nix reads both, and nix/fixtures.nix's mkHarness mirror hardcodes each
# row (`backendRows.github`). A backend Connector's accepted Route table is
# baselined in spindrift-connector-routes.json.
[
  "github"
]
