# Registry of every runtime env knob the harness exposes.  One entry per knob;
# generators in mkHarness.nix and flakeModule.nix derive ALL per-knob output from
# this single source — adding an entry here propagates to preambles, flakeModule
# options, the entrypoint defaults block, BOX_ENV_VARS, and harness.env.example
# without further edits.
#
# Fields (all optional except env, doc, and group on non-secret knobs):
#   env          string  env-var name (SCREAMING_SNAKE_CASE)
#   group        string  one of the six domains (agents, git, issues, forge,
#                        dispatch, infra), matching the first segment of the
#                        knob's nixPath; required on every non-secret knob and
#                        must still match a heading in lib/renderers.nix's
#                        groupOrder
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
{
  # ── Consumer-tunable (flakeOption = true) ──────────────────────────────────
  label = {
    env = "LABEL";
    group = "issues";
    flag = "dispatch-label";
    default = "ready-for-agent";
    doc = "issues carrying this label are dispatchable (the launch button)";
    flakeOption = true;
    nixPath = "issues.labels.dispatch";
    boxEnv = false;
  };
  issueTracker = {
    env = "ISSUE_TRACKER";
    group = "issues";
    flag = "tracker";
    default = "github";
    doc = "IssueTracker backend (ADR 0013): github (gh-exec, default), local (private Markdown + YAML frontmatter files; see LOCAL_ISSUES_DIR), or jira (see JIRA_BASE_URL/JIRA_PROJECT_KEY/JIRA_TOKEN); the Code Forge (PR/CI/merge) stays github regardless";
    choices = [
      "github"
      "local"
      "jira"
    ];
    flakeOption = true;
    nixPath = "issues.tracker";
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
    nixPath = "issues.localDir";
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
    nixPath = "issues.localReference";
    boxEnv = true;
    boxEnvOnly = true;
  };
  baseBranch = {
    env = "BASE_BRANCH";
    group = "git";
    default = "main";
    doc = "default branch agent PRs merge into";
    flakeOption = true;
    nixPath = "git.baseBranch";
    boxEnv = true;
  };
  maxParallel = {
    env = "MAX_PARALLEL";
    group = "dispatch";
    default = 3;
    doc = "maximum concurrent agent containers";
    flakeOption = true;
    nixPath = "dispatch.maxParallel";
    boxEnv = false;
  };
  branchPrefix = {
    env = "BRANCH_PREFIX";
    group = "git";
    default = "agent/issue-";
    doc = "prefix for agent-cut branches";
    flakeOption = true;
    nixPath = "git.branchPrefix";
    boxEnv = true;
  };
  inProgressLabel = {
    env = "IN_PROGRESS_LABEL";
    group = "issues";
    default = "agent-in-progress";
    doc = "label swapped on from LABEL when an issue enters the queue";
    flakeOption = true;
    nixPath = "issues.labels.inProgress";
    boxEnv = true;
  };
  failedLabel = {
    env = "FAILED_LABEL";
    group = "issues";
    default = "agent-failed";
    doc = "label swapped on when the agent box exits non-zero";
    flakeOption = true;
    nixPath = "issues.labels.failed";
    boxEnv = false;
  };
  completeLabel = {
    env = "COMPLETE_LABEL";
    group = "issues";
    default = "agent-complete";
    doc = "label the launcher swaps on when CI reaches green (agent is done; merge is separate)";
    flakeOption = true;
    nixPath = "issues.labels.complete";
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
    nixPath = "git.merge.policy";
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
    nixPath = "git.merge.method";
    boxEnv = false;
  };
  mergeGuardPaths = {
    env = "MERGE_GUARD_PATHS";
    group = "git";
    default = ".github/**,**/CLAUDE.md,**/AGENTS.md,.claude/**,.opencode/**";
    doc = "comma-separated globs matched against every changed path (added, modified, deleted); a hit downgrades the merge to manual regardless of MERGE_MODE and posts a PR comment naming the match; empty disables the guard (github Code Forge merge path only)";
    flakeOption = true;
    nixPath = "git.merge.guardPaths";
    boxEnv = false;
  };
  model = {
    env = "MODEL";
    group = "agents";
    default = "claude-opus-4-8";
    doc = "main/coordinator Claude model for the agent (zero-rebuild runtime switch); worker-tier defaults are unaffected";
    flakeOption = true;
    nixPath = "agents.models.default";
    boxEnv = true;
    boxEnvOnly = true;
  };
  scoutModel = {
    env = "SCOUT_MODEL";
    group = "agents";
    default = "claude-haiku-4-5-20251001";
    doc = "scout subagent model tier; empty omits the scout entry from --agents; the flag itself is omitted only when no subagent model is set. DEPRECATED: superseded by the roster option (see docs/reference.md); these per-agent knobs still work but will be removed.";
    flakeOption = true;
    nixPath = "agents.models.scout";
    boxEnv = true;
    boxEnvOnly = true;
  };
  reviewModel = {
    env = "REVIEW_MODEL";
    group = "agents";
    default = "claude-opus-4-8";
    doc = "reviewer subagent model tier; empty omits the reviewer entry from --agents; the flag itself is omitted only when no subagent model is set. DEPRECATED: superseded by the roster option (see docs/reference.md); these per-agent knobs still work but will be removed.";
    flakeOption = true;
    nixPath = "agents.models.review";
    boxEnv = true;
    boxEnvOnly = true;
  };
  filerModel = {
    env = "FILER_MODEL";
    group = "agents";
    default = "";
    doc = "filer subagent model tier; empty (default) omits the filer entry from --agents and means the filer is not provisioned at all — setting a model is the opt-in (recommended: claude-haiku-4-5-20251001). DEPRECATED: superseded by the roster option (see docs/reference.md); these per-agent knobs still work but will be removed.";
    flakeOption = true;
    nixPath = "agents.models.filer";
    boxEnv = true;
    boxEnvOnly = true;
  };
  workerModel = {
    env = "WORKER_MODEL";
    group = "agents";
    default = "claude-sonnet-5";
    doc = "implement-capable worker subagent model tier; empty omits the worker entry from --agents; the implementor prompt does not delegate to it yet — this only provisions the subagent so it is invokable. DEPRECATED: superseded by the roster option (see docs/reference.md); these per-agent knobs still work but will be removed.";
    flakeOption = true;
    nixPath = "agents.models.worker";
    boxEnv = true;
    boxEnvOnly = true;
  };
  devShellName = {
    env = "DEV_SHELL_NAME";
    group = "infra";
    default = "default";
    doc = "which devShell to enter; lets a Target expose a lean headless ci shell distinct from a heavy interactive default";
    flakeOption = true;
    nixPath = "infra.devShell.name";
    boxEnv = true;
    boxEnvOnly = true;
  };
  devShellProbeTimeout = {
    env = "DEV_SHELL_PROBE_TIMEOUT";
    group = "infra";
    default = 300;
    doc = "seconds before the devShell probe is abandoned and the baked toolchain is used";
    flakeOption = true;
    nixPath = "infra.devShell.probeTimeout";
    boxEnv = true;
    boxEnvOnly = true;
  };
  podmanNetwork = {
    env = "PODMAN_NETWORK";
    group = "infra";
    doc = "--network value for podman run; empty applies no flag (podman NAT default); set to 'pasta' to restrict egress";
    flakeOption = true;
    nixPath = "infra.network.podman";
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
    doc = "when non-empty, adds --unshare-net to bwrap; requires slirp/pasta for DNS; by default bwrap shares the host network namespace (host-loopback reachable)";
    flakeOption = true;
    nixPath = "infra.network.bwrapUnshare";
    boxEnv = false;
  };
  memoryLimit = {
    env = "MEMORY_LIMIT";
    group = "infra";
    # #712: a single `nix build .#checks-inbox` peaks near 3.7 GiB RSS
    # (agent-issue-640 dmesg); 4g left ~300MiB headroom and got cgroup
    # OOM-killed. 5g gives real headroom above the observed peak.
    default = "5g";
    doc = "max memory per agent container (--memory); empty string disables the limit";
    flakeOption = true;
    nixPath = "infra.limits.memory";
    boxEnv = false;
  };
  pidsLimit = {
    env = "PIDS_LIMIT";
    group = "infra";
    default = "512";
    doc = "max processes per agent container (--pids-limit); empty string disables the limit";
    flakeOption = true;
    nixPath = "infra.limits.pids";
    boxEnv = false;
  };
  jiraBaseURL = {
    env = "JIRA_BASE_URL";
    group = "issues";
    doc = "Jira site base URL (e.g. https://yourcompany.atlassian.net); required when ISSUE_TRACKER=jira";
    flakeOption = true;
    nixPath = "issues.jira.baseURL";
    boxEnv = false;
  };
  jiraProjectKey = {
    env = "JIRA_PROJECT_KEY";
    group = "issues";
    doc = "Jira project key issues are read from (e.g. ENG); required when ISSUE_TRACKER=jira";
    flakeOption = true;
    nixPath = "issues.jira.projectKey";
    boxEnv = false;
  };
  jiraEmail = {
    env = "JIRA_EMAIL";
    group = "issues";
    doc = "Jira Cloud account email, paired with JIRA_TOKEN for Basic auth; leave empty for Bearer-token auth (Jira Server/Data Center PATs)";
    flakeOption = true;
    nixPath = "issues.jira.email";
    boxEnv = false;
  };
  jiraStatusMapping = {
    env = "JIRA_STATUS_MAPPING";
    group = "issues";
    default = "";
    doc = "JSON object mapping dispatch states (dispatchable, inProgress, complete, failed) to native Jira status names, e.g. {'inProgress':'In Progress'}; TransitionState performs the matching workflow transition, falling back to swapping the matching lifecycle label when a state is unmapped or its transition is blocked by the project's workflow";
    flakeOption = true;
    nixPath = "issues.jira.statusMapping";
    boxEnv = false;
  };
  jiraIncludeComments = {
    env = "JIRA_INCLUDE_COMMENTS";
    group = "issues";
    default = false;
    kind = "bool";
    doc = "when enabled, the Jira adapter appends the issue's comment thread to the description it returns; off (default) keeps the prompt-injection surface tight";
    flakeOption = true;
    nixPath = "issues.jira.includeComments";
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
    nixPath = "forge.repoSlug";
    boxEnv = true;
  };
  ghToken = {
    env = "GH_TOKEN";
    required = true;
    secret = true;
    placeholder = "fake-token";
    doc = "fine-grained PAT scoped to the target repo — Contents/PR/Issues/Metadata RW; required unless CODE_FORGE and ISSUE_TRACKER are both local";
    boxEnv = true;
  };
  ghTokenRefreshFile = {
    env = "GH_TOKEN_REFRESH_FILE";
    group = "forge";
    doc = "path to a file the launcher re-reads and swaps into GH_TOKEN whenever its content changes — lets an external minter (e.g. a workflow step re-minting a GitHub App installation token, keeping the App private key in the workflow rather than the launcher) keep the credential fresh across a run that outlives the token's ~1h lifetime (#1027); empty (default) leaves GH_TOKEN static for the whole run";
    flakeOption = true;
    nixPath = "forge.ghTokenRefreshFile";
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
    placeholder = "fake-oauth";
    doc = "Claude Code OAuth token (run 'claude setup-token'); set this or ANTHROPIC_API_KEY";
    boxEnv = true;
  };
  opencodeAuthContent = {
    env = "OPENCODE_AUTH_CONTENT";
    secret = true;
    doc = "opencode github-copilot Provider auth store (JSON) — the whole auth slice opencode reads natively (ADR 0009 amendment, #260); the github-copilot Provider is OAuth-only. Mint once on a host with 'opencode auth login -p github-copilot', then export the github-copilot slice of ~/.local/share/opencode/auth.json (exact jq recipe in docs/reference.md). Required when DRIVER=opencode and MODEL=github-copilot/<model>; ignored under the claude Driver";
    boxEnv = true;
  };
  anthropicAPIKey = {
    env = "ANTHROPIC_API_KEY";
    secret = true;
    doc = "Anthropic API key; set this or CLAUDE_CODE_OAUTH_TOKEN";
    boxEnv = true;
  };
  jiraToken = {
    env = "JIRA_TOKEN";
    secret = true;
    doc = "Jira API token (Cloud: paired with JIRA_EMAIL for Basic auth; Server/Data Center: used alone as a Bearer PAT); required when ISSUE_TRACKER=jira";
    boxEnv = false;
  };
  gitUserName = {
    env = "GIT_USER_NAME";
    group = "git";
    flag = "user-name";
    placeholder = "Test Bot";
    doc = "commit identity name; falls back to host git config user.name";
    flakeOption = true;
    nixPath = "git.user.name";
    boxEnv = true;
  };
  gitUserEmail = {
    env = "GIT_USER_EMAIL";
    group = "git";
    flag = "user-email";
    placeholder = "bot@example.com";
    doc = "commit identity email; falls back to host git config user.email";
    flakeOption = true;
    nixPath = "git.user.email";
    boxEnv = true;
  };
  codeForge = {
    env = "CODE_FORGE";
    group = "forge";
    flag = "forge-backend";
    default = "github";
    doc = "code-landing backend: github (open PR, watch CI, merge), git (push-only to CODE_FORGE_REMOTE_URL; no PR, CI-watch, or merge gate), or local (host-mediated landing onto the Accumulation repo's Integration branch by rebase and fast-forward, never a merge commit; no PR, CI-watch, or network; ADR 0033, issue #1889)";
    choices = [
      "github"
      "git"
      "local"
    ];
    flakeOption = true;
    nixPath = "forge.backend";
    boxEnv = true;
  };
  codeForgeRemoteURL = {
    env = "CODE_FORGE_REMOTE_URL";
    group = "forge";
    flag = "remote-url";
    doc = "plain git remote URL to clone from and push to (self-hosted git, gitea, GitLab-without-MRs, a bare server repo); required when CODE_FORGE=git, unused otherwise";
    flakeOption = true;
    nixPath = "forge.remoteURL";
    boxEnv = true;
  };
  codeForgeAccumulationRepoDir = {
    env = "CODE_FORGE_ACCUMULATION_REPO_DIR";
    group = "forge";
    flag = "accumulation-repo-dir";
    doc = "host path to the bare Accumulation repo (ADR 0033), mounted read-only into the Box and landed into host-side; when CODE_FORGE=local, defaults to .spindrift/accum.git under the launcher's working directory (auto-created and seeded) and an explicit value still overrides it; unused otherwise";
    flakeOption = true;
    nixPath = "forge.accumulationRepoDir";
    boxEnv = false;
  };
  boxForgeAndIssueAccess = {
    env = "BOX_FORGE_AND_ISSUE_ACCESS";
    group = "forge";
    flag = "box-access";
    default = "read-write";
    doc = "whether the Box writes to the Code Forge and Issue Tracker directly (read-write) or the launcher host-mediates every write instead (read-only), a third axis orthogonal to CODE_FORGE and ISSUE_TRACKER (issue #1914); read-only is gated at startup by capability — permitted only when the selected forge implements bundle-relay and host-side draft-PR-create and the selected tracker implements host-posted comments, otherwise the launcher exits with a startup error naming the missing seam; local and github backends both satisfy the gate today (ADR extending 0032/0033 to github, docs/adr/0034-host-mediated-github-forge-and-issue-access.md)";
    choices = [
      "read-write"
      "read-only"
    ];
    flakeOption = true;
    nixPath = "forge.boxAccess";
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
    nixPath = "dispatch.retry.maxFix";
    boxEnv = false;
  };
  maxRebaseAttempts = {
    env = "MAX_REBASE_ATTEMPTS";
    group = "dispatch";
    default = 3;
    doc = "rebase-and-retry passes when a green PR conflicts with the base after a sibling merge; 0 disables rebase retries";
    flakeOption = true;
    nixPath = "dispatch.retry.maxRebase";
    # Forwarded into the Box so the driver-exec outcome-backstop verb reads
    # this bound for its own best-effort push retry from launcher-delivered
    # plumbing rather than a hand-copied default (issue #2157).
    boxEnv = true;
  };
  maxBudgetTokens = {
    env = "MAX_BUDGET_TOKENS";
    group = "dispatch";
    default = 0;
    doc = "cumulative tokens across the initial run and every fix pass before selfHealGate stops dispatching further fix passes (issue #2001); 0 disables the token budget cap";
    flakeOption = true;
    nixPath = "dispatch.budget.tokens";
    boxEnv = false;
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
    doc = "cumulative cost in USD across the initial run and every fix pass before selfHealGate stops dispatching further fix passes (issue #2001); 0 disables the cost budget cap; give it as a quoted string in flake settings since it may be fractional, e.g. 4.44";
    flakeOption = true;
    nixPath = "dispatch.budget.usd";
    boxEnv = false;
  };
  preflightStaleBase = {
    env = "PREFLIGHT_STALE_BASE";
    group = "git";
    default = false;
    kind = "bool";
    doc = "when enabled, the launcher proactively rebases a green PR that is behind its base (no textual conflict) before merging and re-waits for CI on the rebased tree, drawing on MAX_REBASE_ATTEMPTS for its budget (ADR 0026). Off by default: a green-but-behind PR merges as-is, relying on its green CI as the landing gate — this trades the rare cross-PR semantic break ADR 0026 guarded against (two individually-green PRs that break combined) for the throughput of parallel landings that never wait on an extra rebase+CI cycle. WARNING: enabling this on a highly-parallelized fleet without a merge queue in front of the branch invites near-constant rebase+re-CI thrashing (each landing leaves the others behind again), burning CI minutes and tokens — see the Stale-base preflight docs";
    flakeOption = true;
    nixPath = "git.merge.preflightStaleBase";
    boxEnv = false;
  };
  maxJobs = {
    env = "MAX_JOBS";
    group = "dispatch";
    default = 0;
    doc = "caps the wave size; 0 means uncapped";
    flakeOption = true;
    nixPath = "dispatch.maxJobs";
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
    nixPath = "dispatch.continuous.enable";
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
    nixPath = "dispatch.overlapGate";
    boxEnv = false;
  };
  mergePollInterval = {
    env = "MERGE_POLL_INTERVAL";
    group = "git";
    default = 30;
    doc = "seconds between merge-gate poll iterations";
    flakeOption = true;
    nixPath = "git.merge.pollInterval";
    boxEnv = false;
  };
  mergePollTimeout = {
    env = "MERGE_POLL_TIMEOUT";
    group = "git";
    default = 1800;
    doc = "total seconds to wait for CI green before abandoning the merge attempt";
    flakeOption = true;
    nixPath = "git.merge.pollTimeout";
    boxEnv = false;
  };
  spindriftPromptDir = {
    env = "SPINDRIFT_PROMPT_DIR";
    group = "agents";
    flag = "prompt-dir";
    doc = "host directory mounted over /agent/prompts for zero-rebuild prompt iteration";
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
    nixPath = "agents.format.enable";
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
    nixPath = "agents.lint.enable";
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
    nixPath = "dispatch.orchestrator.enable";
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
    nixPath = "dispatch.retry.holdJitter";
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
    nixPath = "dispatch.retry.transientBackoff";
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
    nixPath = "dispatch.retry.transientMax";
    boxEnv = false;
  };
}
