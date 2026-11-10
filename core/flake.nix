{
  description = "spindrift core — the harness library and flake-parts module, without batteries or dogfood";

  # Declared because core's contract is built on them (Consumers pass their
  # own); the entry point itself consumes neither.
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts.url = "github:hercules-ci/flake-parts";
  };

  outputs = _: import ./.;
}
