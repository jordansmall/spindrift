# Eval-level pins for lib/env-schema.nix's `networkMode` knob (issue #2562):
# mkHarness.nix accepts a valid choice, and the eval-time coherence asserts
# throw when network.mode is combined with a raw per-runtime network knob.
# mkHarnessWith forces only `.spindrift.drvPath`, which walks the assert chain
# without paying for a real build (see nix/checks/read-only-capability.nix).
{
  pkgs,
  nixpkgs,
  system,
  ...
}:
let
  inherit (pkgs.lib) assertMsg concatMap;
  runners = import ../../lib/runners/index.nix { };
  # Every (runtime, mode) pair, tagged with whether the runtime's runner row
  # lists the mode in `networkModes`.
  pairs = concatMap (
    row:
    concatMap (
      runtime:
      map (mode: {
        inherit runtime mode;
        listed = builtins.elem mode row.networkModes;
      }) runners.networkModes
    ) row.runtimes
  ) runners.rows;
  evaluates =
    { runtime, mode, ... }:
    (builtins.tryEval (builtins.seq (mkHarnessWithRuntime runtime { networkMode = mode; }) "reached"))
    .success;
  pairName = { runtime, mode, ... }: "${runtime}+${mode}";
  rejectedPairs = builtins.filter (p: p.listed && !evaluates p) pairs;
  acceptedPairs = builtins.filter (p: !p.listed && evaluates p) pairs;
  bwrapRow = runners.forRuntime "bwrap";
  mkHarnessWith =
    defaults:
    (import ../../lib/mkHarness.nix {
      inherit nixpkgs system;
      packages = p: [ p.hello ];
      inherit defaults;
    }).spindrift.drvPath;
  # `runtime` is a baked mkHarness.nix function arg, not a schema default, and
  # the row-driven mode checks below are decided by it rather than by
  # anything in `defaults`.
  mkHarnessWithRuntime =
    runtime: defaults:
    (import ../../lib/mkHarness.nix {
      inherit nixpkgs system runtime;
      packages = p: [ p.hello ];
      inherit defaults;
    }).spindrift.drvPath;
in
{
  network-mode-none-does-not-throw =
    let
      ok = builtins.tryEval (builtins.seq (mkHarnessWith { networkMode = "none"; }) "reached");
    in
    assert assertMsg ok.success
      "mkHarness.nix must not throw when networkMode is set to a valid choice (none)";
    pkgs.runCommand "network-mode-none-does-not-throw" { } "touch $out";

  # network.mode = "host" (issue #2666) is the bwrap-only opt-out back to the
  # shared host netns, but it is a valid no-op choice on the OCI runtimes too,
  # so podman must not throw on it.
  network-mode-host-does-not-throw =
    let
      ok = builtins.tryEval (builtins.seq (mkHarnessWith { networkMode = "host"; }) "reached");
    in
    assert assertMsg ok.success
      "mkHarness.nix must not throw when networkMode is set to a valid choice (host)";
    pkgs.runCommand "network-mode-host-does-not-throw" { } "touch $out";

  # There is no precedence rule between network.mode and a raw network knob,
  # so the assert must throw rather than silently pick a winner.
  network-mode-and-raw-podman-network-both-set-throws =
    let
      broken = builtins.tryEval (
        builtins.seq (mkHarnessWith {
          networkMode = "none";
          podmanNetwork = "pasta";
        }) "unreached"
      );
    in
    assert assertMsg (
      !broken.success
    ) "mkHarness.nix must throw when networkMode is set together with the raw podmanNetwork knob";
    pkgs.runCommand "network-mode-and-raw-podman-network-both-set-throws" { } "touch $out";

  # A raw network knob alone, with network.mode never set, is the documented
  # escape hatch: the coherence assert must key off `defaults ? networkMode`,
  # not off the post-merge defaulted value.
  network-mode-unset-raw-podman-network-alone-does-not-throw =
    let
      ok = builtins.tryEval (builtins.seq (mkHarnessWith { podmanNetwork = "pasta"; }) "reached");
    in
    assert assertMsg ok.success
      "mkHarness.nix must not throw when only the raw podmanNetwork knob is set and networkMode is left unset (the escape hatch)";
    pkgs.runCommand "network-mode-unset-raw-podman-network-alone-does-not-throw" { } "touch $out";

  # The coherence assert has no precedence rule for any mode value, including
  # an explicitly set "open".
  network-mode-open-and-raw-podman-network-both-set-throws =
    let
      broken = builtins.tryEval (
        builtins.seq (mkHarnessWith {
          networkMode = "open";
          podmanNetwork = "pasta";
        }) "unreached"
      );
    in
    assert assertMsg (!broken.success)
      "mkHarness.nix must throw when networkMode is explicitly set to \"open\" together with the raw podmanNetwork knob";
    pkgs.runCommand "network-mode-open-and-raw-podman-network-both-set-throws" { } "touch $out";

  # Covers the bwrapUnshareNet disjunct of the coherence check; the cases above
  # only exercise podmanNetwork.
  network-mode-and-raw-bwrap-unshare-net-both-set-throws =
    let
      broken = builtins.tryEval (
        builtins.seq (mkHarnessWith {
          networkMode = "none";
          bwrapUnshareNet = true;
        }) "unreached"
      );
    in
    assert assertMsg (
      !broken.success
    ) "mkHarness.nix must throw when networkMode is set together with the raw bwrapUnshareNet knob";
    pkgs.runCommand "network-mode-and-raw-bwrap-unshare-net-both-set-throws" { } "touch $out";

  # The assert reads each runner row's `networkModes` with no family-name
  # branch, so every (runtime, mode) pair a row lists must evaluate...
  network-mode-every-row-listed-mode-does-not-throw =
    assert assertMsg (rejectedPairs == [ ])
      "mkHarness.nix must accept every network mode its runner row lists, but threw on: ${builtins.concatStringsSep ", " (map pairName rejectedPairs)}";
    pkgs.runCommand "network-mode-every-row-listed-mode-does-not-throw" { } "touch $out";

  # ...and every enum mode a row does not list must throw.
  network-mode-every-row-unlisted-mode-throws =
    assert assertMsg (acceptedPairs == [ ])
      "mkHarness.nix must reject every network mode its runner row omits, but accepted: ${builtins.concatStringsSep ", " (map pairName acceptedPairs)}";
    pkgs.runCommand "network-mode-every-row-unlisted-mode-throws" { } "touch $out";

  # The row-derived message the harness throws. tryEval drops messages, so the
  # pure function is pinned instead. cmd/launcher/internal/runner/family_test.go
  # pins the same literal for the Go launch gate (CheckNetworkMode); change both
  # together.
  network-mode-unsupported-message-matches-go-gate =
    assert assertMsg (
      runners.unsupportedNetworkModeMessage bwrapRow "no-host-loopback"
      == "NETWORK_MODE=no-host-loopback is unsupported on runner family bwrap, which supports: open, none, host"
    ) "unsupportedNetworkModeMessage must build the same text as the Go launch gate";
    pkgs.runCommand "network-mode-unsupported-message-matches-go-gate" { } "touch $out";
}
