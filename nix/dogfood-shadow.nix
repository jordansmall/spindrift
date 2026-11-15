# Pinned dogfood exception (issues #6797, #6810, ADR 0073): the dogfood
# otherwise mimics a new Consumer, but runs these Connectors in shadow mode
# until their cut-over (20 consecutive clean runs). A Driver Connector replays
# Pass logs; a backend Connector would sample its reads at the row's
# `shadowEvery` rate. Its own file, not a nix/dogfood-defaults.nix leaf, because
# flake.nix sets `spindrift.connectors.<name>.shadow` at flake level, where that
# file's `system` argument does not exist. flake.nix reads the list from here;
# nix/fixtures.nix's mkHarness mirror reads it too, but hardcodes each row
# (`driverRows.claude`), so a new name also needs its row added there. A
# backend Connector promoted to primary moves to nix/dogfood-primary.nix.
[
  "claude"
]
