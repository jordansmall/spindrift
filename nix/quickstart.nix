# This builds standalone, outside the Consumer-facing lib/mkHarness.nix
# pipeline, because quickstart runs before any Consumer flake and its
# resolved runtime and driver options exist (ADR 0027).

# vendorHash comes from lib/build-constants.nix so this, lib/mkHarness.nix's
# launcherBin, and nix/checks/go.nix's launcherGoModules cannot drift apart.
# Vendoring covers the whole module, so subPackages does not change it.
{ pkgs }:
let
  buildConstants = import ../lib/build-constants.nix;
  # The github Connector quickstart's identity probe and token scope audit go
  # through, so the wizard needs no gh for them (ADR 0073). Built from the
  # checkout's own core, as nix/checks/go.nix does.
  connector = (import ../connectors/github).lib.connector { core = import ../core; };
in
pkgs.buildGoModule {
  pname = "spindrift-quickstart";
  version = "0";
  src = ../cmd/launcher;
  subPackages = [ "quickstart" ];
  vendorHash = buildConstants.launcherVendorHash;
  nativeBuildInputs = [ pkgs.makeWrapper ];
  # quickstart's main reads these into a github backend row
  # (cmd/launcher/quickstart/connector_row.go); a plain `go run` sets neither
  # and keeps native gh.
  postInstall = ''
    wrapProgram $out/bin/quickstart \
      --set SPINDRIFT_BACKEND_CONNECTOR_WASM ${connector.package pkgs}/github.wasm \
      --set SPINDRIFT_BACKEND_CONNECTOR_ROUTES ${pkgs.lib.escapeShellArg (builtins.toJSON connector.routes)}
  '';
  # The launcher-binaries-go-test check (nix/checks/go.nix) already runs go
  # test over the same source, and running it here would fail on a
  # docs/-relative path one test resolves: this derivation's src has no docs/
  # sibling.
  doCheck = false;
  meta.license = pkgs.lib.licenses.mit;
}
