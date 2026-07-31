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
            infra.image.packages = p: [ p.go ];

            # Warm any dependency caches after the clone (runs in the work tree).
            infra.image.prefetch = "go mod download || true";

            # ---- Agent behaviour ---------------------------------------------
            # The prompt is baked into the image; changing it requires an image
            # rebuild (spindrift build). Set SPINDRIFT_PROMPT_DIR at runtime to
            # point at a local directory for zero-rebuild iteration.
            agents.prompt = builtins.readFile ./prompts/issue-prompt.md;

            # ---- Non-secret run defaults (optional) --------------------------
            # Grouped by domain (ADR 0037); a matching env var still wins at
            # runtime. Secrets and the target (REPO_SLUG, GH_TOKEN, auth) are
            # runtime env — see harness.env.example. Full reference: docs/flake-options.md
            # BEGIN GENERATED SETTINGS EXAMPLE -- nix run .#regen -- DO NOT EDIT
            # agents = {
            #   format = {
            #     # when enabled, the implementor auto-detects and runs the project's formatter on changed files before each commit; skips silently when no formatter is found
            #     enable = false;
            #   };
            #   lint = {
            #     # when enabled, the implementor auto-detects and runs the project's linter on changed files before each commit, applying auto-fix then resolving remaining findings; skips silently when no linter is found
            #     enable = false;
            #   };
            #   models = {
            #     # main/coordinator Claude model for the agent (zero-rebuild runtime switch); worker-tier defaults are unaffected
            #     default = "claude-opus-4-8";
            #     # filer subagent model tier; empty (default) omits the filer entry from --agents and means the filer is not provisioned at all — setting a model is the opt-in (recommended: claude-haiku-4-5-20251001). DEPRECATED: superseded by the roster option (see docs/reference.md); these per-agent knobs still work but will be removed.
            #     filer = "";
            #     # reviewer subagent model tier; empty omits the reviewer entry from --agents; the flag itself is omitted only when no subagent model is set. DEPRECATED: superseded by the roster option (see docs/reference.md); these per-agent knobs still work but will be removed.
            #     review = "claude-opus-4-8";
            #     # scout subagent model tier; empty omits the scout entry from --agents; the flag itself is omitted only when no subagent model is set. DEPRECATED: superseded by the roster option (see docs/reference.md); these per-agent knobs still work but will be removed.
            #     scout = "claude-haiku-4-5-20251001";
            #     # implement-capable worker subagent model tier; empty omits the worker entry from --agents; the implementor prompt does not delegate to it yet — this only provisions the subagent so it is invokable. DEPRECATED: superseded by the roster option (see docs/reference.md); these per-agent knobs still work but will be removed.
            #     worker = "claude-sonnet-5";
            #   };
            #   # host directory mounted over /agent/prompts for zero-rebuild prompt iteration
            #   promptDir = "";
            # };
            # dispatch = {
            #   budget = {
            #     # cumulative tokens across the initial run and every fix pass before selfHealGate stops dispatching further fix passes (issue #2001); 0 disables the token budget cap
            #     tokens = 0;
            #     # cumulative cost in USD across the initial run and every fix pass before selfHealGate stops dispatching further fix passes (issue #2001); 0 disables the cost budget cap; give it as a quoted string in flake settings since it may be fractional, e.g. 4.44
            #     usd = "0.000000";
            #   };
            #   continuous = {
            #     # when enabled, dispatch runs as a long-running slot-refill loop instead of a single wave (#527): as each Box finishes, the launcher re-discovers the queue and refills the freed slot when the image-freshness probe (#526) reports fresh; a rebuild-needed result stops refilling, lets in-flight Boxes finish, and exits with the new documented code (see the exit-code table in docs/reference.md's Dogfood loop section, under Termination). Off by default; applies to queue discovery only — ISSUE_NUMBER-claimed and selective dispatch ignore it
            #     enable = false;
            #   };
            #   # caps the wave size; 0 means uncapped
            #   maxJobs = 0;
            #   # maximum concurrent agent containers
            #   maxParallel = 3;
            #   orchestrator = {
            #     # master feature-flag switch (issue #1996; canonicalized #2047): when enabled, forks entrypoint.sh's rendered prompt/--agents JSON onto the orchestrator-on path -- the implementor pass hands off to the in-box Go orchestrator instead of calling driver-exec directly, and every other orchestrator-conditioned fork (e.g. the filer's write-mechanism gate) reads this same switch; off by default, the direct driver-exec path is unchanged; the off-path is legacy, slated for demolition once this defaults on in production with a sustained A/B win (ADR 0035 amendment)
            #     enable = false;
            #   };
            #   # declared ## Touches overlap policy: defer (hold a Dispatchable issue whose declared touch-set intersects an InProgress issue's, retrying once the collider completes), off (disable the check)
            #   overlapGate = "defer";
            #   retry = {
            #     # jitter seconds added to 429 hold duration to spread re-dispatch
            #     holdJitter = 5;
            #     # fix-agent passes when CI is genuinely red before marking agent-failed; 0 disables self-healing
            #     maxFix = 3;
            #     # rebase-and-retry passes when a green PR conflicts with the base after a sibling merge; 0 disables rebase retries
            #     maxRebase = 3;
            #     # base backoff seconds per retry for 529/overloaded and network transients
            #     transientBackoff = 30;
            #     # max retries for transient exits (529/network backoff; consecutive 429 holds)
            #     transientMax = 3;
            #   };
            # };
            # forge = {
            #   # host path to the bare Accumulation repo (ADR 0033), mounted read-only into the Box and landed into host-side; when CODE_FORGE=local, defaults to .spindrift/accum.git under the launcher's working directory (auto-created and seeded) and an explicit value still overrides it; unused otherwise
            #   accumulationRepoDir = "";
            #   # code-landing backend: github (open PR, watch CI, merge), git (push-only to CODE_FORGE_REMOTE_URL; no PR, CI-watch, or merge gate), local (host-mediated landing onto the Accumulation repo's Integration branch by rebase and fast-forward, never a merge commit; no PR, CI-watch, or network; ADR 0033, issue #1889), or forgejo (push-only to a Forgejo/Gitea instance authenticated by FORGEJO_TOKEN; agent branch, rebase, and merge under MERGE_MODE, no PR surface yet; ADR 0038)
            #   backend = "github";
            #   # whether the Box writes to the Code Forge and Issue Tracker directly (read-write) or the launcher host-mediates every write instead (read-only), a third axis orthogonal to CODE_FORGE and ISSUE_TRACKER (issue #1914); read-only is gated at startup by capability — permitted only when the selected forge implements bundle-relay and host-side draft-PR-create and the selected tracker implements host-posted comments, otherwise the launcher exits with a startup error naming the missing seam; local and github backends both satisfy the gate today (ADR extending 0032/0033 to github, docs/adr/0034-host-mediated-github-forge-and-issue-access.md)
            #   boxAccess = "read-write";
            #   # path to a file the launcher re-reads and swaps into GH_TOKEN whenever its content changes — lets an external minter (e.g. a workflow step re-minting a GitHub App installation token, keeping the App private key in the workflow rather than the launcher) keep the credential fresh across a run that outlives the token's ~1h lifetime (#1027); empty (default) leaves GH_TOKEN static for the whole run
            #   ghTokenRefreshFile = "";
            #   # plain git remote URL to clone from and push to (self-hosted git, gitea, GitLab-without-MRs, a bare server repo); required when CODE_FORGE=git, unused otherwise
            #   remoteURL = "";
            #   # target GitHub repository the agents work on; required unless CODE_FORGE and ISSUE_TRACKER are both local
            #   repoSlug = "owner/repo";
            # };
            # git = {
            #   # default branch agent PRs merge into
            #   baseBranch = "main";
            #   # prefix for agent-cut branches
            #   branchPrefix = "agent/issue-";
            #   merge = {
            #     # comma-separated globs matched against every changed path (added, modified, deleted); a hit downgrades the merge to manual regardless of MERGE_MODE and posts a PR comment naming the match; empty disables the guard (github Code Forge merge path only)
            #     guardPaths = ".github/**,.forgejo/**,**/CLAUDE.md,**/AGENTS.md,.claude/**,.opencode/**";
            #     # how the final integration commits land on green: merge (merge commit), squash, or rebase; maps to GitHub's native merge_method (github Code Forge merge path only)
            #     method = "rebase";
            #     # post-green merge policy: immediate (merge on green), auto (enqueue GitHub native auto-merge; repo must have Allow auto-merge enabled), manual (leave PR open for human approval)
            #     policy = "manual";
            #     # seconds between merge-gate poll iterations
            #     pollInterval = 30;
            #     # total seconds to wait for CI green before abandoning the merge attempt
            #     pollTimeout = 1800;
            #     # when enabled, the launcher proactively rebases a green PR that is behind its base (no textual conflict) before merging and re-waits for CI on the rebased tree, drawing on MAX_REBASE_ATTEMPTS for its budget (ADR 0026). Off by default: a green-but-behind PR merges as-is, relying on its green CI as the landing gate — this trades the rare cross-PR semantic break ADR 0026 guarded against (two individually-green PRs that break combined) for the throughput of parallel landings that never wait on an extra rebase+CI cycle. WARNING: enabling this on a highly-parallelized fleet without a merge queue in front of the branch invites near-constant rebase+re-CI thrashing (each landing leaves the others behind again), burning CI minutes and tokens — see the Stale-base preflight docs
            #     preflightStaleBase = false;
            #     # how a behind branch is brought current before landing: rebase (linear history) or merge (merge the base in); governs both the preflight-stale-base proactive sync and the reactive on-conflict sync during an immediate merge (github Code Forge PR-landing path only)
            #     syncMethod = "rebase";
            #   };
            #   user = {
            #     # commit identity email; falls back to host git config user.email
            #     email = "";
            #     # commit identity name; falls back to host git config user.name
            #     name = "";
            #   };
            # };
            # infra = {
            #   devShell = {
            #     # which devShell to enter; lets a Target expose a lean headless ci shell distinct from a heavy interactive default
            #     name = "default";
            #     # seconds before the devShell probe is abandoned and the baked toolchain is used
            #     probeTimeout = 300;
            #   };
            #   limits = {
            #     # max memory per agent container (--memory); empty string disables the limit
            #     memory = "5g";
            #     # max processes per agent container (--pids-limit); empty string disables the limit
            #     pids = "512";
            #   };
            #   network = {
            #     # when non-empty, adds --unshare-net to bwrap; requires slirp/pasta for DNS; by default bwrap shares the host network namespace (host-loopback reachable)
            #     bwrapUnshare = false;
            #     # --network value for podman run; empty applies no flag (podman NAT default); set to 'pasta' to restrict egress
            #     podman = "";
            #   };
            # };
            # issues = {
            #   forgejo = {
            #     # Forgejo/Gitea instance base URL, defaulting to Codeberg; used when ISSUE_TRACKER=forgejo
            #     baseURL = "https://codeberg.org";
            #   };
            #   jira = {
            #     # Jira site base URL (e.g. https://yourcompany.atlassian.net); required when ISSUE_TRACKER=jira
            #     baseURL = "";
            #     # Jira Cloud account email, paired with JIRA_TOKEN for Basic auth; leave empty for Bearer-token auth (Jira Server/Data Center PATs)
            #     email = "";
            #     # when enabled, the Jira adapter appends the issue's comment thread to the description it returns; off (default) keeps the prompt-injection surface tight
            #     includeComments = false;
            #     # Jira project key issues are read from (e.g. ENG); required when ISSUE_TRACKER=jira
            #     projectKey = "";
            #     # JSON object mapping dispatch states (dispatchable, inProgress, complete, failed) to native Jira status names, e.g. {'inProgress':'In Progress'}; TransitionState performs the matching workflow transition, falling back to swapping the matching lifecycle label when a state is unmapped or its transition is blocked by the project's workflow
            #     statusMapping = "";
            #   };
            #   labels = {
            #     # label the launcher swaps on when CI reaches green (agent is done; merge is separate)
            #     complete = "agent-complete";
            #     # issues carrying this label are dispatchable (the launch button)
            #     dispatch = "ready-for-agent";
            #     # label swapped on when the agent box exits non-zero
            #     failed = "agent-failed";
            #     # label swapped on from LABEL when an issue enters the queue
            #     inProgress = "agent-in-progress";
            #   };
            #   # directory scanned for issue files when ISSUE_TRACKER=local; keep it git-ignored so breakout issues stay private
            #   localDir = ".spindrift/issues";
            #   # when enabled and ISSUE_TRACKER=local, the PR body includes a non-auto-closing `Local-issue: <slug>` breadcrumb; default off keeps the private local ticket slug out of the PR body entirely (ISSUE_TRACKER=github is unaffected -- `Closes #ISSUE_NUMBER` stays either way)
            #   localReference = false;
            #   research = {
            #     # JSON array of research verdict objects [{verdict,label,description}], order preserved, defining the research dispatch's verdict vocabulary and each verdict's terminal label (ADR 0022); empty (default) uses the built-in three (recommend->agent-research-recommend, reject->agent-research-reject, unclear->agent-research-unclear) with no behavior change. The launcher validates the posted verdict against this set and applies the mapped label on Settle; the research prompt's verdict contract is rendered from it
            #     verdicts = "";
            #   };
            #   # IssueTracker backend (ADR 0013): github (gh-exec, default), local (private Markdown + YAML frontmatter files; see LOCAL_ISSUES_DIR), jira (see JIRA_BASE_URL/JIRA_PROJECT_KEY/JIRA_TOKEN), or forgejo (Forgejo/Gitea REST API adapter; Codeberg default via FORGEJO_BASE_URL; see FORGEJO_BASE_URL/FORGEJO_TOKEN); the Code Forge (PR/CI/merge) stays github regardless
            #   tracker = "github";
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
