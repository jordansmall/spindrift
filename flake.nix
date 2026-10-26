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
            # Re-importable from any source tree (issue #5154): the
            # docs-edit-isolation check evaluates the checkset off a copy of
            # the repo with docs files removed and compares drvPaths.
            fixturesAt =
              root:
              import (root + "/nix/fixtures.nix") {
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
            checksAt =
              root:
              import (root + "/nix/checks") {
                inherit
                  pkgs
                  config
                  system
                  flake-parts
                  checksAt
                  ;
                baselineChecks = checksResult.checks;
                fixtures = fixturesAt root;
                nixpkgs = nixpkgsShared;
              };
            fixtures = fixturesAt ./.;
            checksResult = checksAt ./.;
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
              forge.repoSlug = dogfoodDefaults.defaults.repoSlug;
              git.merge.policy = dogfoodDefaults.defaults.mergeMode;
              git.merge.guardPaths = dogfoodDefaults.defaults.mergeGuardPaths;
              forge.boxAccess = dogfoodDefaults.defaults.boxForgeAndIssueAccess;
              agents.format.enable = dogfoodDefaults.defaults.autoFormat;
              agents.lint.enable = dogfoodDefaults.defaults.autoLint;
              # Driver map (ADR 0064, issue #5054); see nix/dogfood-defaults.nix.
              agents.defaultDriver = "claude";
              agents.drivers.claude = {
                models.roster = dogfoodDefaults.roster;
                models.default = dogfoodDefaults.defaults.model;
                models.effort = dogfoodDefaults.defaults.effort;
                bashTimeoutMs = dogfoodDefaults.defaults.driverBashTimeoutMs;
              };
              agents.drivers.opencode = {
                models.default = dogfoodDefaults.opencode.model;
                models.roster = dogfoodDefaults.opencode.roster;
                bashTimeoutMs = dogfoodDefaults.defaults.driverBashTimeoutMs;
              };
              agents.drivers.pi = {
                models.default = dogfoodDefaults.pi.model;
                models.roster = dogfoodDefaults.pi.roster;
              };
              dispatch.maxParallel = dogfoodDefaults.defaults.maxParallel;
              dispatch.butler.chores = dogfoodDefaults.defaults.butlerChores;
              dispatch.butler.maxPromotionsPerDay = dogfoodDefaults.defaults.butlerMaxPromotionsPerDay;
              dispatch.butler.maxPatchesPerDay = dogfoodDefaults.defaults.butlerMaxPatchesPerDay;
              dispatch.butler.every = dogfoodDefaults.defaults.butlerEvery;
              dispatch.butler.maxSweepsPerDay = dogfoodDefaults.defaults.butlerMaxSweepsPerDay;
              dispatch.butler.maxFindingsPerDay = dogfoodDefaults.defaults.butlerMaxFindingsPerDay;
            };

            checks = checksResult.checks;

            # Marginal-cost eval probe (issue #5860); see nix/eval-probe.nix.
            # Under legacyPackages so `nix flake check` leaves its contents
            # alone.
            legacyPackages.evalProbe = import ./nix/eval-probe.nix {
              nixpkgs = nixpkgsShared;
              inherit system;
            };

            # The inbox checks as an attrset (issue #5862), outside `packages`
            # because `nix flake check` requires derivations there.
            legacyPackages.checks-inbox-set = checksResult.checks-inbox-set;

            packages = {
              # Scoped in-box gate (issue #581): the source-level checks only,
              # skipping the OCI-image realization the full `checks` set above
              # still covers for CI.
              checks-inbox = checksResult.checks-inbox;
              # CI's docs-only lane (issue #5155): the checks that read docs
              # files.
              checks-docs = checksResult.checks-docs;
              # The fixtures directory the Go seam tests build on demand
              # (issue #4280).
              seam-fixtures = fixtures.seamFixtures;
            }
            # A bwrap harness bakes FLAKE_IMAGE_ATTR as
            # `.#packages.<system>.<imageAttrName>` (issue #2672), agent-closure
            # here, so the launcher's freshness Probe needs agent-closure at top
            # level, not only on fixtures.dogfoodBwrapHarness. The guard matches
            # lib/mkHarness.nix:
            # on aarch64-darwin that harness has none and the access would throw.
            // pkgs.lib.optionalAttrs (fixtures.dogfoodBwrapHarness.packages ? agent-closure) {
              agent-closure = fixtures.dogfoodBwrapHarness.packages.agent-closure;
            }
            # The pi twin's closure (issue #6064). Its FLAKE_IMAGE_ATTR names
            # agent-closure-pi, not the claude closure above, so freshness
            # needs its own top-level entry; same guard as agent-closure.
            // pkgs.lib.optionalAttrs (fixtures.dogfoodBwrapPiHarness.packages ? agent-closure-pi) {
              agent-closure-pi = fixtures.dogfoodBwrapPiHarness.packages.agent-closure-pi;
            };

            apps = {
              # Repo-internal dev tooling, not for consumers (issue #402). It
              # shares lib/renderers.nix with nix/checks/schema-drift.nix, so a
              # generated artifact and its drift guard can never diverge.
              regen = {
                type = "app";
                program = "${import ./nix/regen.nix { inherit pkgs; }}/bin/regen";
              };

              # Evaluates and builds the inbox checks in parallel (issue #5862).
              gate = {
                type = "app";
                program = "${import ./nix/gate.nix { inherit pkgs system; }}/bin/gate";
              };

              # Rewrites the opencode session-store fixture from the baked
              # opencode (issue #5492); nix/checks/opencode-session-schema.nix
              # pins it to the derivation built here.
              regen-opencode-schema = {
                type = "app";
                program = "${import ./nix/regen-opencode-schema.nix { inherit pkgs; }}/bin/regen-opencode-schema";
              };

              # The pre-CLI interactive setup (ADR 0027), standalone from the
              # Consumer-facing lib/mkHarness.nix pipeline.
              quickstart = {
                type = "app";
                program = "${import ./nix/quickstart.nix { inherit pkgs; }}/bin/quickstart";
              };

              # The read-only Dashboard (ADR 0060): standalone, no Consumer
              # input document, so it stays outside lib/mkHarness.nix.
              dashboard = {
                type = "app";
                program = "${import ./nix/dashboard.nix { inherit pkgs; }}/bin/dashboard";
              };
            }
            # regen-goldens runs the prompt-assembly golden test in update mode
            # (issue #2951), and nix/checks/promptassembly.nix pins it to the
            # derivation built here. It needs fixtures.seamFixtures, rendered
            # off lib/mkHarness.nix's batsHarness, which builds only on Linux, so the
            # guard is `pkgs.stdenv.isLinux`, not a sibling's `? agent-closure`.
            // pkgs.lib.optionalAttrs pkgs.stdenv.isLinux {
              regen-goldens = {
                type = "app";
                program = "${import ./nix/regen-goldens.nix { inherit pkgs fixtures; }}/bin/regen-goldens";
              };
            }
            # dogfood-bwrap is the apps.default CLI built off
            # fixtures.dogfoodBwrapHarness (issue #2672), so DAEMON_APP /
            # dogfood-bwrap-daemon can drive a bwrap Box without touching
            # apps.default or the podman module config. Without the guard it
            # resolves on aarch64-darwin and fails opaquely once the launcher
            # realizes agent-closure, not up front.
            // pkgs.lib.optionalAttrs (fixtures.dogfoodBwrapHarness.packages ? agent-closure) {
              dogfood-bwrap = fixtures.dogfoodBwrapHarness.apps.default;
              # Its daemon counterpart (issue #3538): dogfoodBwrapHarness's own
              # daemonApp default points back at `.#dogfood-bwrap`, so this
              # drives a bwrap Box rather than the podman one apps.daemon
              # (off the default harness) would.
              dogfood-bwrap-daemon = fixtures.dogfoodBwrapHarness.apps.daemon;
            }
            # The pi Driver in a bwrap Box (issue #6064): the bwrap twin above
            # with only Driver, model/Roster, image attr and daemon app names
            # changed. Guarded on its own closure, which builds only where
            # dogfoodBwrapHarness's does.
            // pkgs.lib.optionalAttrs (fixtures.dogfoodBwrapPiHarness.packages ? agent-closure-pi) {
              dogfood-bwrap-pi = fixtures.dogfoodBwrapPiHarness.apps.default;
              dogfood-bwrap-pi-daemon = fixtures.dogfoodBwrapPiHarness.apps.daemon;
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
                # network isolation path (issue #2666). The egress test also
                # needs iproute2's `ip` to inspect the namespace's tap
                # interface and routes, and the in-box nix tests need sqlite3
                # for the store-db snapshot. Without these, the tests that
                # need them skip rather than exercise anything real, unless
                # SPINDRIFT_REQUIRE_BWRAP is set, as CI sets it.
                ++ pkgs.lib.optionals pkgs.stdenv.isLinux [
                  pkgs.bubblewrap
                  pkgs.passt
                  pkgs.iproute2
                  pkgs.sqlite
                ];
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
