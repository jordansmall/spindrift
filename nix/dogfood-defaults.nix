# The dogfood's tuned leaf values, defined once and consumed by both flake.nix's
# `spindrift` module config and nix/fixtures.nix's direct mkHarness mirror, so
# the flakemodule-equivalence check compares two wiring paths rather than two
# hand-copied value sets (issue #459).
{ system, lib }:
let
  isLinux = builtins.match ".*-linux" system != null;
  rosterLib = import ../lib/roster.nix { inherit lib; };
in
{
  # `nix flake archive` warms flake inputs alongside the Go module cache so a
  # later in-box `nix flake check` doesn't hit the network cold (ADR 0008,
  # issue #470).
  prefetch = "go mod download || true\nnix flake archive || true";
  packages = p: [
    p.go
    p.nil
    p.bats
    p.shellcheck
  ];
  # Self-test mode (ADR 0018, issue #469): a writable store lets a Box working a
  # spindrift issue run real checks in-box (issue #470) instead of
  # round-tripping CI for nix feedback.
  nixStoreWritable = true;
  # Bake the rest of nix/checks.nix's dependency closure so in-box checks don't
  # cold-substitute it. The packages above and the ones every mkHarness image
  # bakes already cover most checks; `nixfmt` and `mandoc` are the remaining
  # gap (issue #470).
  extraClosures = p: [
    p.nixfmt
    p.mandoc
  ];
  # `defaultRoster` sources models and efforts by roster entry name, not the
  # legacy per-agent knobs (issues #2386, #2388, #2426). Scout, reviewer, and
  # worker inherit the `lib/env-schema.nix` defaults (issues #2387, #2433,
  # #2434, #2435, #2512); a local pin would duplicate them. `filer` is the one
  # exception: its default is empty, so findings would stay in PR bodies (#393).
  roster = rosterLib.defaultRoster {
    models = {
      filer = "claude-haiku-4-5-20251001";
    };
  };
  defaults = {
    mergeMode = "immediate";
    autoFormat = true;
    autoLint = true;
    # Dogfood the host-mediated read-only path (ADR 0034, #1916-#1919): the Box
    # makes no forge or tracker writes, and the launcher relays branch, draft
    # PR, and comment writes host-side.
    boxForgeAndIssueAccess = "read-only";
    # On darwin and windows podman runs in a fixed-RAM VM and needs the cap
    # (issue #712); native Linux shares host RAM with the container, so no cap
    # is needed there (issue #2379).
    memoryLimit = if isLinux then "" else "5g";
  };
}
