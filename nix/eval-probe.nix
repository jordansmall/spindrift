# Marginal-cost eval probe (issue #5860). `one` forces a single mkHarness
# instance, `two` forces two independent ones, both on the same nixpkgs. Each
# is evaluated in its own process (.github/scripts/eval-probe.sh), because
# NIX_SHOW_STATS is cumulative per process; two minus one is then the cost of
# the second instance, and lost Harness-core sharing shows up as growth in it.
#
# `nixpkgs` must be the shared instance (lib/nixpkgs-shared.nix): a raw input
# would charge every instance a fresh nixpkgs fixed point and measure nothing
# useful.
{ nixpkgs, system }:
let
  # A function only so each application is a separate, unshared evaluation (a
  # plain binding would be one shared thunk); the argument is ignored.
  freshInstance =
    _:
    import ../lib/mkHarness.nix {
      inherit nixpkgs system;
      nixInBox = false;
      packages = p: [ p.hello ];
    };

  # Harness values are lazy; these reach the per-instance work (the launcher
  # fileset walks behind the in-Box Go binaries and the prompt asserts) while
  # leaving the OCI image unrealized.
  force = h: [
    h.apps.default.program
    h.apps.daemon.program
    h.internals.promptDir.drvPath
    h.internals.driverExecBin.drvPath
    h.internals.orchestratorBin.drvPath
    h.internals.boxBin.drvPath
  ];
in
{
  one = force (freshInstance null);
  two = force (freshInstance null) ++ force (freshInstance null);
}
