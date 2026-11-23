# Scaffold app (issues #7421, #7423):
# `nix run .#new-connector -- <tracker|driver> <name> [<dir>]` writes
# templates/connector-<kind> with the name substituted. The templates and the
# core backend and Driver row names are baked in here, so the app runs from
# `nix run github:jordansmall/spindrift#new-connector` with no checkout, and
# the row lists are the ones lib/connector-checks.nix looks `row` and the core
# Driver up in.
#
# `self` is the spindrift flake. Its commit becomes the pseudo-version the
# generated go.mod pins the SDK at; an uncommitted tree has no rev, so none.
{
  pkgs,
  self ? { },
}:
let
  sdkVersion =
    if self ? rev then "v0.0.0-${self.lastModifiedDate}-${builtins.substring 0 12 self.rev}" else "";
in
pkgs.writeShellApplication {
  name = "new-connector";
  runtimeInputs = [
    pkgs.coreutils
    pkgs.findutils
    pkgs.gnugrep
    pkgs.gnused
  ];
  runtimeEnv = {
    # Only the Connector templates, so a templates/default edit never rebuilds
    # the app.
    NEW_CONNECTOR_TEMPLATES = pkgs.lib.fileset.toSource {
      root = ../templates;
      fileset = pkgs.lib.fileset.unions [
        ../templates/connector-tracker
        ../templates/connector-driver
      ];
    };
    NEW_CONNECTOR_SDK_VERSION = sdkVersion;
    NEW_CONNECTOR_CORE_NAMES = toString (map (r: r.name) (import ../lib/backends/default.nix));
    NEW_CONNECTOR_CORE_DRIVER_NAMES = toString (
      builtins.attrNames (import ../lib/drivers/default.nix { inherit (pkgs) lib; }).entries
    );
  };
  text = builtins.readFile ./new-connector.sh;
}
