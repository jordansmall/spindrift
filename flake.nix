{
  description = "spindrift — run headless Claude Code agents in disposable, nix-built containers, one per GitHub issue";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts.url = "github:hercules-ci/flake-parts";
  };

  outputs =
    inputs@{
      flake-parts,
      nixpkgs,
      ...
    }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      systems = [
        "aarch64-darwin"
        "x86_64-darwin"
        "aarch64-linux"
        "x86_64-linux"
      ];

      # Dogfood the declarative surface: our own packages/apps are produced by
      # the flake-parts shim, not a direct mkHarness call.
      imports = [ ./lib/flakeModule.nix ];

      # The engine, exposed for Consumer flakes to import.
      flake.lib.mkHarness = import ./lib/mkHarness.nix;

      # The flake-parts shim, exposed for Consumer flakes that want the
      # declarative option surface (ADR 0001).
      flake.flakeModules.default = ./lib/flakeModule.nix;

      # A ready-to-edit consumer starter (`nix flake init -t
      # github:jordansmall/spindrift`). This is spindrift's own scaffold — the
      # dogfood above consumes the very same templates/default toolchain and
      # prompt.
      flake.templates.default = {
        path = ./templates/default;
        description = "spindrift consumer starter: flake + prompts + toolchain + harness.env.example";
      };

      perSystem =
        {
          system,
          pkgs,
          config,
          ...
        }:
        let
          revision = inputs.self.shortRev or inputs.self.dirtyShortRev or "unknown";
          fixtures = import ./nix/fixtures.nix { inherit pkgs nixpkgs system flake-parts revision; };
        in
        {
          # The dogfood's real packages/apps flow through the flake-parts shim.
          spindrift = {
            prefetch = "go mod download || true";
            packages = p: [ p.go ];
            settings.branches.mergeMode = "immediate";
          };

          checks = import ./nix/checks.nix {
            inherit pkgs config fixtures nixpkgs system flake-parts;
          };

          # For hacking ON the harness itself (host-side).
          # spindrift CLI is included so `nix develop` → `spindrift dispatch` works.
          devShells.default = pkgs.mkShell {
            packages = [
              pkgs.git
              pkgs.gh
              pkgs.jq
              config.packages.spindrift
            ];
          };
        };
    };
}
