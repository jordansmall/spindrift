# The Driver registry (ADR 0009): one entry per in-box agent CLI, keyed by
# name. lib/mkHarness.nix selects an entry by its `driver` option (default
# "claude") and bakes its data into the image; the Go launcher selects the
# matching host-side strategy by the same name via DRIVER (see
# cmd/launcher/internal/driver). A parity test
# (cmd/launcher/internal/driver/parity_test.go) asserts the two registries'
# names never drift.
{ lib }:
{
  claude = import ./claude.nix { inherit lib; };
}
