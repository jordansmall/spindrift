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
      perSystem =
        { system, ... }:
        let
          # OCI images are Linux-only. Map a darwin host to its Linux twin so
          # `nix build .#spindrift` yields a runnable image no matter where
          # it's invoked. On darwin this realises through a Linux builder —
          # see README.md ("Building on macOS").
          linuxSystem =
            {
              "aarch64-darwin" = "aarch64-linux";
              "x86_64-darwin" = "x86_64-linux";
              "aarch64-linux" = "aarch64-linux";
              "x86_64-linux" = "x86_64-linux";
            }
            .${system};

          pkgs = import nixpkgs {
            system = linuxSystem;
            overlays = [ (import rust-overlay) ];
            # Claude Code ships under an unfree license.
            config.allowUnfree = true;
          };

          # Pinned language toolchain. Edit toolchain/rust-toolchain.toml to
          # change channel/targets; for a non-Rust project, drop this and its
          # reference in `agentEnv` below.
          rustToolchain = pkgs.rust-bin.fromRustupToolchainFile ./toolchain/rust-toolchain.toml;

          # Project-specific tools the agent needs to build & test the target
          # repo. EDIT toolchain/packages.nix for your stack.
          projectPackages = import ./toolchain/packages.nix { inherit pkgs; };

          # Plumbing every agent needs regardless of language: a shell, the VCS
          # + GitHub CLIs, Claude Code, CA certs, and the unix tools the
          # entrypoint relies on. Leave this list alone.
          harnessPackages = with pkgs; [
            bashInteractive
            coreutils
            gnugrep
            gnused
            findutils
            gettext # envsubst, used by agent/entrypoint.sh
            git
            gh
            claude-code
            cacert
          ];

          agentEnv = pkgs.buildEnv {
            name = "agent-env";
            paths = [ rustToolchain ] ++ harnessPackages ++ projectPackages;
            pathsToLink = [
              "/bin"
              "/lib"
              "/etc"
              "/share"
              "/include"
            ];
          };
        in
        {
          # The disposable agent container: the pinned toolchain baked into a
          # minimal OCI image. `bin/build` runs this and loads it into podman.
          packages.spindrift = pkgs.dockerTools.buildLayeredImage {
            name = "spindrift";
            tag = "latest";
            contents = [ agentEnv ];
            extraCommands = ''
              mkdir -p tmp home/agent work
              chmod 1777 tmp
            '';
            config = {
              Entrypoint = [ "/bin/bash" ];
              WorkingDir = "/";
              Env = [
                "PATH=/bin"
                "HOME=/home/agent"
                "CARGO_HOME=/home/agent/.cargo"
                "SSL_CERT_FILE=${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt"
                "GIT_SSL_CAINFO=${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt"
                "PKG_CONFIG_PATH=/lib/pkgconfig"
              ];
            };
          };

          # For hacking ON the harness itself (host-side). Uses the native
          # system's pkgs, not the Linux image set above.
          devShells.default =
            let
              hostPkgs = import nixpkgs { inherit system; };
            in
            hostPkgs.mkShell {
              packages = [
                hostPkgs.git
                hostPkgs.gh
                hostPkgs.jq
              ];
            };
        };
    };
}
