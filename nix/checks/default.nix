# Aggregator: merges the per-concern check modules and splits the result into
# `checks` (everything, wired to `perSystem.checks`, issue #454) and
# `checks-inbox` (the source-level subset, wired to `perSystem.packages`,
# issue #581) so the Box gate can build the scoped target without re-baking
# the OCI image. The shared arg bundle is hoisted once here so every module
# sees the exact same fixtures/pkgs instead of each re-deriving its own.
{
  pkgs,
  config,
  fixtures,
  nixpkgs,
  system,
  flake-parts,
}:
let
  common = {
    inherit
      pkgs
      config
      fixtures
      nixpkgs
      system
      flake-parts
      ;
  };
  sourceChecks =
    (import ./bats.nix common)
    // (import ./equivalence.nix common)
    // (import ./preambles.nix common)
    // (import ./drivers.nix common)
    // (import ./prompt-inject.nix common)
    // (import ./prompts.nix common)
    // (import ./schema-drift.nix common)
    // (import ./dispatch-labels.nix common)
    // (import ./agent-workflow-smoke.nix common)
    // (import ./changelog.nix common)
    // (import ./go.nix common);

  imageChecks = pkgs.lib.optionalAttrs pkgs.stdenv.isLinux (import ./image.nix common);

  # Checks that realize the OCI image (dockerTools.buildLayeredImage,
  # lib/image.nix:198) directly, via a bats fixture, or by asserting facts
  # about the very box's own baked toolchain — redundant/heavy to re-run
  # *inside* the box built from that image (issue #581). Named once here;
  # `checks-inbox` below excludes them, `checks` below still carries them for
  # CI's pre-dispatch gate.
  imageOnlyCheckNames = [
    "bats"
    "nil-baked-in-dogfood"
    "bats-baked-in-dogfood"
    "shellcheck-baked-in-dogfood"
    "caveman-baked-in-dogfood"
  ];

  checksInboxSet = removeAttrs sourceChecks imageOnlyCheckNames;

  # A narrower axis than imageOnlyCheckNames: source checks whose *build*
  # closure embeds the aarch64-linux image — `bats` pulls it in through
  # batsHarness.run/build/spindrift and skillsBwrapHarness.agentFiles
  # (nix/checks/bats.nix). `nix flake check` builds the whole checkset for the
  # current system, so on darwin these fail with "Required system:
  # aarch64-linux" (there is no Linux builder). Dropped from the darwin
  # checkset below; still run on both Linux arches. Distinct from
  # imageOnlyCheckNames: the `*-baked-in-dogfood` asserts there build natively
  # on darwin (hostPkgs skillsDir / eval-only), so `bats` is the only truly
  # Linux-bound source check.
  linuxOnlyCheckNames = [ "bats" ];

  # The darwin checkset drops the Linux-bound checks; Linux keeps everything.
  portableSourceChecks =
    if pkgs.stdenv.isLinux then sourceChecks else removeAttrs sourceChecks linuxOnlyCheckNames;

  # Stale-name guard (mirrors checks-inbox-excludes-image-checks): every
  # linuxOnlyCheckName must name a real source check, or the darwin drop
  # silently does nothing. Eval-only.
  linux-only-check-names-exist =
    let
      inherit (pkgs.lib) assertMsg concatStringsSep filter;
      stale = filter (n: !(builtins.hasAttr n sourceChecks)) linuxOnlyCheckNames;
    in
    assert assertMsg (
      stale == [ ]
    ) "linuxOnlyCheckNames names a check absent from sourceChecks: ${concatStringsSep ", " stale}";
    pkgs.runCommand "linux-only-check-names-exist" { } "touch $out";

  # Regression guard (issue #581): imageOnlyCheckNames must name checks that
  # actually exist (catches a stale/renamed entry silently doing nothing),
  # and none of them may leak into checksInboxSet (catches the exclusion
  # itself regressing). Eval-only — no builder needed.
  checks-inbox-excludes-image-checks =
    let
      inherit (pkgs.lib) assertMsg concatStringsSep filter;
      stale = filter (n: !(builtins.hasAttr n sourceChecks)) imageOnlyCheckNames;
      leaked = filter (n: builtins.hasAttr n checksInboxSet) imageOnlyCheckNames;
    in
    assert assertMsg (
      stale == [ ]
    ) "imageOnlyCheckNames names a check absent from sourceChecks: ${concatStringsSep ", " stale}";
    assert assertMsg (
      leaked == [ ]
    ) "checks-inbox must not contain image-realizing checks: ${concatStringsSep ", " leaked}";
    pkgs.runCommand "checks-inbox-excludes-image-checks" { } "touch $out";
in
{
  checks =
    portableSourceChecks
    // imageChecks
    // {
      inherit checks-inbox-excludes-image-checks linux-only-check-names-exist;
    };

  # Scoped in-box gate (issue #581): every source-level check with the
  # image-realizing ones excluded, joined into one derivation so it builds
  # with a single `nix build .#checks-inbox` instead of enumerating names.
  checks-inbox = pkgs.runCommand "checks-inbox" { } ''
    ${pkgs.lib.concatMapStringsSep "\n" (p: ": ${p}") (builtins.attrValues checksInboxSet)}
    touch $out
  '';
}
