# The dogfood's tuned leaf values, defined exactly once and consumed by both
# flake.nix's `spindrift` module config and nix/fixtures.nix's direct
# mkHarness mirror, so the flakemodule-equivalence check verifies the two
# wiring paths rather than two hand-copied value sets (issue #459).
{
  # `nix flake archive` warms flake inputs alongside the Go module cache, so
  # a subsequent in-box `nix flake check` doesn't hit the network cold
  # (ADR 0008's original suggestion, wired in by issue #470).
  prefetch = "go mod download || true\nnix flake archive || true";
  packages = p: [
    p.go
    p.nil
    p.bats
    p.shellcheck
  ];
  # Self-test mode (ADR 0018, issue #469): spindrift dogfoods its own writable
  # store so a Box working a spindrift issue can run real `nix flake check`
  # in-box (issue #470) instead of round-tripping CI for nix feedback.
  nixStoreWritable = true;
  # Bake the rest of nix/checks.nix's dependency closure so in-box
  # `nix flake check` doesn't cold-substitute it. `go`/`bats`/`shellcheck`
  # above and `bash`/`coreutils`/`git`/`gettext`/`jq`/`gnugrep`/`gnused` (baked
  # unconditionally by every mkHarness image) already cover most checks;
  # `nixfmt` and `mandoc` are the remaining gap (issue #470).
  extraClosures = p: [
    p.nixfmt
    p.mandoc
  ];
  defaults = {
    mergeMode = "immediate";
    autoFormat = true;
    autoLint = true;
  };
}
