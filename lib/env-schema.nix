# Registry of every runtime env knob the harness exposes.  One entry per knob;
# generators in mkHarness.nix and flakeModule.nix derive ALL per-knob output from
# this single source — adding an entry here propagates to preambles, flakeModule
# options, the entrypoint defaults block, BOX_ENV_VARS, and harness.env.example
# without further edits.
#
# Fields (all optional except env, doc, and group on non-secret knobs):
#   env          string  env-var name (SCREAMING_SNAKE_CASE)
#   group        string  one of the six domains (agents, git, issues, forge,
#                        dispatch, infra); IS the domain segment of the knob's
#                        flake path — the first segment of `perSystem.spindrift.*`
#                        (lib/nixpath.nix); required on every non-secret knob and
#                        must still match a heading in lib/renderers.nix's
#                        groupOrder
#   nixSubPath   string  optional, flakeOption knobs only: the intra-domain
#                        remainder of the knob's flake path — the nesting or
#                        renamed leaf a flat `group` can't express (e.g.
#                        `label` -> `labels.dispatch` under domain `issues`).
#                        When omitted, the leaf defaults to the knob's own
#                        schema key. The full path is derived by
#                        lib/nixpath.nix as `${group}.${nixSubPath or key}`
#                        (ADR 0037 Pass 2, issue #2188 — collapses the former
#                        standalone nixPath parallel taxonomy)
#   alias        string  optional short-form CLI flag alias (kebab-case, no dashes);
#                        when set, --<alias> is a second way to set the same knob
#   flag         string  optional canonical CLI flag name (kebab-case, no dashes);
#                        when set, --<flag> is the knob's flag and its env-derived
#                        name (toKebab env) becomes a deprecated alias (ADR 0037
#                        Pass 2). When absent, the flag is toKebab(env).
#   default      any     baked-in default; absent means runtime-required or empty
#   placeholder  string  friendly value shown in harness.env.example for required
#                        non-secret vars (e.g. REPO_SLUG=owner/repo); also the
#                        fake value the bats set_box_env fixture exports for a
#                        boxEnv knob with no default (tests/box_env_gen.bash)
#   required     bool    runtime-required (no sensible default; validate() aborts)
#   secret       bool    never a flakeOption; shown as an empty placeholder in example
#   choices      list    nonSecret knobs only (nix/checks/schema-drift.nix's
#                        schema-choices rejects it on a secret knob): non-empty
#                        list of strings driving shell completion; a knob's
#                        default (if any) must be a member of it. Secret knobs
#                        get only a --*-file path flag, never a value-taking
#                        one, so lib/renderers.nix's completion renderers never
#                        look at choices on a secret knob — declaring it there
#                        would pass no validation surface and render nowhere.
#   doc          string  one-line description rendered into harness.env.example
#   flakeOption  bool    consumer-tunable via the flakeModule declarative surface
#   boxEnv       bool    forwarded from the launcher host into the Box container
#   boxEnvOnly   bool    boxEnv knob the Go launcher never reads directly (forwarded
#                        to the Box only); excluded from launcher-env-coverage's
#                        main.go presence requirement
#   intKind      string  one of "positive" / "nonneg"; declares which int parser
#                        a config member takes: "positive" for atoiSchema (zero
#                        or negative falls back to default -- use where zero
#                        would break something, e.g. semaphore capacity),
#                        "nonneg" for atoiNonnegSchema (zero is a valid value --
#                        use for timeouts/poll intervals/counts where 0 means
#                        "disabled"/"uncapped"). Required on every int-typed
#                        host-config member (not secret, not boxEnvOnly) that
#                        loadConfig() reads via atoiSchema/atoiNonnegSchema;
#                        must not appear on non-int members. Enforced by the
#                        schema-drift check (nix/checks/schema-drift.nix)
#   hostConfig   bool    overrides the derived host-config membership rule
#                        (member iff not secret and not boxEnvOnly) for knobs
#                        where that derivation gives the wrong answer
#   hostDerived  bool    marks a field that is generated but whose loader is
#                        hand-written (not a plain getenvSchema/atoiSchema
#                        call); implies host-config membership
#   emptyDisables bool   string-typed knobs only: an explicit KEY= empty
#                        override is itself a meaningful value (not "use the
#                        default") for this knob's runtime env lookup;
#                        loaderLine in lib/renderers.nix routes it through
#                        getenvSchemaPreserveEmpty instead of getenvSchema
#   legacySettingsExempt bool  flakeOption knobs only: true when this knob
#                        postdates the ADR 0037 Pass 2 freeze and therefore
#                        never had an old `settings.<section>` alias; exempts
#                        it from nix/checks/schema-drift.nix's
#                        legacy-settings-section coverage assert (issue #2522
#                        -- freeze details and the cross-check rationale live
#                        on lib/legacy-settings-section.nix and
#                        lib/pre-freeze-flake-options.nix, not repeated here)
let
  backends = import ./backends/default.nix;
in
{
  # ── Consumer-tunable (flakeOption = true) ──────────────────────────────────
  label = {
    env = "LABEL";
    group = "issues";
    flag = "dispatch-label";
    default = "ready-for-agent";
    doc = "issues carrying this label are dispatchable (the launch button)";
    flakeOption = true;
    nixSubPath = "labels.dispatch";
    boxEnv = false;
  };
  issueTracker = {
    env = "ISSUE_TRACKER";
    group = "issues";
    flag = "tracker";
    default = "github";
    doc = "IssueTracker backend (ADR 0013): github (gh-exec, default), local (private Markdown + YAML frontmatter files; see LOCAL_ISSUES_DIR), jira (see JIRA_BASE_URL/JIRA_PROJECT_KEY/JIRA_TOKEN), or forgejo (Forgejo/Gitea REST API adapter; Codeberg default via FORGEJO_BASE_URL; see FORGEJO_BASE_URL/FORGEJO_TOKEN); the Code Forge (PR/CI/merge) stays github regardless";
    choices = map (r: r.name) (builtins.filter (r: r.validAsTracker or false) backends);
    flakeOption = true;
    nixSubPath = "tracker";
    # Forwarded into the Box (issue #1429): the issue prompt's PR-body
    # reference step (lib/fragments.nix PR_BODY_CLOSES/PR_BODY_LOCAL_REF/
    # PR_BODY_LOCAL_NOREF gates) needs to know the tracker in-box to pick the
    # right case; still read directly by the launcher too (main.go), so no
    # boxEnvOnly here.
    boxEnv = true;
  };
  localIssuesDir = {
    env = "LOCAL_ISSUES_DIR";
    group = "issues";
    flag = "local-dir";
    default = ".spindrift/issues";
    doc = "directory scanned for issue files when ISSUE_TRACKER=local; keep it git-ignored so breakout issues stay private";
    flakeOption = true;
    nixSubPath = "localDir";
    boxEnv = false;
  };
  localIssueReference = {
    env = "LOCAL_ISSUE_REFERENCE";
    group = "issues";
    flag = "local-reference";
    default = false;
    kind = "bool";
    doc = "when enabled and ISSUE_TRACKER=local, the PR body includes a non-auto-closing `Local-issue: <slug>` breadcrumb; default off keeps the private local ticket slug out of the PR body entirely (ISSUE_TRACKER=github is unaffected -- `Closes #ISSUE_NUMBER` stays either way)";
    flakeOption = true;
    nixSubPath = "localReference";
    boxEnv = true;
    boxEnvOnly = true;
  };
  baseBranch = {
    env = "BASE_BRANCH";
    group = "git";
    default = "main";
    doc = "default branch agent PRs merge into";
    flakeOption = true;
    boxEnv = true;
  };
  maxParallel = {
    env = "MAX_PARALLEL";
    group = "dispatch";
    default = 3;
    doc = "maximum concurrent agent containers";
    flakeOption = true;
    intKind = "positive";
    boxEnv = false;
  };
  branchPrefix = {
    env = "BRANCH_PREFIX";
    group = "git";
    default = "agent/issue-";
    doc = "prefix for agent-cut branches";
    flakeOption = true;
    boxEnv = true;
  };
  inProgressLabel = {
    env = "IN_PROGRESS_LABEL";
    group = "issues";
    default = "agent-in-progress";
    doc = "label swapped on from LABEL when an issue enters the queue";
    flakeOption = true;
    nixSubPath = "labels.inProgress";
    boxEnv = true;
  };
  failedLabel = {
    env = "FAILED_LABEL";
    group = "issues";
    default = "agent-failed";
    doc = "label swapped on when the agent box exits non-zero";
    flakeOption = true;
    nixSubPath = "labels.failed";
    boxEnv = false;
  };
  completeLabel = {
    env = "COMPLETE_LABEL";
    group = "issues";
    default = "agent-complete";
    doc = "label the launcher swaps on when CI reaches green (agent is done; merge is separate)";
    flakeOption = true;
    nixSubPath = "labels.complete";
    boxEnv = true;
  };
  mergeMode = {
    env = "MERGE_MODE";
    group = "git";
    flag = "merge-policy";
    default = "manual";
    doc = "post-green merge policy: immediate (merge on green), auto (enqueue GitHub native auto-merge; repo must have Allow auto-merge enabled), manual (leave PR open for human approval)";
    choices = [
      "immediate"
      "auto"
      "manual"
    ];
    flakeOption = true;
    nixSubPath = "merge.policy";
    boxEnv = false;
  };
  mergeMethod = {
    env = "MERGE_METHOD";
    group = "git";
    default = "rebase";
    doc = "how the final integration commits land on green: merge (merge commit), squash, or rebase; maps to GitHub's native merge_method (github Code Forge merge path only)";
    choices = [
      "merge"
      "squash"
      "rebase"
    ];
    flakeOption = true;
    nixSubPath = "merge.method";
    boxEnv = false;
  };
  syncMethod = {
    env = "SYNC_METHOD";
    group = "git";
    default = "rebase";
    doc = "how a behind branch is brought current before landing: rebase (linear history) or merge (merge the base in); governs both the preflight-stale-base proactive sync and the reactive on-conflict sync during an immediate merge (github Code Forge PR-landing path only)";
    choices = [
      "rebase"
      "merge"
    ];
    flakeOption = true;
    legacySettingsExempt = true;
    nixSubPath = "merge.syncMethod";
    boxEnv = false;
  };
  mergeGuardPaths = {
    env = "MERGE_GUARD_PATHS";
    group = "git";
    default = ".github/**,.forgejo/**,**/CLAUDE.md,**/AGENTS.md,.claude/**,.opencode/**";
    doc = "comma-separated globs matched against every changed path (added, modified, deleted); a hit downgrades the merge to manual regardless of MERGE_MODE and posts a PR comment naming the match; empty disables the guard (github Code Forge merge path only)";
    flakeOption = true;
    nixSubPath = "merge.guardPaths";
    boxEnv = false;
  };
  effort = {
    env = "EFFORT";
    group = "agents";
    doc = "main/coordinator reasoning-effort level for the agent (zero-rebuild runtime switch); pass-through only, no normalization -- the value must be valid for the active Driver: claude accepts low/medium/high/xhigh/max (appended as --effort <level>), opencode's cross-provider variant selector accepts a provider-specific set (appended as --variant <level>); unset emits no argument for either Driver, leaving the Driver's own default effort in place";
    flakeOption = true;
    legacySettingsExempt = true;
    nixSubPath = "models.effort";
    boxEnv = true;
    boxEnvOnly = true;
  };
  model = {
    env = "MODEL";
    group = "agents";
    default = "claude-sonnet-5";
    doc = "main/coordinator Claude model for the agent (zero-rebuild runtime switch); worker-tier defaults are unaffected";
    flakeOption = true;
    hostConfig = true;
    nixSubPath = "models.default";
    boxEnv = true;
    boxEnvOnly = true;
  };
  scoutModel = {
    env = "SCOUT_MODEL";
    group = "agents";
    default = "claude-haiku-4-5-20251001";
    doc = "scout subagent model tier; empty omits the scout entry from --agents; the flag itself is omitted only when no subagent model is set. DEPRECATED: superseded by the byName/roster options (agents.models.byName for a one-agent override, agents.models.roster for the full list; see docs/reference.md); these per-agent knobs still work but will be removed.";
    flakeOption = true;
    nixSubPath = "models.scout";
    boxEnv = true;
    boxEnvOnly = true;
  };
  reviewModel = {
    env = "REVIEW_MODEL";
    group = "agents";
    default = "claude-opus-5";
    doc = "reviewer subagent model tier; empty omits the reviewer entry from --agents; the flag itself is omitted only when no subagent model is set. DEPRECATED for non-orchestrator use: superseded by the byName/roster options (agents.models.byName for a one-agent override, agents.models.roster for the full list; see docs/reference.md). Under ORCHESTRATOR, the roster reviewer entry is itself superseded by the code-owned review pass, which instead binds its model from this value (captured before the roster entry is deleted, falling back to the coordinator model when unset). An explicit dispatch-time value (REVIEW_MODEL=... / --review-model ...) overrides even that baked extraction on an already-built image, no rebuild needed (issue #3171) -- precedence is dispatch-time env > baked roster reviewer entry > coordinator-model fallback; unset or empty at dispatch leaves the baked chain unchanged.";
    flakeOption = true;
    nixSubPath = "models.review";
    boxEnv = true;
    boxEnvOnly = true;
  };
  reviewEffort = {
    env = "REVIEW_EFFORT";
    group = "agents";
    doc = "value for the orchestrator's code-owned review pass's own --effort flag (issue #2387); pass-through only, no normalization, same accepted values as EFFORT for the active Driver. Overrides the roster reviewer entry's own effort (rosterDefaults.reviewer.effort by default), regardless of whether the roster is the built-in default or a Consumer-supplied explicit one (lib/mkHarness.nix applies the override post-normalize, issue #2512) -- empty means follow the roster, a non-empty value overrides it. Unlike the four legacy per-agent model knobs (scoutModel/reviewModel/filerModel/workerModel), which an explicit roster arg always wins over, this override applies regardless of roster source (lib/roster.nix). Meaningful only under ORCHESTRATOR: the resolved value reaches the orchestrator via the prompt-assembly Handoff's ReviewEffort field (issue #2512), mirroring how ReviewModel reaches it. Like REVIEW_MODEL, an explicit dispatch-time value (REVIEW_EFFORT=... / --review-effort ...) overrides the baked value on an already-built image, no rebuild needed (issue #3171) -- precedence is dispatch-time env > baked roster reviewer entry > coordinator fallback; unset or empty at dispatch leaves the baked chain unchanged (see MIGRATING.md).";
    flakeOption = true;
    legacySettingsExempt = true;
    nixSubPath = "models.reviewEffort";
    boxEnv = true;
    boxEnvOnly = true;
  };
  filerModel = {
    env = "FILER_MODEL";
    group = "agents";
    default = "";
    doc = "filer subagent model tier; empty (default) omits the filer entry from --agents and means the filer is not provisioned at all — setting a model is the opt-in (recommended: claude-haiku-4-5-20251001). DEPRECATED: superseded by the byName/roster options (agents.models.byName for a one-agent override, agents.models.roster for the full list; see docs/reference.md); these per-agent knobs still work but will be removed.";
    flakeOption = true;
    nixSubPath = "models.filer";
    boxEnv = true;
    boxEnvOnly = true;
  };
  workerModel = {
    env = "WORKER_MODEL";
    group = "agents";
    default = "claude-sonnet-5";
    doc = "implement-capable worker subagent model tier; empty omits the worker entry from --agents. When set, the implementor runs IMPLEMENT as a coordinator and delegates one slice at a time to this subagent (fragments/coordinator.md). DEPRECATED: superseded by the byName/roster options (agents.models.byName for a one-agent override, agents.models.roster for the full list; see docs/reference.md); these per-agent knobs still work but will be removed.";
    flakeOption = true;
    nixSubPath = "models.worker";
    boxEnv = true;
    boxEnvOnly = true;
  };
  devShellName = {
    env = "DEV_SHELL_NAME";
    group = "infra";
    default = "default";
    doc = "which devShell to enter; lets a Target expose a lean headless ci shell distinct from a heavy interactive default";
    flakeOption = true;
    nixSubPath = "devShell.name";
    boxEnv = true;
    boxEnvOnly = true;
  };
  devShellProbeTimeout = {
    env = "DEV_SHELL_PROBE_TIMEOUT";
    group = "infra";
    default = 300;
    doc = "seconds before the devShell probe is abandoned and the baked toolchain is used";
    flakeOption = true;
    nixSubPath = "devShell.probeTimeout";
    boxEnv = true;
    boxEnvOnly = true;
  };
  networkMode = {
    env = "NETWORK_MODE";
    group = "infra";
    default = "open";
    doc = "Box network posture, rendered per runtime/OCI backend into the right flag/syntax: 'open' (default) isolates bwrap into its own network namespace behind a hardened pasta helper -- working egress, host loopback blocked, podman-rootless parity (issue #2666) -- while applying no flag on OCI (unchanged there, the runtime's own default network); 'host' is a bwrap-only opt-out that deliberately restores the pre-#2666 shared-host-netns posture (no OCI rendering -- falls through to the same no-flag default 'open' renders there, a harmless no-op); 'no-host-loopback' keeps internet egress while denying host-loopback on podman (renders pasta, no --map-gw); on docker/nerdctl it renders their own default bridge network, an inert-but-correct render that does not yet deny host-loopback there -- unsupported on runtime=bwrap (no rendering distinct from the new isolated-by-default 'open' -- throws at eval); 'none' is fully offline, documented test-only since a Driver can't reach its Provider under it. Mutually exclusive at eval time with the raw PODMAN_NETWORK/BWRAP_UNSHARE_NET escape-hatch knobs (network.podman/network.bwrapUnshare) -- setting both throws, there is no precedence rule";
    choices = [
      "open"
      "no-host-loopback"
      "none"
      "host"
    ];
    flakeOption = true;
    legacySettingsExempt = true;
    nixSubPath = "network.mode";
    boxEnv = false;
  };
  podmanNetwork = {
    env = "PODMAN_NETWORK";
    group = "infra";
    doc = "--network value for podman run; empty applies no flag (podman NAT default); set to 'pasta' to restrict egress";
    flakeOption = true;
    nixSubPath = "network.podman";
    boxEnv = false;
  };
  bwrapUnshareNet = {
    env = "BWRAP_UNSHARE_NET";
    group = "infra";
    default = false;
    # Presence-style bool flag (issue #2145): `--bwrap-unshare-net` (bare) or
    # `--bwrap-unshare-net=<value>` set it; the space-separated value form is
    # not accepted. The boolean `default` also makes it a `types.bool` flake
    # option; `kind` opts its CLI flag into presence parsing.
    kind = "bool";
    doc = "when non-empty, forces bwrap's network-namespace isolation on (pasta-backed since issue #2666, no longer DNS-breaking); redundant with the new isolate-by-default posture unless paired with NETWORK_MODE=host, which nix eval already rejects -- see network.mode";
    flakeOption = true;
    nixSubPath = "network.bwrapUnshare";
    boxEnv = false;
  };
  memoryLimit = {
    env = "MEMORY_LIMIT";
    group = "infra";
    # #712: a single `nix build .#checks-inbox` peaks near 3.7 GiB RSS
    # (agent-issue-640 dmesg); 4g left ~300MiB headroom and got cgroup
    # OOM-killed. 5g gives real headroom above the observed peak.
    #
    # #2379: this cap only matters where podman runs inside a fixed-RAM VM
    # (macOS/Windows) — on native Linux the container shares host RAM
    # directly, so a per-container cap is a self-imposed constraint with no
    # upside there. That's why spindrift's own dogfood config
    # (nix/dogfood-defaults.nix) leaves it unset on native Linux; this "5g"
    # default remains unchanged for every other Consumer.
    default = "5g";
    doc = "max memory per agent Box: hard --memory cap under OCI; under bwrap, a per-Box cgroup v2 memory.max when the host delegates a writable cgroup subtree, else best-effort (warns and proceeds uncapped -- ADR 0042); empty string disables the limit";
    flakeOption = true;
    nixSubPath = "limits.memory";
    boxEnv = false;
    emptyDisables = true;
  };
  pidsLimit = {
    env = "PIDS_LIMIT";
    group = "infra";
    default = "512";
    doc = "max processes per agent Box: hard --pids-limit cap under OCI; under bwrap, a per-Box cgroup v2 pids.max when delegation is available, else best-effort (warns and proceeds uncapped -- ADR 0042); empty string disables the limit";
    flakeOption = true;
    nixSubPath = "limits.pids";
    boxEnv = false;
    emptyDisables = true;
  };
  registryProxyRoutesFile = {
    env = "REGISTRY_PROXY_ROUTES_FILE";
    group = "infra";
    doc = "path to a TOML routes file declaring registry routes (ADR 0045); each route binds match-host, upstream-base-url (base path permitted), optional auth-scheme (bearer default; basic and header:<Name>), optional enforce-allowlist (default false, advisory: a route-relative path outside the derived allowlist is logged and relayed, never refused, since derived patterns aren't provably complete and a false denial looks like a registry outage to the Agent; when true, checked after the TCP shared-secret and GET/HEAD gates and before credential attachment, so a non-matching path is answered 403 naming the policy and pattern set and never dials upstream or gets a credential attached; per-route only -- tightens, never loosens, the read-only posture; on a cargo route, note the Forwarder-rewritten dl endpoint is itself outside the derived allowlist, so enabling it there also 403s crate downloads), optional cargo-registries (names of the Target repo's [registries.NAME] entries this route serves, each restricted to letters, digits, '-', and '_'; when absent, the per-route CARGO_REGISTRIES_<NAME>_TOKEN placeholders are instead derived from the rewritten .cargo/config.toml), and an optional credential source reference (exactly one when present; an absent credential key leaves that route unauthenticated, a plain pass-through); the file carries credential source REFERENCES (env var names, file paths), never secret values; unset disables the registry proxy entirely; this is the only declaration surface for registry routes; the proxy routes strictly by a per-route path prefix slugged from each route's match-host -- a request under no known prefix is refused before any upstream is dialed";
    flakeOption = true;
    legacySettingsExempt = true;
    boxEnv = false;
  };
  jiraBaseURL = {
    env = "JIRA_BASE_URL";
    group = "issues";
    doc = "Jira site base URL (e.g. https://yourcompany.atlassian.net); required when ISSUE_TRACKER=jira";
    flakeOption = true;
    nixSubPath = "jira.baseURL";
    boxEnv = false;
  };
  jiraProjectKey = {
    env = "JIRA_PROJECT_KEY";
    group = "issues";
    doc = "Jira project key issues are read from (e.g. ENG); required when ISSUE_TRACKER=jira";
    flakeOption = true;
    nixSubPath = "jira.projectKey";
    boxEnv = false;
  };
  jiraEmail = {
    env = "JIRA_EMAIL";
    group = "issues";
    doc = "Jira Cloud account email, paired with JIRA_TOKEN for Basic auth; leave empty for Bearer-token auth (Jira Server/Data Center PATs)";
    flakeOption = true;
    nixSubPath = "jira.email";
    boxEnv = false;
  };
  jiraStatusMapping = {
    env = "JIRA_STATUS_MAPPING";
    group = "issues";
    default = "";
    doc = "JSON object mapping dispatch states (dispatchable, inProgress, complete, failed) to native Jira status names, e.g. {'inProgress':'In Progress'}; TransitionState performs the matching workflow transition, falling back to swapping the matching lifecycle label when a state is unmapped or its transition is blocked by the project's workflow";
    flakeOption = true;
    nixSubPath = "jira.statusMapping";
    boxEnv = false;
  };
  jiraIncludeComments = {
    env = "JIRA_INCLUDE_COMMENTS";
    group = "issues";
    default = false;
    kind = "bool";
    doc = "when enabled, the Jira adapter appends the issue's comment thread to the description it returns; off (default) keeps the prompt-injection surface tight";
    flakeOption = true;
    nixSubPath = "jira.includeComments";
    boxEnv = false;
  };
  forgejoBaseURL = {
    env = "FORGEJO_BASE_URL";
    group = "issues";
    default = "https://codeberg.org";
    doc = "Forgejo/Gitea instance base URL, defaulting to Codeberg; used when ISSUE_TRACKER=forgejo";
    flakeOption = true;
    legacySettingsExempt = true;
    nixSubPath = "forgejo.baseURL";
    boxEnv = true;
  };
  researchVerdicts = {
    env = "RESEARCH_VERDICTS";
    group = "issues";
    default = "";
    doc = "JSON array of research verdict objects [{verdict,label,description}], order preserved, defining the research dispatch's verdict vocabulary and each verdict's terminal label (ADR 0022); empty (default) uses the built-in three, with no behavior change (see lib/research-verdicts.nix's defaultVerdicts for the built-in three and their labels). The launcher validates the posted verdict against this set and applies the mapped label on Settle; the research prompt's verdict contract is rendered from it";
    flakeOption = true;
    legacySettingsExempt = true;
    nixSubPath = "research.verdicts";
    boxEnv = false;
  };
  # ── Required runtime inputs ────────────────────────────────────────────────
  repoSlug = {
    env = "REPO_SLUG";
    group = "forge";
    required = true;
    placeholder = "owner/repo";
    doc = "target GitHub repository the agents work on; required unless CODE_FORGE and ISSUE_TRACKER are both local";
    flakeOption = true;
    boxEnv = true;
  };
  ghToken = {
    env = "GH_TOKEN";
    required = true;
    secret = true;
    hostConfig = true;
    placeholder = "fake-token";
    doc = "fine-grained PAT scoped to the target repo — Contents/PR/Issues/Metadata RW; required unless CODE_FORGE and ISSUE_TRACKER are both local";
    boxEnv = true;
  };
  ghTokenRefreshFile = {
    env = "GH_TOKEN_REFRESH_FILE";
    group = "forge";
    doc = "path to a file the launcher re-reads and swaps into GH_TOKEN whenever its content changes — lets an external minter (e.g. a workflow step re-minting a GitHub App installation token, keeping the App private key in the workflow rather than the launcher) keep the credential fresh across a run that outlives the token's ~1h lifetime (#1027); empty (default) leaves GH_TOKEN static for the whole run";
    flakeOption = true;
    boxEnv = false;
  };
  boxGhToken = {
    env = "BOX_GH_TOKEN";
    secret = true;
    doc = "opt-in two-actor separation (ADR 0016): a second machine user's fine-grained PAT for the Box only — the launcher keeps using its own GH_TOKEN for merges, labels, and all host-side forge calls, while the Box receives this value as its GH_TOKEN instead; empty (default) leaves the single-token flow unchanged. Pair with a repository ruleset that bars this user from updating the base branch and bypass-lists only the launcher's user — see docs/reference.md's two-actor separation recipe";
    boxEnv = false;
  };
  claudeOAuthToken = {
    env = "CLAUDE_CODE_OAUTH_TOKEN";
    secret = true;
    hostConfig = true;
    placeholder = "fake-oauth";
    doc = "Claude Code OAuth token (run 'claude setup-token'); set this or ANTHROPIC_API_KEY";
    boxEnv = true;
  };
  opencodeAuthContent = {
    env = "OPENCODE_AUTH_CONTENT";
    secret = true;
    hostConfig = true;
    doc = "opencode github-copilot Provider auth store (JSON) — the whole auth slice opencode reads natively (ADR 0009 amendment, #260); the github-copilot Provider is OAuth-only. Mint once on a host with 'opencode auth login -p github-copilot', then export the github-copilot slice of ~/.local/share/opencode/auth.json (exact jq recipe in docs/reference.md). Required when DRIVER=opencode and MODEL=github-copilot/<model>; ignored under the claude Driver";
    boxEnv = true;
  };
  anthropicAPIKey = {
    env = "ANTHROPIC_API_KEY";
    secret = true;
    hostConfig = true;
    doc = "Anthropic API key; set this or CLAUDE_CODE_OAUTH_TOKEN";
    boxEnv = true;
  };
  jiraToken = {
    env = "JIRA_TOKEN";
    secret = true;
    hostConfig = true;
    doc = "Jira API token (Cloud: paired with JIRA_EMAIL for Basic auth; Server/Data Center: used alone as a Bearer PAT); required when ISSUE_TRACKER=jira";
    boxEnv = false;
  };
  forgejoToken = {
    env = "FORGEJO_TOKEN";
    secret = true;
    hostConfig = true;
    doc = "Forgejo/Gitea API token (Bearer/token scheme); required when ISSUE_TRACKER=forgejo";
    boxEnv = true;
  };
  boxForgejoToken = {
    env = "BOX_FORGEJO_TOKEN";
    secret = true;
    doc = "opt-in two-actor separation (ADR 0016 analog): a second machine user's Forgejo PAT for the Box only — the launcher keeps using its own FORGEJO_TOKEN for merges, labels, and all host-side forge calls, while the Box receives this value as its FORGEJO_TOKEN instead; empty (default) leaves the single-token flow unchanged. Pair with a Forgejo branch-protection rule that bars this user from updating the base branch and whitelists only the launcher's user for push and merge — see docs/reference.md's Forgejo two-actor separation recipe";
    boxEnv = false;
  };
  gitUserName = {
    env = "GIT_USER_NAME";
    group = "git";
    flag = "user-name";
    placeholder = "Test Bot";
    doc = "commit identity name; falls back to host git config user.name";
    flakeOption = true;
    hostDerived = true;
    nixSubPath = "user.name";
    boxEnv = true;
  };
  gitUserEmail = {
    env = "GIT_USER_EMAIL";
    group = "git";
    flag = "user-email";
    placeholder = "bot@example.com";
    doc = "commit identity email; falls back to host git config user.email";
    flakeOption = true;
    hostDerived = true;
    nixSubPath = "user.email";
    boxEnv = true;
  };
  codeForge = {
    env = "CODE_FORGE";
    group = "forge";
    flag = "forge-backend";
    default = "github";
    doc = "code-landing backend: github (open PR, watch CI, merge), git (push-only to CODE_FORGE_REMOTE_URL; no PR, CI-watch, or merge gate), local (host-mediated landing onto the Accumulation repo's Integration branch by rebase and fast-forward, never a merge commit; no PR, CI-watch, or network; ADR 0033, issue #1889), or forgejo (push-only to a Forgejo/Gitea instance authenticated by FORGEJO_TOKEN; agent branch, rebase, and merge under MERGE_MODE, no PR surface yet; ADR 0038)";
    choices = map (r: r.name) (builtins.filter (r: r.validAsCodeForge or false) backends);
    flakeOption = true;
    nixSubPath = "backend";
    boxEnv = true;
  };
  codeForgeRemoteURL = {
    env = "CODE_FORGE_REMOTE_URL";
    group = "forge";
    flag = "remote-url";
    doc = "plain git remote URL to clone from and push to (self-hosted git, gitea, GitLab-without-MRs, a bare server repo); required when CODE_FORGE=git, unused otherwise";
    flakeOption = true;
    nixSubPath = "remoteURL";
    boxEnv = true;
  };
  codeForgeAccumulationRepoDir = {
    env = "CODE_FORGE_ACCUMULATION_REPO_DIR";
    group = "forge";
    flag = "accumulation-repo-dir";
    doc = "host path to the bare Accumulation repo (ADR 0033), mounted read-only into the Box and landed into host-side; when CODE_FORGE=local, defaults to .spindrift/accum.git under the launcher's working directory (auto-created and seeded) and an explicit value still overrides it; unused otherwise";
    flakeOption = true;
    hostDerived = true;
    nixSubPath = "accumulationRepoDir";
    boxEnv = false;
  };
  boxForgeAndIssueAccess = {
    env = "BOX_FORGE_AND_ISSUE_ACCESS";
    group = "forge";
    flag = "box-access";
    default = "read-write";
    doc = "whether the Box writes to the Code Forge and Issue Tracker directly (read-write) or the launcher host-mediates every write instead (read-only), a third axis orthogonal to CODE_FORGE and ISSUE_TRACKER (issue #1914); read-only is coherence-checked against the selected forge/tracker's registry capability bits at nix build (Consumer eval) time (issue #2526) — permitted only when the selected forge implements bundle-relay and host-side draft-PR-create and the selected tracker implements host-posted comments, otherwise the build throws naming the missing seam; local, github, and forgejo backends all satisfy the check today (ADR extending 0032/0033 to github, docs/adr/0034-host-mediated-github-forge-and-issue-access.md); the launcher's own startup gate now only backstops a runtime override of this value past what nix already validated";
    choices = [
      "read-write"
      "read-only"
    ];
    flakeOption = true;
    nixSubPath = "boxAccess";
    # Forwarded into the Box, but the Box's prompt fragments no longer branch
    # on this raw value (issue #1951): dispatch.buildBoxEnv resolves the
    # write-enabled-vs-not decision once, host-side, from this value and
    # forwards a single explicit positive signal, BOX_WRITE_ENABLED, only
    # when writes are permitted. The github issue-blocked-comment and
    # research-verdict prompt fragments (issue #1917), and the github push
    # prompt fragment (issue #1918), gate on BOX_WRITE_ENABLED's presence
    # instead, so an unset, typo'd, or forwarding-glitched value can never
    # fall open into the write-capable path. This knob is still read
    # directly by the launcher too (main.go's
    # newCodeForge/checkReadOnlyCapabilityGate), so no boxEnvOnly here --
    # mirrors ISSUE_TRACKER's own boxEnv forwarding for its PR-body
    # ticket-reference gate.
    boxEnv = true;
  };
  # ── Operator-tunable knobs (flakeOption = true; also tune via harness.env) ─
  maxFixAttempts = {
    env = "MAX_FIX_ATTEMPTS";
    group = "dispatch";
    default = 3;
    doc = "fix-agent passes when CI is genuinely red before marking agent-failed; 0 disables self-healing";
    flakeOption = true;
    intKind = "nonneg";
    nixSubPath = "retry.maxFix";
    boxEnv = false;
  };
  maxRebaseAttempts = {
    env = "MAX_REBASE_ATTEMPTS";
    group = "dispatch";
    default = 3;
    doc = "rebase-and-retry passes when a green PR conflicts with the base after a sibling merge; 0 disables rebase retries";
    flakeOption = true;
    intKind = "nonneg";
    nixSubPath = "retry.maxRebase";
    # Forwarded into the Box so the driver-exec outcome-backstop verb reads
    # this bound for its own best-effort push retry from launcher-delivered
    # plumbing rather than a hand-copied default (issue #2157).
    boxEnv = true;
  };
  maxBudgetTokens = {
    env = "MAX_BUDGET_TOKENS";
    group = "dispatch";
    default = 0;
    doc = "cumulative tokens across every attempt dispatched so far -- the initial run, every fix pass, and any retried attempt within each (issue #2575) -- before selfHealGate stops dispatching further fix passes (issue #2001) and, forwarded into the Box, before the orchestrator's own review loop commits to a terminal land pass instead of a further BLOCK-triggered review round (issue #2694); 0 disables the token budget cap";
    flakeOption = true;
    intKind = "nonneg";
    nixSubPath = "budget.tokens";
    # Forwarded into the Box (issue #2694): the in-Box orchestrator's own
    # review loop now also caps its cumulative token spend against this
    # bound (entrypoint.sh forwards it as --max-budget-tokens), the same
    # dial selfHealGate already gates its host-side fix-pass dispatch with.
    boxEnv = true;
  };
  maxBudgetUSD = {
    # default is a float, not the bare int 0 (unlike every other numeric knob
    # here): lib/flakeModule.nix's mkKnobOption infers a knob's Consumer-
    # facing option type from builtins.isInt (entry.default), so an int
    # default would type settings.selfHealing.maxBudgetUSD as types.int —
    # rejecting the very fractional caps ($4.44, $17.66) this knob exists
    # for. Falling through to types.str instead means a Consumer flake sets
    # it quoted, e.g. maxBudgetUSD = "4.44";.
    env = "MAX_BUDGET_USD";
    group = "dispatch";
    default = 0.0;
    doc = "cumulative cost in USD across every attempt dispatched so far -- the initial run, every fix pass, and any retried attempt within each (issue #2575) -- before selfHealGate stops dispatching further fix passes (issue #2001) and, forwarded into the Box, before the orchestrator's own review loop commits to a terminal land pass instead of a further BLOCK-triggered review round (issue #2694); 0 disables the cost budget cap; give it as a quoted string in flake settings since it may be fractional, e.g. 4.44";
    flakeOption = true;
    nixSubPath = "budget.usd";
    # Forwarded into the Box (issue #2694): the in-Box orchestrator's own
    # review loop now also caps its cumulative USD spend against this bound
    # (entrypoint.sh forwards it as --max-budget-usd), the same dial
    # selfHealGate already gates its host-side fix-pass dispatch with.
    boxEnv = true;
  };
  preflightStaleBase = {
    env = "PREFLIGHT_STALE_BASE";
    group = "git";
    default = false;
    kind = "bool";
    doc = "when enabled, the launcher proactively rebases a green PR that is behind its base (no textual conflict) before merging and re-waits for CI on the rebased tree, drawing on MAX_REBASE_ATTEMPTS for its budget (ADR 0026). Off by default: a green-but-behind PR merges as-is, relying on its green CI as the landing gate — this trades the rare cross-PR semantic break ADR 0026 guarded against (two individually-green PRs that break combined) for the throughput of parallel landings that never wait on an extra rebase+CI cycle. WARNING: enabling this on a highly-parallelized fleet without a merge queue in front of the branch invites near-constant rebase+re-CI thrashing (each landing leaves the others behind again), burning CI minutes and tokens — see the Stale-base preflight docs";
    flakeOption = true;
    nixSubPath = "merge.preflightStaleBase";
    boxEnv = false;
  };
  maxJobs = {
    env = "MAX_JOBS";
    group = "dispatch";
    default = 0;
    doc = "caps the wave size; 0 means uncapped";
    flakeOption = true;
    intKind = "nonneg";
    boxEnv = false;
  };
  continuousDispatch = {
    env = "CONTINUOUS_DISPATCH";
    group = "dispatch";
    default = false;
    kind = "bool";
    alias = "continuous";
    doc = "when enabled, dispatch runs as a long-running slot-refill loop instead of a single wave (#527): as each Box finishes, the launcher re-discovers the queue and refills the freed slot when the image-freshness probe (#526) reports fresh; a rebuild-needed result stops refilling, lets in-flight Boxes finish, and exits with the new documented code (see the exit-code table in docs/reference.md's Dogfood loop section, under Termination). Off by default; applies to queue discovery only — ISSUE_NUMBER-claimed and selective dispatch ignore it";
    flakeOption = true;
    nixSubPath = "continuous.enable";
    boxEnv = false;
  };
  overlapGate = {
    env = "OVERLAP_GATE";
    group = "dispatch";
    default = "defer";
    doc = "declared ## Touches overlap policy: defer (hold a Dispatchable issue whose declared touch-set intersects an InProgress issue's, retrying once the collider completes), off (disable the check)";
    choices = [
      "defer"
      "off"
    ];
    flakeOption = true;
    boxEnv = false;
  };
  mergePollInterval = {
    env = "MERGE_POLL_INTERVAL";
    group = "git";
    default = 180;
    doc = "seconds between merge-gate poll iterations";
    flakeOption = true;
    intKind = "nonneg";
    nixSubPath = "merge.pollInterval";
    boxEnv = false;
  };
  mergePollTimeout = {
    env = "MERGE_POLL_TIMEOUT";
    group = "git";
    default = 3600;
    doc = "total seconds to wait for CI green before abandoning the merge attempt";
    flakeOption = true;
    intKind = "nonneg";
    nixSubPath = "merge.pollTimeout";
    boxEnv = false;
  };
  spindriftPromptDir = {
    env = "SPINDRIFT_PROMPT_DIR";
    group = "agents";
    flag = "prompt-dir";
    doc = "host directory mounted over /agent/prompts for zero-rebuild prompt iteration";
    flakeOption = true;
    legacySettingsExempt = true;
    nixSubPath = "promptDir";
    boxEnv = false;
  };
  spindriftSkillsDir = {
    env = "SPINDRIFT_SKILLS_DIR";
    group = "agents";
    flag = "skills-dir";
    doc = "host directory mounted read-only over /home/agent/.claude/skills so the headless agent can load operator-provided skills";
    boxEnv = false;
  };
  autoFormat = {
    env = "AUTO_FORMAT";
    group = "agents";
    default = false;
    kind = "bool";
    doc = "when enabled, the implementor auto-detects and runs the project's formatter on changed files before each commit; skips silently when no formatter is found";
    flakeOption = true;
    nixSubPath = "format.enable";
    boxEnv = true;
    boxEnvOnly = true;
  };
  autoLint = {
    env = "AUTO_LINT";
    group = "agents";
    default = false;
    kind = "bool";
    doc = "when enabled, the implementor auto-detects and runs the project's linter on changed files before each commit, applying auto-fix then resolving remaining findings; skips silently when no linter is found";
    flakeOption = true;
    nixSubPath = "lint.enable";
    boxEnv = true;
    boxEnvOnly = true;
  };
  orchestratorEnabled = {
    env = "ORCHESTRATOR_ENABLED";
    group = "dispatch";
    flag = "orchestrator";
    default = false;
    kind = "bool";
    doc = "master feature-flag switch (issue #1996; canonicalized #2047): when enabled, forks entrypoint.sh's rendered prompt/--agents JSON onto the orchestrator-on path -- the implementor pass hands off to the in-box Go orchestrator instead of calling driver-exec directly, and every other orchestrator-conditioned fork (e.g. the filer's write-mechanism gate) reads this same switch; off by default, the direct driver-exec path is unchanged; the off-path is legacy, slated for demolition once this defaults on in production with a sustained A/B win (ADR 0035 amendment)";
    flakeOption = true;
    nixSubPath = "orchestrator.enable";
    boxEnv = true;
    boxEnvOnly = true;
  };
  issueNumber = {
    env = "ISSUE_NUMBER";
    group = "issues";
    alias = "issue";
    doc = "dispatch only this issue number, bypassing the LABEL query; empty discovers by LABEL";
    boxEnv = false;
  };
  holdJitterSecs = {
    env = "HOLD_JITTER_SECS";
    group = "dispatch";
    default = 5;
    doc = "jitter seconds added to 429 hold duration to spread re-dispatch";
    flakeOption = true;
    intKind = "nonneg";
    nixSubPath = "retry.holdJitter";
    # Forwarded into the Box for the outcome-backstop verb's push-retry
    # jitter (issue #2157); see maxRebaseAttempts.
    boxEnv = true;
  };
  transientBackoffSecs = {
    env = "TRANSIENT_BACKOFF_SECS";
    group = "dispatch";
    default = 30;
    doc = "base backoff seconds per retry for 529/overloaded and network transients";
    flakeOption = true;
    intKind = "positive";
    nixSubPath = "retry.transientBackoff";
    # Forwarded into the Box for the outcome-backstop verb's push-retry
    # linear backoff unit (issue #2157); see maxRebaseAttempts.
    boxEnv = true;
  };
  transientRetryMax = {
    env = "TRANSIENT_RETRY_MAX";
    group = "dispatch";
    default = 3;
    doc = "max retries for transient exits (529/network backoff; consecutive 429 holds)";
    flakeOption = true;
    intKind = "positive";
    nixSubPath = "retry.transientMax";
    boxEnv = false;
  };
}
