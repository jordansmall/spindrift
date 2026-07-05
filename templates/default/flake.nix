{
  description = "A spindrift consumer — headless Claude Code agents in nix-built, disposable containers, one per GitHub issue";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts.url = "github:hercules-ci/flake-parts";
    spindrift.url = "github:jordansmall/spindrift";

    # Only needed by the sample Rust toolchain below. Delete it (and the
    # overlay/packages that use it) if your project isn't Rust.
    rust-overlay = {
      url = "github:oxalica/rust-overlay";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs =
    inputs@{
      flake-parts,
      spindrift,
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

      # Pull in spindrift's declarative option surface. Everything below under
      # `perSystem.spindrift` tunes the harness; unset options keep spindrift's
      # own defaults.
      imports = [ spindrift.flakeModules.default ];

      perSystem = _: {
        spindrift = {
          # ---- Toolchain baked into the agent's image -----------------------
          # Swap the overlay/config/packages for your stack (node, go, python,
          # …) — the engine is language agnostic. `packages` is a function of
          # the (Linux) pkgs.
          overlays = [ (import rust-overlay) ];
          config.allowUnfree = true;
          packages =
            p:
            [ (p.rust-bin.fromRustupToolchainFile ./toolchain/rust-toolchain.toml) ]
            ++ import ./toolchain/packages.nix { pkgs = p; };

          # Warm any dependency caches after the clone (runs in the work tree).
          prefetch = "cargo fetch --locked || true";

          # ---- Agent behaviour ---------------------------------------------
          # The prompt is baked into the image; changing it requires an image
          # rebuild (nix run .#build). Set SPINDRIFT_PROMPT_DIR at runtime to
          # point at a local directory for zero-rebuild iteration.
          prompt = builtins.readFile ./prompts/issue-prompt.md;

          # ---- Non-secret run defaults (optional) --------------------------
          # A matching env var still wins at runtime. Secrets and the target
          # (REPO_SLUG, GH_TOKEN, auth) are runtime env — see harness.env.example.
          # defaults = {
          #   label = "ready-for-agent";
          #   baseBranch = "main";
          #   maxParallel = 3;
          #   branchPrefix = "agent/issue-";
          # };
        };
      };
    };
}
