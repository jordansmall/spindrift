# `nix run .#quickstart` (ADR 0027): builds the pre-CLI Quickstart wizard
# binary from cmd/launcher/quickstart, standalone from the Consumer-facing
# lib/mkHarness.nix pipeline — quickstart runs *before* any Consumer flake
# (and its resolved runtime/driver options) exists, so it cannot be built
# through that config-dependent machinery.
#
# vendorHash matches lib/mkHarness.nix's launcherBin/driverExecBin and
# nix/checks/go.nix's launcherGoModules (same cmd/launcher go.mod/go.sum,
# same full source tree — vendoring is a whole-module computation
# independent of subPackages); update all three together.
{ pkgs }:
pkgs.buildGoModule {
  pname = "spindrift-quickstart";
  version = "0";
  src = ../cmd/launcher;
  subPackages = [ "quickstart" ];
  vendorHash = "sha256-pz95WwGNc065UWJspokZ4heMGKWh8Bsi+5O+UmCAtqA=";
  # go test ./... already runs, vendored and offline, as the
  # launcher-go-test check (nix/checks/go.nix) against the same source —
  # running it again here would also fail on a docs/-relative path one of
  # those tests resolves, since this derivation's src has no docs/ sibling.
  doCheck = false;
  meta.license = pkgs.lib.licenses.mit;
}
