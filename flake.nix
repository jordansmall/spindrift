{
  description = "spindrift — run headless Claude Code agents in disposable, nix-built containers, one per GitHub issue";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts.url = "github:hercules-ci/flake-parts";
    rust-overlay = {
      url = "github:oxalica/rust-overlay";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs =
    inputs@{
      flake-parts,
      nixpkgs,
      rust-overlay,
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
          # The launchers pin the real `gh` via runtimeInputs, which would shadow
          # a PATH-injected fake; so the bats-driven harnesses below overlay `gh`
          # with the recording fake, keeping the suite offline. podman/docker stay
          # unpinned host installs, so their fakes still resolve through PATH.
          ghFakeOverlay = _final: prev: {
            gh = prev.runCommand "fake-gh" { } ''
              mkdir -p $out/bin
              # The launcher execs this by path, so rewrite the fake's
              # `#!/usr/bin/env bash` to the store bash — a sandboxed Linux
              # build has no /usr/bin/env.
              substitute ${./tests/fakes/gh} $out/bin/gh \
                --replace '#!/usr/bin/env bash' "#!${prev.bash}/bin/bash"
              chmod +x $out/bin/gh
            '';
          };

          # A plain harness whose launcher commands drive the bats suite: default
          # run knobs, a trivial toolchain, and the fake `gh` overlaid in.
          batsHarness = import ./lib/mkHarness.nix {
            inherit nixpkgs system;
            overlays = [ ghFakeOverlay ];
            packages = p: [ p.hello ];
          };

          # The rust dogfood as a direct call, mirroring the `spindrift = { ... }`
          # module config below. Kept so the equivalence check can prove the
          # module and direct paths yield byte-identical outputs.
          harness = import ./lib/mkHarness.nix {
            inherit nixpkgs system;
            overlays = [ (import rust-overlay) ];
            config.allowUnfree = true;
            prefetch = "cargo fetch --locked || true";
            packages =
              p:
              [ (p.rust-bin.fromRustupToolchainFile ./templates/default/toolchain/rust-toolchain.toml) ]
              ++ import ./templates/default/toolchain/packages.nix { pkgs = p; };
          };

          # A minimal, non-Rust consumer, proving the engine bakes an arbitrary
          # `packages` set with no language-specific machinery. Kept off the
          # public outputs — the checks introspect it at eval time only.
          nonRustHarness = import ./lib/mkHarness.nix {
            inherit nixpkgs system;
            packages = p: [ p.hello ];
          };

          # The lean/no-nix escape hatch: a Consumer that opts out of the
          # nix-in-box default for the smallest possible image. Eval-only.
          leanHarness = import ./lib/mkHarness.nix {
            inherit nixpkgs system;
            nixInBox = false;
            packages = p: [ p.hello ];
          };

          # Exercise the run knobs (#3): non-default baked `defaults` and a
          # docker `runtime`. Eval-only, consumed by the checks below.
          customHarness = import ./lib/mkHarness.nix {
            inherit nixpkgs system;
            overlays = [ ghFakeOverlay ];
            defaults = {
              label = "custom-label";
              baseBranch = "develop";
              maxParallel = 5;
              branchPrefix = "bot/";
              inProgressLabel = "custom-wip";
              failedLabel = "custom-broken";
              scoutModel = "custom-scout";
              reviewModel = "custom-reviewer";
              completeLabel = "custom-done";
            };
            packages = p: [ p.hello ];
          };

          dockerHarness = import ./lib/mkHarness.nix {
            inherit nixpkgs system;
            overlays = [ ghFakeOverlay ];
            runtime = "docker";
            packages = p: [ p.hello ];
          };

          # The daemonless bubblewrap runner fixture (issue #54): exercises the
          # bwrap build/run path through the bats suite.
          bwrapHarness = import ./lib/mkHarness.nix {
            inherit nixpkgs system;
            overlays = [ ghFakeOverlay ];
            runtime = "bwrap";
            packages = p: [ p.hello ];
          };

          # A harness whose baked runtime is never on PATH, so `build`'s
          # container fallback is unavailable — used to exercise the
          # both-paths-impossible error (the host build is faked to fail too).
          noRuntimeHarness = import ./lib/mkHarness.nix {
            inherit nixpkgs system;
            overlays = [ ghFakeOverlay ];
            runtime = "no-such-runtime";
            packages = p: [ p.hello ];
          };

          # A Consumer-configured prompt (#4): proves the `prompt` argument is
          # what gets rendered to the store path and flows through to the agent.
          # The per-issue placeholders are escaped so they survive to run time.
          promptHarness = import ./lib/mkHarness.nix {
            inherit nixpkgs system;
            prompt = ''
              CONFIGURED-PROMPT-MARKER
              Implement issue #''${ISSUE_NUMBER}: ''${ISSUE_TITLE} on ''${BRANCH}
            '';
            packages = p: [ p.hello ];
          };

          # A minimal flake-parts consumer fixture (#5), standing in for a
          # downstream flake. Evaluated in-repo (no separate lock / no network)
          # via a nested `mkFlake`; the checks compare its outputs to the
          # equivalent direct `mkHarness` call.
          minimalDirect = import ./lib/mkHarness.nix {
            inherit nixpkgs system;
            packages = p: [ p.hello ];
          };
          moduleConsumer =
            flake-parts.lib.mkFlake
              {
                inputs = {
                  inherit nixpkgs;
                  self = {
                    outPath = ./.;
                  };
                };
              }
              {
                systems = [ system ];
                imports = [ ./lib/flakeModule.nix ];
                perSystem.spindrift.packages = p: [ p.hello ];
              };
          consumerPkgs = moduleConsumer.packages.${system};

          # The `templates.default` starter, evaluated as a fixture (#6): call
          # its real `outputs` directly — no `nix flake init`, no network —
          # wiring `spindrift` to THIS checkout instead of the github input. The
          # full Linux image realise is verified out-of-band via the podman
          # builder; here we assert eval + the image store path resolving into
          # the launcher commands.
          templateOutputs = (import ./templates/default/flake.nix).outputs {
            inherit nixpkgs flake-parts rust-overlay;
            self = {
              outPath = ./templates/default;
            };
            spindrift = {
              flakeModules.default = ./lib/flakeModule.nix;
              lib.mkHarness = import ./lib/mkHarness.nix;
            };
          };
          templatePkgs = templateOutputs.packages.${system};
        in
        {
          # The dogfood's real packages/apps flow through the flake-parts shim.
          spindrift = {
            overlays = [ (import rust-overlay) ];
            config = {
              allowUnfree = true;
            };
            prefetch = "cargo fetch --locked || true";
            packages =
              p:
              [ (p.rust-bin.fromRustupToolchainFile ./templates/default/toolchain/rust-toolchain.toml) ]
              ++ import ./templates/default/toolchain/packages.nix { pkgs = p; };
          };

          checks = {
            shellcheck =
              pkgs.runCommand "shellcheck"
                {
                  nativeBuildInputs = [ pkgs.shellcheck ];
                }
                ''
                  # The launcher scripts are body fragments (they reference the
                  # nix-rendered preamble), so they are shellcheck'd by
                  # writeShellApplication at build time, not standalone here.
                  shellcheck --shell=bash \
                    ${./agent/entrypoint.sh} \
                    ${./tests/fakes/podman} \
                    ${./tests/fakes/docker} \
                    ${./tests/fakes/bwrap} \
                    ${./tests/fakes/gh} \
                    ${./tests/fakes/claude} \
                    ${./tests/fakes/nix} \
                    ${./tests/helper.bash}
                  touch $out
                '';

            # The bash layers under bats, driven entirely through fakes — no real
            # container, network, or LLM.
            bats =
              pkgs.runCommand "bats"
                {
                  nativeBuildInputs = [
                    pkgs.bats
                    pkgs.bash
                    pkgs.git
                    pkgs.gettext
                    pkgs.coreutils
                    pkgs.gnugrep
                    pkgs.gnused
                  ];
                  # The launcher commands under test overlay `gh` with the fake
                  # (batsHarness/customHarness/dockerHarness), since the real `gh`
                  # is pinned into their runtimeInputs PATH and would otherwise
                  # shadow a PATH-injected fake.
                  RUN_CMD = "${batsHarness.run}/bin/run";
                  BUILD_CMD = "${batsHarness.build}/bin/build";
                  BUILD_NO_RUNTIME_CMD = "${noRuntimeHarness.build}/bin/build";
                  CUSTOM_RUN_CMD = "${customHarness.run}/bin/run";
                  DOCKER_RUN_CMD = "${dockerHarness.run}/bin/run";
                  BWRAP_RUN_CMD = "${bwrapHarness.run}/bin/run";
                  BWRAP_BUILD_CMD = "${bwrapHarness.build}/bin/build";
                  IMAGE_PATH = batsHarness.imagePath;
                  ENTRYPOINT = ./agent/entrypoint.sh;
                  PROMPTS_DIR = ./templates/default/prompts;
                  # The baked default prompt dir the `run` command mounts, and a
                  # Consumer-configured one whose rendered content flows through
                  # to the stubbed agent (#4).
                  PROMPT_PATH = batsHarness.promptDir;
                  PROMPT_HARNESS_DIR = promptHarness.promptDir;
                }
                ''
                  export HOME="$TMPDIR/home"
                  mkdir -p "$HOME"
                  cp -r ${./tests} tests
                  chmod -R +w tests
                  # The fakes ship a `#!/usr/bin/env bash` shebang, which the
                  # host's launchers exec by path. A sandboxed Linux build has no
                  # /usr/bin/env, so rewrite them to the store bash before use.
                  for f in tests/fakes/*; do
                    substituteInPlace "$f" \
                      --replace '#!/usr/bin/env bash' "#!${pkgs.bash}/bin/bash"
                  done
                  export FAKES_DIR="$PWD/tests/fakes"
                  bats --print-output-on-failure tests/
                  touch $out
                '';

            # Pure-eval-style assertion: the image store path is substituted into
            # the generated commands and the placeholder is gone.
            mkharness-substitution = pkgs.runCommand "mkharness-substitution" { } ''
              buildCmd=${harness.build}/bin/build
              runCmd=${harness.run}/bin/run

              grep -q '${harness.imagePath}' "$buildCmd"
              grep -q '${harness.imagePath}' "$runCmd"
              ! grep -q '@imagePath@' "$buildCmd"
              ! grep -q '@imagePath@' "$runCmd"

              case '${harness.imagePath}' in
                /nix/store/*spindrift*) : ;;
                *) echo "unexpected image path: ${harness.imagePath}" >&2; exit 1 ;;
              esac
              touch $out
            '';

            # The declarative shim must produce byte-identical outputs to a
            # direct `mkHarness` call with the same inputs (#5). Compare store
            # paths at eval time — no Linux builder needed, since the launcher
            # commands are native and the image path is baked into them as text.
            flakemodule-equivalence =
              pkgs.runCommand "flakemodule-equivalence"
                {
                  moduleBuild = config.packages.build;
                  directBuild = harness.build;
                  moduleRun = config.packages.run;
                  directRun = harness.run;
                  imagePath = harness.imagePath;
                }
                ''
                  [ "$moduleBuild" = "$directBuild" ] \
                    || { echo "build mismatch: $moduleBuild != $directBuild" >&2; exit 1; }
                  [ "$moduleRun" = "$directRun" ] \
                    || { echo "run mismatch: $moduleRun != $directRun" >&2; exit 1; }
                  # The module bakes the very same (Linux) image store path.
                  grep -q "$imagePath" "$moduleRun/bin/run"
                  touch $out
                '';

            # A minimal flake-parts consumer that imports the shim evaluates and
            # yields the same outputs as the equivalent direct call (#5).
            flakemodule-fixture =
              pkgs.runCommand "flakemodule-fixture"
                {
                  fixtureBuild = consumerPkgs.build;
                  directBuild = minimalDirect.build;
                  fixtureRun = consumerPkgs.run;
                  directRun = minimalDirect.run;
                  imagePath = minimalDirect.imagePath;
                }
                ''
                  [ "$fixtureBuild" = "$directBuild" ] \
                    || { echo "build mismatch: $fixtureBuild != $directBuild" >&2; exit 1; }
                  [ "$fixtureRun" = "$directRun" ] \
                    || { echo "run mismatch: $fixtureRun != $directRun" >&2; exit 1; }
                  # The fixture's image store path matches the direct call's,
                  # asserted via the path baked into its `run` command.
                  grep -q "$imagePath" "$fixtureRun/bin/run"
                  touch $out
                '';

            # The `templates.default` starter (#6): its `build`/`run` commands
            # must have the Linux image store path substituted in, and — since
            # its config mirrors the dogfood's — be byte-identical to the direct
            # call. Eval-only; the Linux realise is done on the podman builder
            # against an instantiated copy.
            template-fixture =
              pkgs.runCommand "template-fixture"
                {
                  templateBuild = templatePkgs.build;
                  templateRun = templatePkgs.run;
                  directBuild = harness.build;
                  directRun = harness.run;
                  imagePath = harness.imagePath;
                }
                ''
                  buildCmd="$templateBuild/bin/build"
                  runCmd="$templateRun/bin/run"

                  ! grep -q '@imagePath@' "$buildCmd"
                  ! grep -q '@imagePath@' "$runCmd"
                  grep -q "$imagePath" "$buildCmd"
                  grep -q "$imagePath" "$runCmd"
                  case "$imagePath" in
                    /nix/store/*spindrift*) : ;;
                    *) echo "unexpected image path: $imagePath" >&2; exit 1 ;;
                  esac

                  # Same config as the dogfood ⇒ identical launcher commands.
                  [ "$templateBuild" = "$directBuild" ] \
                    || { echo "build mismatch: $templateBuild != $directBuild" >&2; exit 1; }
                  [ "$templateRun" = "$directRun" ] \
                    || { echo "run mismatch: $templateRun != $directRun" >&2; exit 1; }
                  touch $out
                '';

            # The configured `defaults` and `runtime` are baked into the
            # generated `run` command text (eval-only; no Linux builder).
            mkharness-defaults = pkgs.runCommand "mkharness-defaults" { } ''
              runCmd=${customHarness.run}/bin/run
              ! grep -q -- '@label@' "$runCmd"
              grep -q 'LABEL:-custom-label' "$runCmd"
              grep -q 'BASE_BRANCH:-develop' "$runCmd"
              grep -q 'MAX_PARALLEL:-5' "$runCmd"
              grep -q 'BRANCH_PREFIX:-bot/' "$runCmd"
              grep -q 'IN_PROGRESS_LABEL:-custom-wip' "$runCmd"
              grep -q 'FAILED_LABEL:-custom-broken' "$runCmd"
              grep -q 'SCOUT_MODEL:-custom-scout' "$runCmd"
              grep -q 'REVIEW_MODEL:-custom-reviewer' "$runCmd"
              grep -q 'COMPLETE_LABEL:-custom-done' "$runCmd"

              # Default COMPLETE_LABEL baked into a default harness.
              grep -q 'COMPLETE_LABEL:-agent-complete' ${harness.run}/bin/run

              # Default runtime is podman; the docker harness bakes docker.
              grep -q 'RUNTIME="podman"' ${harness.run}/bin/run
              grep -q 'RUNTIME="docker"' ${dockerHarness.run}/bin/run

              # bwrap harness bakes bwrap runtime and agent store paths; no OCI store paths.
              grep -q 'RUNTIME="bwrap"' ${bwrapHarness.run}/bin/run
              grep -q 'AGENT_FILES=' ${bwrapHarness.run}/bin/run
              grep -q 'AGENT_ENV=' ${bwrapHarness.run}/bin/run
              # IMAGE_ARCHIVE is not baked as a store path (empty-default guard is fine).
              ! grep -q 'IMAGE_ARCHIVE="/nix/store/' ${bwrapHarness.run}/bin/run
              grep -q 'AGENT_FILES_DRV=' ${bwrapHarness.build}/bin/build
              grep -q 'AGENT_ENV_DRV=' ${bwrapHarness.build}/bin/build
              ! grep -q 'IMAGE_DRV=' ${bwrapHarness.build}/bin/build
              touch $out
            '';

            # The configured `prompt` is rendered to a store-path directory and,
            # by default, baked into the image (see agentFiles) rather than
            # mounted — `run` only bind-mounts a dir under the
            # SPINDRIFT_PROMPT_DIR override. Eval/native only (the rendered
            # prompt dir is a host store path; the image bake is checked
            # Linux-side by prompt-baked-into-image below).
            # The conditional prompt mount is handled by the Go launcher binary,
            # so the bats suite verifies runtime behaviour rather than grepping
            # the wrapper's source.
            mkharness-prompt = pkgs.runCommand "mkharness-prompt" { } ''
              # The Consumer's prompt text is what lands in the rendered file.
              grep -q 'CONFIGURED-PROMPT-MARKER' \
                ${promptHarness.promptDir}/issue-prompt.md
              touch $out
            '';

            # The engine must carry nothing language-specific: a Go/Node/Python
            # consumer inherits no Rust machinery (ADR 0003).
            engine-language-agnostic =
              pkgs.runCommand "engine-language-agnostic" { engine = ./lib/mkHarness.nix; }
                ''
                  if grep -Eni 'rust|cargo' "$engine"; then
                    echo "lib/mkHarness.nix must not reference rust/cargo symbols" >&2
                    exit 1
                  fi
                  touch $out
                '';

            # A non-Rust `packages` set is baked into the image on top of the
            # harness plumbing. Asserted by matching the (Linux) env's `paths`
            # names in nix — pure eval, so it needs no Linux builder and no
            # sandboxed read of the env derivation.
            packages-baked =
              let
                inherit (pkgs.lib) assertMsg any hasInfix;
                names = map (p: p.name or "") nonRustHarness.agentEnv.paths;
                baked = frag: any (n: hasInfix frag n) names;
              in
              assert assertMsg (baked "hello-")
                "expected the hello package baked into the env";
              # engine plumbing is still layered on, language-agnostically
              assert assertMsg (baked "git-")
                "expected git plumbing layered into the env";
              pkgs.runCommand "packages-baked" { } "touch $out";

            # Nix is the first-class default: every box ships the nix CLI unless
            # the Consumer opts into the lean escape hatch (nixInBox = false).
            nix-baked-by-default =
              let
                inherit (pkgs.lib) assertMsg any hasInfix;
                names = map (p: p.name or "") nonRustHarness.agentEnv.paths;
                hasNix = any (n: hasInfix "nix-" n || n == "nix") names;
              in
              assert assertMsg hasNix
                "expected the nix CLI to be baked into the default box";
              pkgs.runCommand "nix-baked-by-default" { } "touch $out";

            # The lean/no-nix escape hatch must not include the nix CLI.
            lean-escape-hatch =
              let
                inherit (pkgs.lib) assertMsg any hasInfix;
                names = map (p: p.name or "") leanHarness.agentEnv.paths;
                hasNix = any (n: hasInfix "nix-" n || n == "nix") names;
              in
              assert assertMsg (!hasNix)
                "lean harness (nixInBox = false) must not bake in the nix CLI";
              pkgs.runCommand "lean-escape-hatch" { } "touch $out";
          }
          # The baked entrypoint must carry a store-path shebang, not the
          # source's `#!/usr/bin/env bash` — the Box has no /usr/bin/env. Guards
          # against baking the raw source instead of the writeShellApplication
          # output. Realises the agent-files layer, so it is gated to a Linux
          # builder and omitted from `nix flake check` on darwin.
          // pkgs.lib.optionalAttrs pkgs.stdenv.isLinux {
            entrypoint-shebang = pkgs.runCommand "entrypoint-shebang" { } ''
              shebang=$(head -1 ${nonRustHarness.agentFiles}/agent/entrypoint.sh)
              case "$shebang" in
                '#!'/nix/store/*bash*) : ;;
                *) echo "entrypoint shebang is not a store bash: $shebang" >&2
                   exit 1 ;;
              esac
              touch $out
            '';

            # The Box must run unprivileged: Claude Code refuses
            # --dangerously-skip-permissions under root. Assert the image config
            # runs as the non-root `agent` user. Realises the image, so it is
            # Linux-gated like the shebang check.
            box-runs-as-non-root =
              pkgs.runCommand "box-runs-as-non-root" { nativeBuildInputs = [ pkgs.jq ]; } ''
                mkdir img && tar -xf ${nonRustHarness.image} -C img
                cfg=$(jq -r '.[0].Config' img/manifest.json)
                user=$(jq -r '.config.User // ""' "img/$cfg")
                echo "image config User = '$user'"
                [ "$user" = "agent" ] || {
                  echo "expected the Box to run as non-root 'agent', got '$user'" >&2
                  exit 1
                }
                touch $out
              '';

            # The rendered prompt must be baked into the agent-files layer at
            # /agent/prompts, so the Box is self-contained and needs no host
            # /nix/store mount (which a macOS podman VM cannot provide). Realises
            # the agent-files layer, so it is Linux-gated like the shebang check.
            prompt-baked-into-image = pkgs.runCommand "prompt-baked-into-image" { } ''
              grep -q 'CONFIGURED-PROMPT-MARKER' \
                ${promptHarness.agentFiles}/agent/prompts/issue-prompt.md
              touch $out
            '';

            # The nix.conf and store DB must be present in the image so
            # `nix flake check` reuses the baked closure instead of re-substituting.
            # Realises the default image; Linux-gated like the other image checks.
            nix-conf-in-image =
              pkgs.runCommand "nix-conf-in-image" { nativeBuildInputs = [ pkgs.jq ]; } ''
                # Extract the image ONCE (like box-runs-as-non-root), then read
                # only the top "customisation" layer where extraCommands writes
                # nix.conf. Reading the compressed image more than once exhausts
                # the runner's disk burst credits and wedges CI for minutes;
                # re-reading all ~98 extracted layers is just as slow.
                mkdir img && tar -xf ${nonRustHarness.image} -C img
                layer="$(jq -r '.[0].Layers[-1]' img/manifest.json)"
                # The customisation layer is packed with `tar -cf layer.tar .`, so
                # members carry a leading `./`; match and extract the real name.
                member="$(tar -tf "img/$layer" \
                  | grep -E '^(\./)?etc/nix/nix\.conf$' | head -1 || true)"
                [ -n "$member" ] || {
                  echo "etc/nix/nix.conf not in the image's top (customisation) layer" >&2
                  exit 1
                }
                tar -xOf "img/$layer" "$member" > nix.conf
                grep -q 'experimental-features = nix-command flakes' nix.conf || {
                  echo "nix.conf is missing experimental-features" >&2
                  exit 1
                }
                grep -q 'sandbox = false' nix.conf || {
                  echo "nix.conf is missing sandbox = false" >&2
                  exit 1
                }
                touch $out
              '';
          };

          # For hacking ON the harness itself (host-side).
          devShells.default = pkgs.mkShell {
            packages = [
              pkgs.git
              pkgs.gh
              pkgs.jq
            ];
          };
        };
    };
}
