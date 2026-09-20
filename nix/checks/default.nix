# Merges the per-concern check modules into `checks` (everything, issue #454)
# and `checks-inbox` (the source-level subset, issue #581) so the Box gate can
# build the scoped target without re-baking the OCI image. The shared arg
# bundle is hoisted once so every module sees the same fixtures and pkgs.
{
  pkgs,
  config,
  fixtures,
  nixpkgs,
  system,
  flake-parts,
}:
let
  buildConstants = import ../../lib/build-constants.nix;

  # bats.nix generates its bats-shard-N checks from this same module
  # (issue #2648). Importing it once keeps the directory scan to a single run
  # and lets the name lists below derive from it instead of hand-duplicating.
  batsShards = import ./bats-shards.nix { inherit pkgs; };
  batsShardNames = batsShards.shardNames;

  # Every check module that reconstructs a launcher tree (go.nix,
  # baked-skills.nix) shares this one vendor tree instead of deriving its own
  # (issue #784).
  launcherGoModules =
    (pkgs.buildGoModule {
      pname = "spindrift-launcher-modules";
      version = "0";
      src = ../../cmd/launcher;
      vendorHash = buildConstants.launcherVendorHash;
    }).goModules;

  common = {
    inherit
      pkgs
      config
      fixtures
      nixpkgs
      system
      flake-parts
      launcherGoModules
      batsShards
      ;
  };
  sourceChecks =
    (import ./bats.nix common)
    // (import ./equivalence.nix common)
    // (import ./builtins-compat.nix common)
    // (import ./preambles.nix common)
    // (import ./drivers.nix common)
    // (import ./prompt-inject.nix common)
    // (import ./fragment-pairs.nix common)
    // (import ./tdd-fragment-parity.nix common)
    // (import ./commit-fragment-parity.nix common)
    // (import ./nix-checks-lore-parity.nix common)
    // (import ./scout-rationale-parity.nix common)
    // (import ./code-review-fragment-parity.nix common)
    // (import ./prompt-contract.nix common)
    // (import ./prompt-contract-parity.nix common)
    // (import ./research-verdicts.nix common)
    // (import ./jira-status-mapping.nix common)
    // (import ./awake-window.nix common)
    // (import ./read-only-capability.nix common)
    // (import ./network-mode.nix common)
    // (import ./prompts.nix common)
    // (import ./schema-drift.nix common)
    // (import ./quickstart-golden.nix common)
    // (import ./dispatch-labels.nix common)
    // (import ./gh-token-intervals.nix common)
    // (import ./agent-workflow-smoke.nix common)
    // (import ./changelog.nix common)
    // (import ./go.nix common)
    // (import ./roster.nix common)
    // (import ./promptassembly.nix common)
    // (import ./baked-skills.nix common)
    // (import ./seccomp.nix common);

  imageChecks = pkgs.lib.optionalAttrs pkgs.stdenv.isLinux (import ./image.nix common);

  # Checks that realize the OCI image, directly or by asserting facts about the
  # box's own baked toolchain. Re-running them inside the box built from that
  # image is redundant and heavy (issue #581), so `checks-inbox` excludes them
  # while `checks` keeps them for CI's pre-dispatch gate. Each bats-shard-N
  # (issue #2648) carries the same image dependency.
  imageOnlyCheckNames = batsShardNames ++ [
    "nil-baked-in-dogfood"
    "bats-baked-in-dogfood"
    "shellcheck-baked-in-dogfood"
    "caveman-baked-in-dogfood"
    # Realizes the real (unfree) claude-code package to grep its own binary
    # (issue #2011), a fact about the box's own baked toolchain.
    "drivers-claude-cli-knows-disable-background-tasks-env"
  ];

  # This starts from portableSourceChecks, not sourceChecks, so a darwin eval
  # also drops linuxOnlyCheckNames. Otherwise checks-inbox forces
  # mkharness-agent-closure-package's build on darwin, where
  # bwrapHarness.packages lacks agent-closure and its own assert throws.
  checksInboxSet = removeAttrs portableSourceChecks imageOnlyCheckNames;

  # A narrower axis than imageOnlyCheckNames: source checks whose build closure
  # embeds the aarch64-linux image through the bats harness internals
  # (issue #2648, issue #2354). mkHarness.nix always instantiates pkgs for the
  # Linux twin of the host system, so a darwin eval still demands a Linux
  # builder and fails with "Required system: aarch64-linux".
  linuxOnlyCheckNames = batsShardNames ++ [
    "promptassembly-parity"
    "bats-outcome-opencode"
    "bats-prompt-contract-parity"
    # flake.nix only exposes apps.regen-goldens under isLinux, because it pulls
    # in fixtures.batsHarness.internals.driverExecBin. On darwin it is absent
    # from config.apps, so this check's own existence assert would throw.
    "regen-goldens-app-wiring"
    # lib/mkHarness.nix only exposes packages.agent-closure when isLinux, and
    # nix/fixtures.nix instantiates bwrapHarness at the current system, so on
    # darwin bwrapHarness.packages lacks it and this assert throws during eval.
    "mkharness-agent-closure-package"
    "mkharness-agent-closure-bundles-both"
    "mkharness-agent-closure-tracks-prefetch"
    # Same isLinux gate one hop further out: asserts config.packages carries
    # agent-closure, which flake.nix only re-exports when
    # fixtures.dogfoodBwrapHarness.packages has it.
    "dogfood-bwrap-app-wiring"
    # lib/seccomp.nix builds against pkgs.libseccomp, whose meta.platforms is
    # Linux-only, so evaluating it on darwin throws "Refusing to evaluate
    # package 'libseccomp'" before the check derivation itself ever runs.
    "seccomp-filter-is-regular-file-multiple-of-8-bytes"
  ];

  portableSourceChecks =
    if pkgs.stdenv.isLinux then sourceChecks else removeAttrs sourceChecks linuxOnlyCheckNames;

  # Stale-name guard: every linuxOnlyCheckName must name a real source check,
  # or the darwin drop silently does nothing.
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
  # exist, or a renamed entry silently does nothing, and none of them may leak
  # into checksInboxSet, or the exclusion itself has regressed.
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

  # Scoped in-box gate (issue #581): the source-level checks joined into one
  # derivation so it builds with a single `nix build .#checks-inbox` instead
  # of enumerating names.
  checks-inbox = pkgs.runCommand "checks-inbox" { } ''
    ${pkgs.lib.concatMapStringsSep "\n" (p: ": ${p}") (builtins.attrValues checksInboxSet)}
    touch $out
  '';
}
