# Pinned dogfood exception (issues #6797, #6810, ADR 0073): the dogfood
# otherwise mimics a new Consumer, but runs these Connectors in shadow mode
# until their cut-over (20 consecutive clean runs). A Driver Connector replays
# Pass logs; a backend Connector would sample its reads at the row's
# `shadowEvery` rate. opencode (issue #6874) and pi (issue #6876) shadow here;
# claude is cut over (issue #6799) and has no native Strategy left to shadow.
# Its own file, not a nix/dogfood-defaults.nix leaf, because flake.nix sets
# `spindrift.connectors.<name>.shadow` at flake level, where that file's
# `system` argument does not exist. flake.nix reads the list from here;
# nix/fixtures.nix's mkHarness mirror reads it too and derives each Driver row
# from nix/connector-dirs.nix, so adding a name is the only edit. A Connector
# promoted to primary moves to nix/dogfood-primary.nix.
[
  "opencode"
  "pi"
]
