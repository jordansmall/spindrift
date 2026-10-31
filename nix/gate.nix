# Parallel-eval gate (issue #5862): `nix run .#gate` evaluates the inbox checks
# with nix-eval-jobs and builds each derivation as it arrives, so builds overlap
# eval. Dogfood-only dev tooling, not a consumer-facing option.

# `nix` is deliberately not a runtimeInput: the gate must drive the Box's own
# nix, whose nix.conf pins max-jobs and cores (lib/image.nix nixConfigFile).
{ pkgs, system }:
pkgs.writeShellApplication {
  name = "gate";
  runtimeInputs = [
    pkgs.nix-eval-jobs
    pkgs.jq
    pkgs.coreutils
    pkgs.findutils
    pkgs.gnused
    pkgs.gnugrep
    pkgs.util-linux
  ];
  text = ''
    : "''${GATE_INSTALLABLE:=.#legacyPackages.${system}.checks-inbox-set}"
    export GATE_INSTALLABLE
    ${builtins.readFile ./gate.sh}
  '';
}
