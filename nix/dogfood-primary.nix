# Pinned dogfood exception (issue #6998, ADR 0073): the dogfood runs these
# Connectors in primary mode. A Driver Connector would be the Driver in place
# of the native Strategy, with no fallback; a backend Connector answers every
# read the native backend of the same name would, but only a backend that still
# has a native adapter can be listed (the module refuses `primary` elsewhere).
# github, jira and forgejo have none left, so the list holds no backend.
# Dropping a name here restores the native path without a code revert, though a
# backend's hand-written `primary` in nix/fixtures.nix's mirror row moves with
# it. A row is never in both this list and nix/dogfood-shadow.nix (flake.nix
# asserts it); flake.nix reads both, and nix/fixtures.nix's mirror derives each
# Driver row from nix/connector-dirs.nix via `dogfoodDriverRows`, marking it
# primary through `primaryDrivers`. claude is not listed: it is cut over (issue
# #6799), has no native Strategy, and its Connector is the Driver for every
# Consumer.
[ ]
