# Eval-level pins for lib/env-schema.nix's `networkMode` knob (issue #2562):
# proves mkHarness.nix accepts a valid `networkMode` choice (slice 1), and
# exercises the slice-2 eval-time coherence asserts -- network.mode set
# together with a raw per-runtime network knob (network.podman /
# network.bwrapUnshare) throws, a raw knob alone (the escape hatch, with
# network.mode left unset) does not, and network.mode = no-host-loopback is
# rejected on runtime = bwrap but accepted on the OCI runtimes. Same tryEval
# throw-guard idiom as nix/checks/read-only-capability.nix, exercised through
# the real mkHarness.nix entry point via mkHarnessWith, which forces only
# `.spindrift.drvPath` -- enough to walk the assert chain without paying for
# a real build (see read-only-capability.nix's own mkHarnessWith comment for
# the full reasoning).
{
  pkgs,
  nixpkgs,
  system,
  ...
}:
let
  inherit (pkgs.lib) assertMsg;
  mkHarnessWith =
    defaults:
    (import ../../lib/mkHarness.nix {
      inherit nixpkgs system;
      packages = p: [ p.hello ];
      inherit defaults;
    }).spindrift.drvPath;
  # Like mkHarnessWith, but also forwards `runtime` -- a baked mkHarness.nix
  # function arg (default "podman"), not a schema default -- since the
  # bwrap x no-host-loopback coherence check below is decided by `runtime`,
  # not by anything in `defaults`.
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

  # network.mode = "host" (issue #2666) is the bwrap-only documented opt-out
  # that restores the pre-#2666 shared-host-netns posture; it's a valid
  # choice on every runtime (no OCI rendering, harmless no-op there), so it
  # must not throw on the default runtime (podman) either.
  network-mode-host-does-not-throw =
    let
      ok = builtins.tryEval (builtins.seq (mkHarnessWith { networkMode = "host"; }) "reached");
    in
    assert assertMsg ok.success
      "mkHarness.nix must not throw when networkMode is set to a valid choice (host)";
    pkgs.runCommand "network-mode-host-does-not-throw" { } "touch $out";

  # network.mode set together with a raw network knob (network.podman) has
  # no precedence rule -- the Consumer must pick one, so the assert must
  # throw rather than silently pick a winner.
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

  # network.mode = no-host-loopback is unsupported on runtime = bwrap: since
  # issue #2666 a bwrap Box isolates its network namespace by default (via a
  # hardened pasta helper), so no-host-loopback would render
  # byte-identical to the default "open" -- the choice stays rejected rather
  # than let a Consumer believe it buys something "open" doesn't already
  # give.
  network-mode-no-host-loopback-on-bwrap-throws =
    let
      broken = builtins.tryEval (
        builtins.seq (mkHarnessWithRuntime "bwrap" { networkMode = "no-host-loopback"; }) "unreached"
      );
    in
    assert assertMsg (
      !broken.success
    ) "mkHarness.nix must throw when networkMode = no-host-loopback is combined with runtime = bwrap";
    pkgs.runCommand "network-mode-no-host-loopback-on-bwrap-throws" { } "touch $out";

  # network.mode = no-host-loopback on the default runtime (podman) is fully
  # supported -- the bwrap-specific throw above must not over-fire onto the
  # oci runner family.
  network-mode-no-host-loopback-on-podman-does-not-throw =
    let
      ok = builtins.tryEval (
        builtins.seq (mkHarnessWith { networkMode = "no-host-loopback"; }) "reached"
      );
    in
    assert assertMsg ok.success
      "mkHarness.nix must not throw when networkMode = no-host-loopback is combined with the default runtime (podman)";
    pkgs.runCommand "network-mode-no-host-loopback-on-podman-does-not-throw" { } "touch $out";

  # Control: a raw network knob set alone, with network.mode left UNSET
  # entirely, is the documented escape hatch -- the coherence assert must key
  # off whether network.mode was explicitly set (defaults ? networkMode), not
  # off the post-merge/defaulted value, so this must not throw.
  network-mode-unset-raw-podman-network-alone-does-not-throw =
    let
      ok = builtins.tryEval (builtins.seq (mkHarnessWith { podmanNetwork = "pasta"; }) "reached");
    in
    assert assertMsg ok.success
      "mkHarness.nix must not throw when only the raw podmanNetwork knob is set and networkMode is left unset (the escape hatch)";
    pkgs.runCommand "network-mode-unset-raw-podman-network-alone-does-not-throw" { } "touch $out";

  # network.mode explicitly set to "open" together with a raw network knob
  # must still throw -- the coherence assert has no precedence rule between
  # network.mode and the raw knobs for ANY mode value, including an
  # explicitly-set "open", not just the non-open choices.
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

  # network.mode set together with the raw bwrapUnshareNet knob (rather than
  # podmanNetwork) must also throw -- the existing coverage above only ever
  # exercises the podmanNetwork disjunct of the coherence check, leaving the
  # bwrapUnshareNet disjunct untested.
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
}
