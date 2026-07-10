{
  description = "spindrift — run headless Claude Code agents in disposable, nix-built containers, one per GitHub issue";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts.url = "github:hercules-ci/flake-parts";
    # Upstream caveman skill (issue #486), pinned via flake.lock rather than
    # a floating fetch. Not a flake itself, so `flake = false` — spindrift
    # reads its skill content directly from the fetched source tree.
    caveman = {
      url = "github:juliusbrussee/caveman";
      flake = false;
    };
  };

  outputs =
    inputs@{
      flake-parts,
      nixpkgs,
      caveman,
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
          dogfoodDefaults = import ./nix/dogfood-defaults.nix;
          dogfoodSkills = import ./nix/dogfood-skills.nix { inherit pkgs caveman; };
          fixtures = import ./nix/fixtures.nix {
            inherit
              pkgs
              nixpkgs
              system
              flake-parts
              revision
              caveman
              ;
          };
        in
        {
          # The dogfood's real packages/apps flow through the flake-parts shim,
          # fed from the same leaf values as fixtures.nix's direct mirror
          # (nix/dogfood-defaults.nix, issue #459).
          spindrift = {
            inherit (dogfoodDefaults)
              prefetch
              packages
              nixStoreWritable
              extraClosures
              ;
            skills = dogfoodSkills;
            settings.branches.mergeMode = dogfoodDefaults.defaults.mergeMode;
            settings.promptSkillIteration.autoFormat = dogfoodDefaults.defaults.autoFormat;
            settings.promptSkillIteration.autoLint = dogfoodDefaults.defaults.autoLint;
          };

          checks = import ./nix/checks {
            inherit
              pkgs
              config
              fixtures
              nixpkgs
              system
              flake-parts
              ;
          };

          # Repo-internal dev tooling, not consumer surface (issue #402):
          # `nix run .#regen` regenerates every schema-generated artifact that
          # nix/checks/schema-drift.nix drift-guards, sharing lib/renderers.nix
          # with those checks so the two can never diverge.
          apps.regen = {
            type = "app";
            program = "${import ./nix/regen.nix { inherit pkgs; }}/bin/regen";
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
            # `dogfood-stop`: ask a running ./dogfood.sh to exit after its current
            # wave (see the USR1/TERM trap in dogfood.sh) instead of Ctrl-C, which
            # would abort the wave mid-flight.
            shellHook = ''
              alias dogfood-stop='pid=$(cat "$(git rev-parse --show-toplevel 2>/dev/null)/.dogfood.pid" 2>/dev/null) && kill -USR1 "$pid" && echo "dogfood: will stop after the current wave (pid $pid)" || echo "dogfood: no running loop (.dogfood.pid not found)"'
            '';
          };
        };
    };
}
