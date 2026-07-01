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

      # The engine, exposed for Consumer flakes to import.
      flake.lib.mkHarness = import ./lib/mkHarness.nix;

      perSystem =
        { system, pkgs, ... }:
        let
          # Dogfood: build spindrift's own harness through mkHarness. Rust stays
          # wired here for now (parameterized in #2).
          harness = import ./lib/mkHarness.nix {
            inherit nixpkgs system;
            overlays = [ (import rust-overlay) ];
            config.allowUnfree = true;
            packages =
              p:
              [ (p.rust-bin.fromRustupToolchainFile ./toolchain/rust-toolchain.toml) ]
              ++ import ./toolchain/packages.nix { pkgs = p; };
          };

          # A minimal, non-Rust consumer, proving the engine bakes an arbitrary
          # `packages` set with no language-specific machinery. Kept off the
          # public outputs — the checks introspect it at eval time only.
          nonRustHarness = import ./lib/mkHarness.nix {
            inherit nixpkgs system;
            packages = p: [ p.hello ];
          };
        in
        {
          inherit (harness) packages apps;

          checks = {
            # shellcheck the bash layers (scripts, entrypoint, fakes, helper).
            shellcheck =
              pkgs.runCommand "shellcheck"
                {
                  nativeBuildInputs = [ pkgs.shellcheck ];
                }
                ''
                  shellcheck --shell=bash \
                    ${./lib/scripts/run.sh} \
                    ${./lib/scripts/build.sh} \
                    ${./agent/entrypoint.sh} \
                    ${./tests/fakes/podman} \
                    ${./tests/fakes/gh} \
                    ${./tests/fakes/claude} \
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
                  RUN_CMD = "${harness.run}/bin/run";
                  BUILD_CMD = "${harness.build}/bin/build";
                  IMAGE_PATH = harness.imagePath;
                  FAKES_DIR = ./tests/fakes;
                  ENTRYPOINT = ./agent/entrypoint.sh;
                  PROMPTS_DIR = ./prompts;
                }
                ''
                  export HOME="$TMPDIR/home"
                  mkdir -p "$HOME"
                  cp -r ${./tests} tests
                  chmod -R +w tests
                  bats tests/
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
            # harness plumbing. Asserted by grepping the (eval-only, Linux) env
            # derivation, so it needs no Linux builder.
            packages-baked =
              pkgs.runCommand "packages-baked"
                {
                  envDrv = builtins.unsafeDiscardStringContext nonRustHarness.agentEnv.drvPath;
                }
                ''
                  test -f "$envDrv"
                  grep -q -- '-hello-' "$envDrv" \
                    || { echo "expected the hello package baked into the env" >&2; exit 1; }
                  # engine plumbing is still layered on, language-agnostically
                  grep -q -- '-git-' "$envDrv"
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
