# Pinned dogfood exception (issues #6998, #6999, ADR 0073): the dogfood runs
# these Connectors in primary mode. A backend Connector answers every read the
# native backend of the same name would; a Driver Connector is the Driver in
# place of the native Strategy, with no fallback. Dropping a name here restores
# the native path without a code revert, though its nix/fixtures.nix mirror row
# moves with it. A row is never in both this list and nix/dogfood-shadow.nix
# (flake.nix asserts it); flake.nix reads both, and nix/fixtures.nix's mkHarness
# mirror hardcodes each row (`primaryDrivers` for a Driver row via
# `dogfoodDriverRows`, `backendRows.github.primary` for a backend row). A
# backend Connector's accepted Route table is baselined in
# spindrift-connector-routes.json. github's primary also carries #6756's
# switches as they land: the writes docs/reference.md lists under "Backend
# primary mode" go through the Connector. Each switch opens the next one's
# observation window, so dogfood runs from switch 2 (#6885) on count toward
# #6886's bar.
[
  "claude"
  "github"
]
