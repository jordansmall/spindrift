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
  buildConstants = import ../../lib/build-constants.nix;

  # Same shard module bats.nix generates its bats-shard-N checks from
  # (nix/checks/bats-shards.nix, issue #2648) — imported once here and
  # threaded through `common` so bats.nix reuses this evaluation instead of
  # each re-running the directory scan/@test count over every tests/*.bats
  # file, and so the image-only/linux-only name lists below are generated
  # from that one source instead of hand-duplicating the shard name list.
  batsShards = import ./bats-shards.nix { inherit pkgs; };
  batsShardNames = batsShards.shardNames;

  # The vendored module tree for cmd/launcher's external deps
  # (charmbracelet/bubbletea, issue #784), built once here so every check
  # module that needs to `go build`/`go vet`/`go test` a reconstructed
  # launcher tree (nix/checks/go.nix, nix/checks/baked-skills.nix) shares the
  # exact same vendor tree instead of each re-deriving its own buildGoModule
  # call against the same inputs.
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
    // (import ./prompt-contract.nix common)
    // (import ./prompt-contract-parity.nix common)
    // (import ./research-verdicts.nix common)
    // (import ./jira-status-mapping.nix common)
    // (import ./read-only-capability.nix common)
    // (import ./network-mode.nix common)
    // (import ./prompts.nix common)
    // (import ./schema-drift.nix common)
    // (import ./quickstart-golden.nix common)
    // (import ./dispatch-labels.nix common)
    // (import ./agent-workflow-smoke.nix common)
    // (import ./changelog.nix common)
    // (import ./go.nix common)
    // (import ./roster.nix common)
    // (import ./promptassembly.nix common)
    // (import ./baked-skills.nix common)
    // (import ./seccomp.nix common);

  imageChecks = pkgs.lib.optionalAttrs pkgs.stdenv.isLinux (import ./image.nix common);

  # Checks that realize the OCI image (dockerTools.buildLayeredImage,
  # lib/image.nix:198) directly, via a bats fixture, or by asserting facts
  # about the very box's own baked toolchain — redundant/heavy to re-run
  # *inside* the box built from that image (issue #581). Named once here;
  # `checks-inbox` below excludes them, `checks` below still carries them for
  # CI's pre-dispatch gate. Each bats-shard-N (issue #2648) carries the same
  # image dependency the old single `bats` derivation had.
  imageOnlyCheckNames = batsShardNames ++ [
    "nil-baked-in-dogfood"
    "bats-baked-in-dogfood"
    "shellcheck-baked-in-dogfood"
    "caveman-baked-in-dogfood"
    # Realizes the real (unfree) claude-code package to grep its own binary
    # (issue #2011) -- a fact about the box's own baked toolchain, same
    # exclusion reasoning as the rest of this list.
    "drivers-claude-cli-knows-disable-background-tasks-env"
  ];

  # Built from portableSourceChecks (not sourceChecks) so a darwin eval also
  # drops linuxOnlyCheckNames here -- otherwise packages.<system>.checks-inbox
  # below forces mkharness-agent-closure-package's build on darwin, where
  # bwrapHarness.packages lacks agent-closure and its own assert throws.
  checksInboxSet = removeAttrs portableSourceChecks imageOnlyCheckNames;

  # A narrower axis than imageOnlyCheckNames: source checks whose *build*
  # closure embeds the aarch64-linux image — each bats-shard-N pulls it in
  # through batsHarness.internals.run/build and
  # skillsBwrapHarness.internals.agentFiles (nix/checks/bats.nix, issue
  # #2648, same dependency the old single `bats` derivation had);
  # `promptassembly-parity` pulls in the same batsHarness's
  # internals.driverExecBin (nix/checks/promptassembly.nix) — mkHarness.nix
  # always re-instantiates pkgs for the Linux twin of the host system, so
  # either derivation is Linux-only regardless of which system evaluates it.
  # `bats-outcome-opencode` and `bats-prompt-contract-parity`
  # (nix/checks/bats.nix) pull in the same batsHarness.internals.driverExecBin
  # now that $ENTRYPOINT unconditionally shells out to `driver-exec
  # assemble-prompt` (issue #2354), so they're Linux-only for the same
  # reason. `nix flake check` builds the whole checkset for the current
  # system, so on darwin these fail with "Required system: aarch64-linux"
  # (there is no Linux builder). Dropped from the darwin checkset below;
  # still run on both Linux arches. Distinct from imageOnlyCheckNames: the
  # `*-baked-in-dogfood` asserts there build natively on darwin (hostPkgs
  # skillsDir / eval-only).
  linuxOnlyCheckNames = batsShardNames ++ [
    "promptassembly-parity"
    "bats-outcome-opencode"
    "bats-prompt-contract-parity"
    # flake.nix only exposes apps.regen-goldens under a `pkgs.stdenv.isLinux`
    # guard, same as promptassembly-parity is listed here for: both pull in
    # fixtures.batsHarness.internals.driverExecBin, which mkHarness.nix only
    # builds when isLinux -- absent from config.apps on darwin, so this
    # check's own existence assert would throw there for the same reason
    # dogfood-bwrap-app-wiring is linux-only below.
    "regen-goldens-app-wiring"
    # nix/fixtures.nix's bwrapHarness is instantiated at the *current*
    # system, and lib/mkHarness.nix only exposes packages.agent-closure when
    # isLinux (system == linuxSystem) -- on darwin bwrapHarness.packages
    # lacks it, so this check's own assert throws during eval. Linux-only
    # for the same reason agent-closure itself is Linux-only.
    "mkharness-agent-closure-package"
    "mkharness-agent-closure-bundles-both"
    # Same isLinux gate, one hop further out: asserts config.packages (the
    # flake's own top-level output) carries agent-closure, which flake.nix
    # only re-exports when fixtures.dogfoodBwrapHarness.packages has it --
    # absent on darwin for the same reason as the two checks above.
    "dogfood-bwrap-app-wiring"
    # lib/seccomp.nix (nix/checks/seccomp.nix) builds against pkgs.libseccomp,
    # whose meta.platforms is Linux-only -- evaluating it on darwin throws
    # "Refusing to evaluate package 'libseccomp' ... not available on the
    # requested hostPlatform" before the check derivation itself ever runs.
    "seccomp-filter-is-regular-file-multiple-of-8-bytes"
  ];

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
