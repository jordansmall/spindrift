# The dogfood's tuned leaf values, defined exactly once and consumed by both
# flake.nix's `spindrift` module config and nix/fixtures.nix's direct
# mkHarness mirror, so the flakemodule-equivalence check verifies the two
# wiring paths rather than two hand-copied value sets (issue #459).
{
  prefetch = "go mod download || true";
  packages = p: [
    p.go
    p.nil
    p.bats
    p.shellcheck
  ];
  defaults = {
    mergeMode = "immediate";
    autoFormat = true;
    autoLint = true;
  };
}
