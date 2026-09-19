{
  description = "spindrift — headless coding agents in disposable, nix-built containers, one per issue";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts.url = "github:hercules-ci/flake-parts";
    # Upstream caveman skill (issue #486), pinned via flake.lock rather than a
    # floating fetch. Not a flake itself, so `flake = false` and spindrift reads
    # its skill content directly from the fetched source tree.
    caveman = {
      url = "github:juliusbrussee/caveman";
      flake = false;
    };
    # More upstream skills baked into the dogfood Box the same way as caveman
    # (issue #486). Neither is a flake, so `flake = false` and
    # dogfood-skills.nix reads the SKILL.md content directly.
    matt-skills = {
      url = "github:mattpocock/skills/v1.1.0";
      flake = false;
    };
    jordan-skills = {
      url = "github:jordansmall/skills";
      flake = false;
    };
  };

  outputs =
    inputs@{
      flake-parts,
      nixpkgs,
      caveman,
      matt-skills,
      jordan-skills,
      ...
    }:
    let
      # Every mkHarness call handed this wrapped input shares one lazy nixpkgs
      # instantiation per system instead of paying its own fixed-point evaluation
      # (lib/nixpkgs-shared.nix); the checkset alone makes ~100 such calls per
      # `nix flake check`. Overriding `inputs.nixpkgs` for mkFlake extends the
      # sharing to lib/flakeModule.nix, which falls back to `inputs.nixpkgs`.
      nixpkgsShared = (import ./lib/nixpkgs-shared.nix).withSharedInstances nixpkgs;
    in
    flake-parts.lib.mkFlake
      {
        inputs = inputs // {
          nixpkgs = nixpkgsShared;
        };
      }
      {
        systems = [
          "aarch64-darwin"
          "aarch64-linux"
          "x86_64-linux"
        ];

        # Dogfood the declarative options: our own packages and apps come from
        # the flake-parts shim, not a direct mkHarness call.
        imports = [ ./lib/flakeModule.nix ];

        flake.lib.mkHarness = import ./lib/mkHarness.nix;

        # Roster helpers (issue #2560). normalizeRosterResult stays internal
        # test-support machinery, so it is deliberately not exported here.
        flake.lib.rosterLib =
          args:
          let
            roster = import ./lib/roster.nix args;
          in
          {
            inherit (roster) normalizeRoster dropOptedOut defaultRoster;
          };

        # The flake-parts shim, for Consumer flakes that want the declarative
        # options (ADR 0001).
        flake.flakeModules.default = ./lib/flakeModule.nix;

        # The dogfood above consumes this same templates/default toolchain and
        # prompt, so an edit here changes both.
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
            dogfoodDefaults = import ./nix/dogfood-defaults.nix {
              inherit system;
              lib = pkgs.lib;
            };
            dogfoodSkills = import ./nix/dogfood-skills.nix {
              inherit caveman matt-skills jordan-skills;
            };
            fixtures = import ./nix/fixtures.nix {
              inherit
                pkgs
                system
                flake-parts
                revision
                caveman
                matt-skills
                jordan-skills
                ;
              nixpkgs = nixpkgsShared;
            };
            checksResult = import ./nix/checks {
              inherit
                pkgs
                config
                fixtures
                system
                flake-parts
                ;
              nixpkgs = nixpkgsShared;
            };
          in
          {
            # The same leaf values feed fixtures.nix's direct mirror
            # (nix/dogfood-defaults.nix, issue #459), so the two stay in sync.
            spindrift = {
              # ADR 0037 Pass 1 (issue #2179): the dogfood sets every knob at its
              # domain-tree path, off the deprecated `settings.*` and flat
              # structural paths, so spindrift's own flake never trips the
              # eval-time deprecation warnings it emits for consumers.
              infra.image.prefetch = dogfoodDefaults.prefetch;
              infra.image.packages = dogfoodDefaults.packages;
              infra.image.extraClosures = dogfoodDefaults.extraClosures;
              infra.nix.storeWritable = dogfoodDefaults.nixStoreWritable;
              infra.limits.memory = dogfoodDefaults.defaults.memoryLimit;
              agents.skills = dogfoodSkills;
              git.merge.policy = dogfoodDefaults.defaults.mergeMode;
              forge.boxAccess = dogfoodDefaults.defaults.boxForgeAndIssueAccess;
              agents.format.enable = dogfoodDefaults.defaults.autoFormat;
              agents.lint.enable = dogfoodDefaults.defaults.autoLint;
              agents.models.roster = dogfoodDefaults.roster;
            };

            checks = checksResult.checks;

            packages = {
              # Scoped in-box gate (issue #581): the source-level checks only,
              # skipping the OCI-image realization the full `checks` set above
              # still covers for CI.
              checks-inbox = checksResult.checks-inbox;
            }
            # lib/preambles.nix bakes FLAKE_IMAGE_ATTR as the fixed path
            # `.#packages.<system>.agent-closure` (issue #2672), so the launcher's
            # freshness Probe needs agent-closure here at top level, not only on
            # fixtures.dogfoodBwrapHarness. The guard matches lib/mkHarness.nix:
            # on aarch64-darwin that harness has none and the access would throw.
            // pkgs.lib.optionalAttrs (fixtures.dogfoodBwrapHarness.packages ? agent-closure) {
              agent-closure = fixtures.dogfoodBwrapHarness.packages.agent-closure;
            };

            apps = {
              # Repo-internal dev tooling, not for consumers (issue #402). It
              # shares lib/renderers.nix with nix/checks/schema-drift.nix, so a
              # generated artifact and its drift guard can never diverge.
              regen = {
                type = "app";
                program = "${import ./nix/regen.nix { inherit pkgs; }}/bin/regen";
              };

              # The pre-CLI interactive setup (ADR 0027), standalone from the
              # Consumer-facing lib/mkHarness.nix pipeline.
              quickstart = {
                type = "app";
                program = "${import ./nix/quickstart.nix { inherit pkgs; }}/bin/quickstart";
              };
            }
            # regen-goldens runs the prompt-assembly parity suite in update mode
            # (issue #2951), and nix/checks/promptassembly.nix pins it to the
            # derivation built here. Its shared nix/parity-env.nix wiring needs
            # driverExecBin, which lib/mkHarness.nix builds only on Linux, so the
            # guard is `pkgs.stdenv.isLinux`, not a sibling's `? agent-closure`.
            // pkgs.lib.optionalAttrs pkgs.stdenv.isLinux {
              regen-goldens = {
                type = "app";
                program = "${import ./nix/regen-goldens.nix { inherit pkgs fixtures; }}/bin/regen-goldens";
              };
            }
            # dogfood-bwrap is the apps.default CLI built off
            # fixtures.dogfoodBwrapHarness (issue #2672), so dogfood.sh can drive
            # a bwrap Box without touching apps.default or the podman module
            # config. Without the guard it resolves on aarch64-darwin and fails
            # opaquely once the launcher realizes agent-closure, not up front.
            // pkgs.lib.optionalAttrs (fixtures.dogfoodBwrapHarness.packages ? agent-closure) {
              dogfood-bwrap = fixtures.dogfoodBwrapHarness.apps.default;
            };

            devShells = {
              # For hacking on the harness itself, host-side.
              default = pkgs.mkShell {
                packages = [
                  pkgs.git
                  pkgs.gh
                  pkgs.jq
                  pkgs.go
                  config.packages.spindrift
                ]
                # bubblewrap only builds on Linux. The runner integration tests
                # (issue #576) need it on PATH to exercise a real sandbox, and
                # need passt's `pasta` binary for the pasta-wrapped default
                # network isolation path (issue #2666). Without both, those
                # tests skip rather than exercise anything real.
                ++ pkgs.lib.optionals pkgs.stdenv.isLinux [
                  pkgs.bubblewrap
                  pkgs.passt
                ];
                # `dogfood-stop` signals a running ./dogfood.sh to wind down
                # gracefully, instead of Ctrl-C, which would abort the wave
                # mid-flight. What that costs depends on state the alias cannot
                # see (in-flight launcher or not, continuous dispatch or not),
                # so it reports only the signal it sent and leaves the
                # consequence to the loop's own output.
                shellHook = ''
                  alias dogfood-stop='pid=$(cat "$(git rev-parse --show-toplevel 2>/dev/null)/.spindrift/dogfood.pid" 2>/dev/null) && kill -USR1 "$pid" && echo "dogfood: stop requested (loop pid $pid) — the loop prints what it does next" || echo "dogfood: no running loop (.spindrift/dogfood.pid not found)"'
                '';
              };
            }
            # For driving the bwrap dogfood harness directly: the bwrap-baked CLI
            # plus the host binaries the Go launcher execs from ambient PATH,
            # bwrap and pasta (issue #2666), which the generic every-runtime CLI
            # wrapper's own runtimeInputs deliberately don't pin. Linux-only,
            # guarded like apps.dogfood-bwrap above and for the same reason.
            // pkgs.lib.optionalAttrs (fixtures.dogfoodBwrapHarness.packages ? agent-closure) {
              bwrap = pkgs.mkShell {
                packages = [
                  fixtures.dogfoodBwrapHarness.packages.spindrift
                  pkgs.bubblewrap
                  pkgs.passt
                ];
              };
            };
          };
      };
}
