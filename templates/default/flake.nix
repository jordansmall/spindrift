{
  description = "A spindrift consumer — headless coding agents, one disposable container per issue";

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
            #     # Name-keyed model/effort shorthand (issue #2560): a lighter alternative to the roster list below when you only want to override one agent's model or effort.
            #     byName = {
            #       filer = {
            #         model = "claude-haiku-4-5-20251001";
            #         effort = "high";
            #       };
            #     };
            #     # main/coordinator Claude model for the agent (zero-rebuild runtime switch); worker-tier defaults are unaffected
            #     default = "claude-sonnet-5";
            #     # main/coordinator reasoning-effort level for the agent (zero-rebuild runtime switch); pass-through only, no normalization -- the value must be valid for the active Driver: claude accepts low/medium/high/xhigh/max (appended as --effort <level>), opencode's cross-provider variant selector accepts a provider-specific set (appended as --variant <level>); unset emits no argument for either Driver, leaving the Driver's own default effort in place
            #     effort = "";
            #     # filer subagent model tier; empty (default) omits the filer entry from --agents and means the filer is not provisioned at all — setting a model is the opt-in (recommended: claude-haiku-4-5-20251001). DEPRECATED: superseded by the byName/roster options (agents.models.byName for a one-agent override, agents.models.roster for the full list; see docs/reference.md); these per-agent knobs still work but will be removed.
            #     filer = "";
            #     # reviewer subagent model tier; empty omits the reviewer entry from --agents; the flag itself is omitted only when no subagent model is set. DEPRECATED for non-orchestrator use: superseded by the byName/roster options (agents.models.byName for a one-agent override, agents.models.roster for the full list; see docs/reference.md). Under ORCHESTRATOR, the roster reviewer entry is itself superseded by the code-owned review pass, which instead binds its model from this value (captured before the roster entry is deleted, falling back to the coordinator model when unset). An explicit dispatch-time value (REVIEW_MODEL=... / --review-model ...) overrides even that baked extraction on an already-built image, no rebuild needed (issue #3171) -- precedence is dispatch-time env > baked roster reviewer entry > coordinator-model fallback; unset or empty at dispatch leaves the baked chain unchanged.
            #     review = "claude-opus-5";
            #     # value for the orchestrator's code-owned review pass's own --effort flag (issue #2387); pass-through only, no normalization, same accepted values as EFFORT for the active Driver. Overrides the roster reviewer entry's own effort (rosterDefaults.reviewer.effort by default), regardless of whether the roster is the built-in default or a Consumer-supplied explicit one (lib/mkHarness.nix applies the override post-normalize, issue #2512) -- empty means follow the roster, a non-empty value overrides it. Unlike the four legacy per-agent model knobs (scoutModel/reviewModel/filerModel/workerModel), which an explicit roster arg always wins over, this override applies regardless of roster source (lib/roster.nix). Meaningful only under ORCHESTRATOR: the resolved value reaches the orchestrator via the prompt-assembly Handoff's ReviewEffort field (issue #2512), mirroring how ReviewModel reaches it. Like REVIEW_MODEL, an explicit dispatch-time value (REVIEW_EFFORT=... / --review-effort ...) overrides the baked value on an already-built image, no rebuild needed (issue #3171) -- precedence is dispatch-time env > baked roster reviewer entry > coordinator fallback; unset or empty at dispatch leaves the baked chain unchanged (see MIGRATING.md).
            #     reviewEffort = "";
            #     # Subagent roster (issue #264): the first-class N-agent list. Supersedes the four deprecated per-agent model knobs (filer/review above, scout/worker below) and the byName shorthand above, when set. An explicit roster like this one replaces defaultRoster wholesale -- this two-entry example customizes only scout/reviewer, so a Consumer copying it verbatim drops filer/worker; add entries for them too to keep all four.
            #     roster = [
            #       {
            #         name = "scout";
            #         model = "claude-haiku-4-5-20251001";
            #         mode = "subagent";
            #         description = "Map relevant files, seams, and tests; return a structured brief";
            #         tools = [ "Read" "Bash" "WebFetch" "WebSearch" "Glob" "Grep" ];
            #         promptFile = "scout-prompt.md";
            #         effort = "medium";
            #       }
            #       {
            #         name = "reviewer";
            #         model = "claude-opus-5";
            #         mode = "subagent";
            #         description = "Review the branch diff for spec compliance and coding standards";
            #         tools = [ "Read" "Bash" "WebFetch" "Agent" ];
            #         promptFile = "review-prompt.md";
            #         effort = "high";
            #       }
            #     ];
            #     # scout subagent model tier; empty omits the scout entry from --agents; the flag itself is omitted only when no subagent model is set. DEPRECATED: superseded by the byName/roster options (agents.models.byName for a one-agent override, agents.models.roster for the full list; see docs/reference.md); these per-agent knobs still work but will be removed.
            #     scout = "claude-haiku-4-5-20251001";
            #     # implement-capable worker subagent model tier; empty omits the worker entry from --agents. When set, the implementor runs IMPLEMENT as a coordinator and delegates one slice at a time to this subagent (fragments/coordinator.md). DEPRECATED: superseded by the byName/roster options (agents.models.byName for a one-agent override, agents.models.roster for the full list; see docs/reference.md); these per-agent knobs still work but will be removed.
            #     worker = "claude-sonnet-5";
            #   };
            #   # host directory mounted over /agent/prompts for zero-rebuild prompt iteration
            #   promptDir = "";
            # };
            # dispatch = {
            #   budget = {
            #     # cumulative tokens across every attempt dispatched so far -- the initial run, every fix pass, and any retried attempt within each (issue #2575) -- before selfHealGate stops dispatching further fix passes (issue #2001) and, forwarded into the Box, before the orchestrator's own review loop commits to a terminal land pass instead of a further BLOCK-triggered review round (issue #2694); 0 disables the token budget cap
            #     tokens = 0;
            #     # cumulative cost in USD across every attempt dispatched so far -- the initial run, every fix pass, and any retried attempt within each (issue #2575) -- before selfHealGate stops dispatching further fix passes (issue #2001) and, forwarded into the Box, before the orchestrator's own review loop commits to a terminal land pass instead of a further BLOCK-triggered review round (issue #2694); 0 disables the cost budget cap; give it as a quoted string in flake settings since it may be fractional, e.g. 4.44
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
            #   # whether the Box writes to the Code Forge and Issue Tracker directly (read-write) or the launcher host-mediates every write instead (read-only), a third axis orthogonal to CODE_FORGE and ISSUE_TRACKER (issue #1914); read-only is coherence-checked against the selected forge/tracker's registry capability bits at nix build (Consumer eval) time (issue #2526) — permitted only when the selected forge implements bundle-relay and host-side draft-PR-create and the selected tracker implements host-posted comments, otherwise the build throws naming the missing seam; local, github, and forgejo backends all satisfy the check today (ADR extending 0032/0033 to github, docs/adr/0034-host-mediated-github-forge-and-issue-access.md); the launcher's own startup gate now only backstops a runtime override of this value past what nix already validated
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
            #     # seconds between merge-gate poll iterations; not a rate-limit lever — the gate is one strictly-serialized single-point GraphQL query at a time, only while a PR is actively landing (the cadence-sensitive pollers are the continuous refill ticker and the console backlog poll), and the interval is reused as four fixed-call-count delays (SUCCESS confirm, check-registration window, merge-blocked retry, fix-pass confirm) that stretch with it for zero rate-limit benefit — see docs/reference.md before bumping (issue #3249)
            #     pollInterval = 30;
            #     # total seconds to wait for CI green before abandoning the merge attempt
            #     pollTimeout = 3600;
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
            #     # max memory per agent Box: hard --memory cap under OCI; under bwrap, a per-Box cgroup v2 memory.max when the host delegates a writable cgroup subtree, else best-effort (warns and proceeds uncapped -- ADR 0042); empty string disables the limit
            #     memory = "5g";
            #     # max processes per agent Box: hard --pids-limit cap under OCI; under bwrap, a per-Box cgroup v2 pids.max when delegation is available, else best-effort (warns and proceeds uncapped -- ADR 0042); empty string disables the limit
            #     pids = "512";
            #   };
            #   network = {
            #     # when non-empty, forces bwrap's network-namespace isolation on (pasta-backed since issue #2666, no longer DNS-breaking); redundant with the new isolate-by-default posture unless paired with NETWORK_MODE=host, which nix eval already rejects -- see network.mode
            #     bwrapUnshare = false;
            #     # Box network posture, rendered per runtime/OCI backend into the right flag/syntax: 'open' (default) isolates bwrap into its own network namespace behind a hardened pasta helper -- working egress, host loopback blocked, podman-rootless parity (issue #2666) -- while applying no flag on OCI (unchanged there, the runtime's own default network); 'host' is a bwrap-only opt-out that deliberately restores the pre-#2666 shared-host-netns posture (no OCI rendering -- falls through to the same no-flag default 'open' renders there, a harmless no-op); 'no-host-loopback' keeps internet egress while denying host-loopback on podman (renders pasta, no --map-gw); on docker/nerdctl it renders their own default bridge network, an inert-but-correct render that does not yet deny host-loopback there -- unsupported on runtime=bwrap (no rendering distinct from the new isolated-by-default 'open' -- throws at eval); 'none' is fully offline, documented test-only since a Driver can't reach its Provider under it. Mutually exclusive at eval time with the raw PODMAN_NETWORK/BWRAP_UNSHARE_NET escape-hatch knobs (network.podman/network.bwrapUnshare) -- setting both throws, there is no precedence rule
            #     mode = "open";
            #     # --network value for podman run; empty applies no flag (podman NAT default); set to 'pasta' to restrict egress
            #     podman = "";
            #   };
            #   # path to a TOML routes file declaring registry routes (ADR 0045); each route binds match-host, upstream-base-url (base path permitted), optional auth-scheme (bearer default; basic and header:<Name>), optional enforce-allowlist (default false, advisory: a route-relative path outside the derived allowlist is logged and relayed, never refused, since derived patterns aren't provably complete and a false denial looks like a registry outage to the Agent; when true, checked after the TCP shared-secret and GET/HEAD gates and before credential attachment, so a non-matching path is answered 403 naming the policy and pattern set and never dials upstream or gets a credential attached; per-route only -- tightens, never loosens, the read-only posture; on a cargo route, note the Forwarder-rewritten dl endpoint is itself outside the derived allowlist, so enabling it there also 403s crate downloads), optional cargo-registries (names of the Target repo's [registries.NAME] entries this route serves, each restricted to letters, digits, '-', and '_'; cargo binds to a named registry through source replacement rendered into the Box's $CARGO_HOME/config.toml, keyed off the repo's own un-rewritten .cargo/config.toml -- not a rewrite of that file -- so when cargo-registries is present it restricts which of the repo's declared registries get a replacement stanza on this route, and when absent every declared registry whose index host matches this route does; the CARGO_REGISTRIES_<PROXY-SOURCE-NAME>_TOKEN placeholder this produces is keyed to cargo's own replacement source name, not the repo's registry name, since cargo binds credential lookups to the replacement source once a source is replaced -- ADR 0044 #3201 amendment), and an optional credential source reference (exactly one when present; an absent credential key leaves that route unauthenticated, a plain pass-through); the file carries credential source REFERENCES (env var names, file paths), never secret values; unset disables the registry proxy entirely; this is the only declaration surface for registry routes; the proxy routes strictly by a per-route path prefix slugged from each route's match-host -- a request under no known prefix is refused before any upstream is dialed
            #   registryProxyRoutesFile = "";
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
            #     # JSON array of research verdict objects [{verdict,label,description}], order preserved, defining the research dispatch's verdict vocabulary and each verdict's terminal label (ADR 0022); empty (default) uses the built-in three, with no behavior change (see lib/research-verdicts.nix's defaultVerdicts for the built-in three and their labels). The launcher validates the posted verdict against this set and applies the mapped label on Settle; the research prompt's verdict contract is rendered from it
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
