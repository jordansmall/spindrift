# Dev wrapper (issue #6769): `nix run .#connector-dev -- <connector> <nix-args>`
# runs nix in a Connector subflake with core overridden to this checkout.
# Dogfood-only dev tooling, not a consumer-facing option.
# `nix` is deliberately not a runtimeInput, as in gate.nix: drive the caller's
# own nix. `git` is one for `git rev-parse --show-toplevel`.
{ pkgs }:
pkgs.writeShellApplication {
  name = "connector-dev";
  runtimeInputs = [
    pkgs.git
    pkgs.coreutils
  ];
  text = builtins.readFile ./connector-dev.sh;
}
