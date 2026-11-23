# Pinned dogfood exception (issue #6998, ADR 0073): the dogfood runs these
# Connectors in primary mode. A Driver Connector would be the Driver in place
# of the native Strategy, with no fallback. A backend row cannot be listed: the
# module refuses `primary` on one (issue #7456).
# Dropping a name here restores the native path without a code revert. A row is
# never in both this list and nix/dogfood-shadow.nix (flake.nix asserts it);
# flake.nix reads both, and nix/fixtures.nix's mirror derives each
# Driver row from nix/connector-dirs.nix via `dogfoodDriverRows`, marking it
# primary through `primaryDrivers`. claude is not listed: it is cut over (issue
# #6799), has no native Strategy, and its Connector is the Driver for every
# Consumer.
[ ]
