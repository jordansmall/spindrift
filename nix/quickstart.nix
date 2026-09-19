# This builds standalone, outside the Consumer-facing lib/mkHarness.nix
# pipeline, because quickstart runs before any Consumer flake and its
# resolved runtime and driver options exist (ADR 0027).

# vendorHash comes from lib/build-constants.nix so this, lib/mkHarness.nix's
# launcherBin, and nix/checks/go.nix's launcherGoModules cannot drift apart.
# Vendoring covers the whole module, so subPackages does not change it.
{ pkgs }:
let
  buildConstants = import ../lib/build-constants.nix;
in
pkgs.buildGoModule {
  pname = "spindrift-quickstart";
  version = "0";
  src = ../cmd/launcher;
  subPackages = [ "quickstart" ];
  vendorHash = buildConstants.launcherVendorHash;
  # The launcher-go-test check (nix/checks/go.nix) already runs go test over
  # the same source, and running it here would fail on a docs/-relative path
  # one test resolves: this derivation's src has no docs/ sibling.
  doCheck = false;
  meta.license = pkgs.lib.licenses.mit;
}
