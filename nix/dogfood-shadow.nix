# Pinned dogfood exception (issue #6797, ADR 0073): the dogfood otherwise
# mimics a new Consumer, but runs these Connectors in shadow mode until their
# cut-over (20 consecutive clean runs). Its own file, not a nix/dogfood-defaults.nix
# leaf, because flake.nix sets `spindrift.connectors.<name>.shadow` at flake
# level, where that file's `system` argument does not exist. flake.nix reads the
# list from here; nix/fixtures.nix's mkHarness mirror reads it too, but hardcodes
# `driverRows.claude`, so a new name also needs its row added there.
[ "claude" ]
