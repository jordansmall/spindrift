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
            # BEGIN GENERATED SETTINGS EXAMPLE -- nix run .#regen -- DO NOT EDIT
            # settings = {
            # issueDiscovery = {
            #   # IssueTracker backend (ADR 0013): github (gh-exec, default), local (private Markdown + YAML frontmatter files; see LOCAL_ISSUES_DIR), or jira (see JIRA_BASE_URL/JIRA_PROJECT_KEY/JIRA_TOKEN); the Code Forge (PR/CI/merge) stays github regardless
            #   issueTracker = "github";
            #   # when non-empty, the Jira adapter appends the issue's comment thread to the description it returns; empty (default) keeps the prompt-injection surface tight
            #   jiraIncludeComments = "";
            #   # issues carrying this label are dispatchable (the launch button)
            #   label = "ready-for-agent";
            #   # when non-empty and ISSUE_TRACKER=local, the PR body includes a non-auto-closing `Local-issue: <slug>` breadcrumb; default off keeps the private local ticket slug out of the PR body entirely (ISSUE_TRACKER=github is unaffected -- `Closes #ISSUE_NUMBER` stays either way)
            #   localIssueReference = false;
            #   # directory scanned for issue files when ISSUE_TRACKER=local; keep it git-ignored so breakout issues stay private
            #   localIssuesDir = ".spindrift/issues";
            # };
            # lifecycleLabels = {
            #   # label the launcher swaps on when CI reaches green (agent is done; merge is separate)
            #   completeLabel = "agent-complete";
            #   # label swapped on when the agent box exits non-zero
            #   failedLabel = "agent-failed";
            #   # label swapped on from LABEL when an issue enters the queue
            #   inProgressLabel = "agent-in-progress";
            #   # JSON object mapping dispatch states (dispatchable, inProgress, complete, failed) to native Jira status names, e.g. {'inProgress':'In Progress'}; TransitionState performs the matching workflow transition, falling back to swapping the matching lifecycle label when a state is unmapped or its transition is blocked by the project's workflow
            #   jiraStatusMapping = "";
            # };
            # branches = {
            #   # default branch agent PRs merge into
            #   baseBranch = "main";
            #   # prefix for agent-cut branches
            #   branchPrefix = "agent/issue-";
            #   # comma-separated globs matched against every changed path (added, modified, deleted); a hit downgrades the merge to manual regardless of MERGE_MODE and posts a PR comment naming the match; empty disables the guard (github Code Forge merge path only)
            #   mergeGuardPaths = ".github/**,**/CLAUDE.md,**/AGENTS.md,.claude/**,.opencode/**";
            #   # post-green merge policy: immediate (merge on green), auto (enqueue GitHub native auto-merge; repo must have Allow auto-merge enabled), manual (leave PR open for human approval)
            #   mergeMode = "manual";
            #   # seconds between merge-gate poll iterations
            #   mergePollInterval = 30;
            #   # total seconds to wait for CI green before abandoning the merge attempt
            #   mergePollTimeout = 1800;
            # };
            # concurrency = {
            #   # when non-empty, dispatch runs as a long-running slot-refill loop instead of a single wave (#527): as each Box finishes, the launcher re-discovers the queue and refills the freed slot when the image-freshness probe (#526) reports fresh; a rebuild-needed result stops refilling, lets in-flight Boxes finish, and exits with the new documented code (see the exit-code table in docs/reference.md's Dogfood loop section, under Termination). Off by default; applies to queue discovery only — ISSUE_NUMBER-claimed and selective dispatch ignore it
            #   continuousDispatch = false;
            #   # caps the wave size; 0 means uncapped
            #   maxJobs = 0;
            #   # maximum concurrent agent containers
            #   maxParallel = 3;
            #   # declared ## Touches overlap policy: defer (hold a Dispatchable issue whose declared touch-set intersects an InProgress issue's, retrying once the collider completes), off (disable the check)
            #   overlapGate = "defer";
            # };
            # models = {
            #   # filer subagent model tier; empty (default) omits the filer entry from --agents and means the filer is not provisioned at all — setting a model is the opt-in (recommended: claude-haiku-4-5-20251001)
            #   filerModel = "";
            #   # primary (implementor) Claude model for the agent (zero-rebuild runtime switch)
            #   model = "claude-sonnet-5";
            #   # reviewer subagent model tier; empty omits the reviewer entry from --agents; the flag itself is omitted only when no subagent model is set
            #   reviewModel = "claude-opus-4-8";
            #   # scout subagent model tier; empty omits the scout entry from --agents; the flag itself is omitted only when no subagent model is set
            #   scoutModel = "claude-haiku-4-5-20251001";
            # };
            # selfHealing = {
            #   # jitter seconds added to 429 hold duration to spread re-dispatch
            #   holdJitterSecs = 5;
            #   # fix-agent passes when CI is genuinely red before marking agent-failed; 0 disables self-healing
            #   maxFixAttempts = 3;
            #   # rebase-and-retry passes when a green PR conflicts with the base after a sibling merge; 0 disables rebase retries
            #   maxRebaseAttempts = 3;
            #   # when non-empty, the launcher proactively rebases a green PR that is behind its base (no textual conflict) before merging and re-waits for CI on the rebased tree, drawing on MAX_REBASE_ATTEMPTS for its budget (ADR 0026). Off by default: a green-but-behind PR merges as-is, relying on its green CI as the landing gate — this trades the rare cross-PR semantic break ADR 0026 guarded against (two individually-green PRs that break combined) for the throughput of parallel landings that never wait on an extra rebase+CI cycle. WARNING: enabling this on a highly-parallelized fleet without a merge queue in front of the branch invites near-constant rebase+re-CI thrashing (each landing leaves the others behind again), burning CI minutes and tokens — see the Stale-base preflight docs
            #   preflightStaleBase = false;
            #   # base backoff seconds per retry for 529/overloaded and network transients
            #   transientBackoffSecs = 30;
            #   # max retries for transient exits (529/network backoff; consecutive 429 holds)
            #   transientRetryMax = 3;
            # };
            # sandbox = {
            #   # when non-empty, adds --unshare-net to bwrap; requires slirp/pasta for DNS; by default bwrap shares the host network namespace (host-loopback reachable)
            #   bwrapUnshareNet = "";
            #   # which devShell to enter; lets a Target expose a lean headless ci shell distinct from a heavy interactive default
            #   devShellName = "default";
            #   # seconds before the devShell probe is abandoned and the baked toolchain is used
            #   devShellProbeTimeout = 300;
            #   # max memory per agent container (--memory); empty string disables the limit
            #   memoryLimit = "5g";
            #   # max processes per agent container (--pids-limit); empty string disables the limit
            #   pidsLimit = "512";
            #   # --network value for podman run; empty applies no flag (podman NAT default); set to 'pasta' to restrict egress
            #   podmanNetwork = "";
            # };
            # repository = {
            #   # code-landing backend: github (open PR, watch CI, merge), git (push-only to CODE_FORGE_REMOTE_URL; no PR, CI-watch, or merge gate), or local (host-mediated landing onto the Accumulation repo's Integration branch by rebase and fast-forward, never a merge commit; no PR, CI-watch, or network; ADR 0033, issue #1889)
            #   codeForge = "github";
            #   # host path to the bare Accumulation repo (ADR 0033), mounted read-only into the Box and landed into host-side; when CODE_FORGE=local, defaults to .spindrift/accum.git under the launcher's working directory (auto-created and seeded) and an explicit value still overrides it; unused otherwise
            #   codeForgeAccumulationRepoDir = "";
            #   # plain git remote URL to clone from and push to (self-hosted git, gitea, GitLab-without-MRs, a bare server repo); required when CODE_FORGE=git, unused otherwise
            #   codeForgeRemoteURL = "";
            #   # path to a file the launcher re-reads and swaps into GH_TOKEN whenever its content changes — lets an external minter (e.g. a workflow step re-minting a GitHub App installation token, keeping the App private key in the workflow rather than the launcher) keep the credential fresh across a run that outlives the token's ~1h lifetime (#1027); empty (default) leaves GH_TOKEN static for the whole run
            #   ghTokenRefreshFile = "";
            #   # commit identity email; falls back to host git config user.email
            #   gitUserEmail = "";
            #   # commit identity name; falls back to host git config user.name
            #   gitUserName = "";
            #   # Jira site base URL (e.g. https://yourcompany.atlassian.net); required when ISSUE_TRACKER=jira
            #   jiraBaseURL = "";
            #   # Jira Cloud account email, paired with JIRA_TOKEN for Basic auth; leave empty for Bearer-token auth (Jira Server/Data Center PATs)
            #   jiraEmail = "";
            #   # Jira project key issues are read from (e.g. ENG); required when ISSUE_TRACKER=jira
            #   jiraProjectKey = "";
            #   # target GitHub repository the agents work on; required unless CODE_FORGE and ISSUE_TRACKER are both local
            #   repoSlug = "owner/repo";
            # };
            # promptSkillIteration = {
            #   # when non-empty, the implementor auto-detects and runs the project's formatter on changed files before each commit; skips silently when no formatter is found
            #   autoFormat = false;
            #   # when non-empty, the implementor auto-detects and runs the project's linter on changed files before each commit, applying auto-fix then resolving remaining findings; skips silently when no linter is found
            #   autoLint = false;
            # };
            # };
            # END GENERATED SETTINGS EXAMPLE
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
