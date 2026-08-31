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
    # More upstream skills baked into the dogfood Box the same way as caveman
    # (issue #486): Matt Pocock's tdd + to-issues live in one repo, jordansmall's
    # commit in another. Pinned via flake.lock; neither is a flake, so
    # `flake = false` — dogfood-skills.nix reads the SKILL.md content directly.
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
      # instantiation per system instead of paying its own fixed-point
      # evaluation (lib/nixpkgs-shared.nix) — the checkset alone makes ~100
      # such calls per `nix flake check`. Overriding `inputs.nixpkgs` for
      # mkFlake extends the sharing to the flake-parts shim's own mkHarness
      # call (lib/flakeModule.nix falls back to `inputs.nixpkgs`).
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

        # Dogfood the declarative surface: our own packages/apps are produced by
        # the flake-parts shim, not a direct mkHarness call.
        imports = [ ./lib/flakeModule.nix ];

        # The engine, exposed for Consumer flakes to import.
        flake.lib.mkHarness = import ./lib/mkHarness.nix;

        # The roster helpers (issue #2560), exposed the same way -- a Consumer
        # calls `spindrift.lib.rosterLib { inherit lib; }` to get
        # `{ normalizeRoster; dropOptedOut; defaultRoster; }`
        # (normalizeRosterResult stays internal test-support machinery, not
        # exported here).
        flake.lib.rosterLib =
          args:
          let
            roster = import ./lib/roster.nix args;
          in
          {
            inherit (roster) normalizeRoster dropOptedOut defaultRoster;
          };

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
            # The dogfood's real packages/apps flow through the flake-parts shim,
            # fed from the same leaf values as fixtures.nix's direct mirror
            # (nix/dogfood-defaults.nix, issue #459).
            spindrift = {
              # ADR 0037 Pass 1 (issue #2179): the dogfood configures every knob
              # at its new domain-tree path, off the deprecated `settings.*` and
              # flat structural paths, so spindrift's own flake never trips the
              # eval-time deprecation warnings it now emits for consumers.
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
              # Scoped in-box gate (issue #581): `nix build .#checks-inbox`
              # builds the source-level checks only, skipping the OCI-image
              # realization the full `checks` set above still covers for CI.
              checks-inbox = checksResult.checks-inbox;
            }
            # lib/preambles.nix bakes FLAKE_IMAGE_ATTR as the fixed structural
            # path `.#packages.<system>.agent-closure` into every bwrap run
            # document (issue #2672 review fix), so cmd/launcher's freshness
            # Probe needs a real `agent-closure` at THIS flake's top-level
            # `packages` output -- not merely on fixtures.dogfoodBwrapHarness's
            # own local `packages` attrset. Re-export the bwrap dogfood
            # harness's own agent-closure output here. Guarded the same way
            # lib/mkHarness.nix guards its own `packages.agent-closure` output
            # (the `isLinux && runtime == "bwrap"` optionalAttrs guard on its
            # `packages` attrset): on aarch64-darwin the harness's
            # packages set never has agent-closure, so an unconditional
            # access here would throw during `nix flake show`/`nix flake
            # check` on that system.
            // pkgs.lib.optionalAttrs (fixtures.dogfoodBwrapHarness.packages ? agent-closure) {
              agent-closure = fixtures.dogfoodBwrapHarness.packages.agent-closure;
            };

            apps = {
              # Repo-internal dev tooling, not consumer surface (issue #402):
              # `nix run .#regen` regenerates every schema-generated artifact
              # that nix/checks/schema-drift.nix drift-guards, sharing
              # lib/renderers.nix with those checks so the two can never diverge.
              regen = {
                type = "app";
                program = "${import ./nix/regen.nix { inherit pkgs; }}/bin/regen";
              };

              # `nix run github:jordansmall/spindrift#quickstart` (ADR 0027):
              # the pre-CLI interactive scaffolder. Standalone from the
              # Consumer-facing lib/mkHarness.nix pipeline — see nix/quickstart.nix.
              quickstart = {
                type = "app";
                program = "${import ./nix/quickstart.nix { inherit pkgs; }}/bin/quickstart";
              };
            }
            # `nix run .#regen-goldens` (issue #2951): update-mode run of the
            # prompt-assembly parity suite, sharing nix/parity-env.nix's env
            # wiring with the promptassembly-parity check itself. That env
            # wiring pulls in fixtures.batsHarness.internals.driverExecBin,
            # which lib/mkHarness.nix only builds for the Linux twin of the
            # host system -- guarded directly on `pkgs.stdenv.isLinux` (the
            # same primitive lib/mkHarness.nix's own isLinux gate is built
            # from), not on a sibling harness's unrelated `packages ?
            # agent-closure` existence check, so a fixtures refactor that
            # changes what that other predicate tracks can't silently drop
            # this app too. nix/checks/promptassembly.nix's
            # regen-goldens-app-wiring check pins this app to the exact
            # derivation built here, the same failure mode
            # dogfood-bwrap-app-wiring below guards for `dogfood-bwrap`.
            // pkgs.lib.optionalAttrs pkgs.stdenv.isLinux {
              regen-goldens = {
                type = "app";
                program = "${import ./nix/regen-goldens.nix { inherit pkgs fixtures; }}/bin/regen-goldens";
              };
            }
            # `nix run .#dogfood-bwrap` (issue #2672): the same spindrift CLI
            # as apps.default, built off fixtures.dogfoodBwrapHarness instead
            # of the podman-configured `harness` — lets dogfood.sh drive a
            # bwrap Box without touching apps.default/the podman module config.
            # Guarded by the same `packages ? agent-closure` predicate as
            # packages.agent-closure above (both true only under
            # isLinux && runtime == "bwrap"): without it, `nix run
            # .#dogfood-bwrap` would resolve on aarch64-darwin and only fail
            # opaquely once the launcher tried to realize
            # packages.<system>.agent-closure, instead of a clear "flake
            # does not provide" error at resolution time (review finding).
            // pkgs.lib.optionalAttrs (fixtures.dogfoodBwrapHarness.packages ? agent-closure) {
              dogfood-bwrap = fixtures.dogfoodBwrapHarness.apps.default;
            };

            # For hacking ON the harness itself (host-side).
            # spindrift CLI is included so `nix develop` → `spindrift dispatch` works.
            devShells.default = pkgs.mkShell {
              packages = [
                pkgs.git
                pkgs.gh
                pkgs.jq
                pkgs.go
                config.packages.spindrift
              ]
              # bubblewrap only builds on Linux; the runner integration tests
              # (go test -tags=integration ./cmd/launcher/internal/runner/...,
              # issue #576) need it on PATH to exercise a real sandbox. passt
              # (provides the `pasta` binary) is the same story for the
              # pasta-wrapped default network isolation path (issue #2666) --
              # without it those integration tests skip rather than exercise
              # anything real.
              ++ pkgs.lib.optionals pkgs.stdenv.isLinux [
                pkgs.bubblewrap
                pkgs.passt
              ];
              # `dogfood-stop`: ask a running ./dogfood.sh to exit after its current
              # wave (see the USR1/TERM trap in dogfood.sh) instead of Ctrl-C, which
              # would abort the wave mid-flight.
              shellHook = ''
                alias dogfood-stop='pid=$(cat "$(git rev-parse --show-toplevel 2>/dev/null)/.spindrift/dogfood.pid" 2>/dev/null) && kill -USR1 "$pid" && echo "dogfood: will stop after the current wave (pid $pid)" || echo "dogfood: no running loop (.spindrift/dogfood.pid not found)"'
              '';
            };
          };
      };
}
