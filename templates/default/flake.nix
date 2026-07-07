{
  description = "A spindrift consumer — headless Claude Code agents in nix-built, disposable containers, one per GitHub issue";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts.url = "github:hercules-ci/flake-parts";
    spindrift.url = "github:jordansmall/spindrift";
  };

  outputs =
    inputs@{
      flake-parts,
      spindrift,
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

      perSystem =
        { config, pkgs, ... }:
        {
          spindrift = {
            # ---- Toolchain baked into the agent's image -----------------------
            # A function of the (Linux) pkgs — the engine is language agnostic, so
            # this is the one line to change for your stack. Straight from nixpkgs
            # here; add `overlays`/an extra input only if your stack needs one
            # (e.g. rust-overlay for pinned Rust channels).
            packages = p: [ p.go ];

            # Warm any dependency caches after the clone (runs in the work tree).
            prefetch = "go mod download || true";

            # ---- Agent behaviour ---------------------------------------------
            # The prompt is baked into the image; changing it requires an image
            # rebuild (spindrift build). Set SPINDRIFT_PROMPT_DIR at runtime to
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

          # devShell-first: `nix develop` (or `direnv allow` with .envrc) puts
          # the spindrift CLI on PATH so you can run `spindrift dispatch` directly.
          # Copy harness.env.example → harness.env and fill in REPO_SLUG / GH_TOKEN
          # before the first dispatch.
          devShells.default = pkgs.mkShell {
            packages = [ config.packages.spindrift ];
          };
        };
    };
}
