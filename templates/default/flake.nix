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
            # Grouped by section; a matching env var still wins at runtime.
            # Secrets and the target (REPO_SLUG, GH_TOKEN, auth) are runtime
            # env — see harness.env.example. Full reference: docs/flake-options.md
            # settings = {
            #   issueDiscovery  = { label               = "ready-for-agent";
            #                       issueTracker        = "github";
            #                       localIssuesDir      = ".spindrift/issues";
            #                       jiraIncludeComments = ""; };
            #   lifecycleLabels = { inProgressLabel   = "agent-in-progress";
            #                       failedLabel       = "agent-failed";
            #                       completeLabel     = "agent-complete";
            #                       jiraStatusMapping = ""; };
            #   branches        = { baseBranch        = "main";
            #                       branchPrefix      = "agent/issue-";
            #                       mergeGuardPaths   = ".github/**,**/CLAUDE.md,**/AGENTS.md,.claude/**,.opencode/**";
            #                       mergeMode         = "manual";
            #                       mergePollInterval = 30;
            #                       mergePollTimeout  = 1800; };
            #   concurrency     = { maxParallel  = 3;
            #                       maxJobs      = 0;
            #                       depsPollSecs = 30;
            #                       depsWaitSecs = 7200; };
            #   models          = { model       = "claude-sonnet-5";
            #                       scoutModel  = "claude-haiku-4-5-20251001";
            #                       reviewModel = "claude-opus-4-8";
            #                       filerModel  = ""; };
            #   selfHealing     = { maxFixAttempts       = 3;
            #                       maxRebaseAttempts    = 3;
            #                       holdJitterSecs       = 5;
            #                       transientBackoffSecs = 30;
            #                       transientRetryMax    = 3; };
            #   sandbox         = { devShellName         = "default";
            #                       devShellProbeTimeout = 300;
            #                       memoryLimit          = "4g";
            #                       pidsLimit            = "512";
            #                       podmanNetwork        = "";
            #                       bwrapUnshareNet      = ""; };
            #   repository      = { repoSlug           = "owner/repo";
            #                       gitUserName        = "";
            #                       gitUserEmail       = "";
            #                       codeForge          = "github";
            #                       codeForgeRemoteURL = "";
            #                       jiraBaseURL        = "";
            #                       jiraProjectKey     = "";
            #                       jiraEmail          = ""; };
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
