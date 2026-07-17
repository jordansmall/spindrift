# Registry of every runtime env knob the harness exposes.  One entry per knob;
# generators in mkHarness.nix and flakeModule.nix derive ALL per-knob output from
# this single source — adding an entry here propagates to preambles, flakeModule
# options, the entrypoint defaults block, BOX_ENV_VARS, and harness.env.example
# without further edits.
#
# Fields (all optional except env, doc, and group on non-secret knobs):
#   env          string  env-var name (SCREAMING_SNAKE_CASE)
#   group        string  category heading for the full flag reference (--help --all
#                        and the man page); required on every non-secret knob and
#                        must match a heading in lib/renderers.nix's groupOrder
#   alias        string  optional short-form CLI flag alias (kebab-case, no dashes);
#                        when set, --<alias> is a second way to set the same knob
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
    group = "Issue discovery";
    default = "ready-for-agent";
    doc = "issues carrying this label are dispatchable (the launch button)";
    flakeOption = true;
    boxEnv = false;
  };
  issueTracker = {
    env = "ISSUE_TRACKER";
    group = "Issue discovery";
    default = "github";
    doc = "IssueTracker backend (ADR 0013): github (gh-exec, default), local (private Markdown + YAML frontmatter files; see LOCAL_ISSUES_DIR), or jira (see JIRA_BASE_URL/JIRA_PROJECT_KEY/JIRA_TOKEN); the Code Forge (PR/CI/merge) stays github regardless";
    choices = [
      "github"
      "local"
      "jira"
    ];
    flakeOption = true;
    boxEnv = false;
  };
  localIssuesDir = {
    env = "LOCAL_ISSUES_DIR";
    group = "Issue discovery";
    default = ".spindrift/issues";
    doc = "directory scanned for issue files when ISSUE_TRACKER=local; keep it git-ignored so breakout issues stay private";
    flakeOption = true;
    boxEnv = false;
  };
  baseBranch = {
    env = "BASE_BRANCH";
    group = "Branches & merge";
    default = "main";
    doc = "default branch agent PRs merge into";
    flakeOption = true;
    boxEnv = true;
  };
  maxParallel = {
    env = "MAX_PARALLEL";
    group = "Concurrency & dependency waves";
    default = 3;
    doc = "maximum concurrent agent containers";
    flakeOption = true;
    boxEnv = false;
  };
  branchPrefix = {
    env = "BRANCH_PREFIX";
    group = "Branches & merge";
    default = "agent/issue-";
    doc = "prefix for agent-cut branches";
    flakeOption = true;
    boxEnv = true;
  };
  inProgressLabel = {
    env = "IN_PROGRESS_LABEL";
    group = "Lifecycle labels";
    default = "agent-in-progress";
    doc = "label swapped on from LABEL when an issue enters the queue";
    flakeOption = true;
    boxEnv = true;
  };
  failedLabel = {
    env = "FAILED_LABEL";
    group = "Lifecycle labels";
    default = "agent-failed";
    doc = "label swapped on when the agent box exits non-zero";
    flakeOption = true;
    boxEnv = false;
  };
  completeLabel = {
    env = "COMPLETE_LABEL";
    group = "Lifecycle labels";
    default = "agent-complete";
    doc = "label the launcher swaps on when CI reaches green (agent is done; merge is separate)";
    flakeOption = true;
    boxEnv = true;
  };
  mergeMode = {
    env = "MERGE_MODE";
    group = "Branches & merge";
    default = "manual";
    doc = "post-green merge policy: immediate (merge on green), auto (enqueue GitHub native auto-merge; repo must have Allow auto-merge enabled), manual (leave PR open for human approval)";
    choices = [
      "immediate"
      "auto"
      "manual"
    ];
    flakeOption = true;
    boxEnv = false;
  };
  mergeGuardPaths = {
    env = "MERGE_GUARD_PATHS";
    group = "Branches & merge";
    default = ".github/**,**/CLAUDE.md,**/AGENTS.md,.claude/**,.opencode/**";
    doc = "comma-separated globs matched against every changed path (added, modified, deleted); a hit downgrades the merge to manual regardless of MERGE_MODE and posts a PR comment naming the match; empty disables the guard (github Code Forge merge path only)";
    flakeOption = true;
    boxEnv = false;
  };
  model = {
    env = "MODEL";
    group = "Models";
    default = "claude-sonnet-5";
    doc = "primary (implementor) Claude model for the agent (zero-rebuild runtime switch)";
    flakeOption = true;
    boxEnv = true;
    boxEnvOnly = true;
  };
  scoutModel = {
    env = "SCOUT_MODEL";
    group = "Models";
    default = "claude-haiku-4-5-20251001";
    doc = "scout subagent model tier; empty omits the scout entry from --agents; the flag itself is omitted only when no subagent model is set";
    flakeOption = true;
    boxEnv = true;
    boxEnvOnly = true;
  };
  reviewModel = {
    env = "REVIEW_MODEL";
    group = "Models";
    default = "claude-opus-4-8";
    doc = "reviewer subagent model tier; empty omits the reviewer entry from --agents; the flag itself is omitted only when no subagent model is set";
    flakeOption = true;
    boxEnv = true;
    boxEnvOnly = true;
  };
  filerModel = {
    env = "FILER_MODEL";
    group = "Models";
    default = "";
    doc = "filer subagent model tier; empty (default) omits the filer entry from --agents and means the filer is not provisioned at all — setting a model is the opt-in (recommended: claude-haiku-4-5-20251001)";
    flakeOption = true;
    boxEnv = true;
    boxEnvOnly = true;
  };
  devShellName = {
    env = "DEV_SHELL_NAME";
    group = "Sandbox & resources";
    default = "default";
    doc = "which devShell to enter; lets a Target expose a lean headless ci shell distinct from a heavy interactive default";
    flakeOption = true;
    boxEnv = true;
    boxEnvOnly = true;
  };
  devShellProbeTimeout = {
    env = "DEV_SHELL_PROBE_TIMEOUT";
    group = "Sandbox & resources";
    default = 300;
    doc = "seconds before the devShell probe is abandoned and the baked toolchain is used";
    flakeOption = true;
    boxEnv = true;
    boxEnvOnly = true;
  };
  podmanNetwork = {
    env = "PODMAN_NETWORK";
    group = "Sandbox & resources";
    doc = "--network value for podman run; empty applies no flag (podman NAT default); set to 'pasta' to restrict egress";
    flakeOption = true;
    boxEnv = false;
  };
  bwrapUnshareNet = {
    env = "BWRAP_UNSHARE_NET";
    group = "Sandbox & resources";
    doc = "when non-empty, adds --unshare-net to bwrap; requires slirp/pasta for DNS; by default bwrap shares the host network namespace (host-loopback reachable)";
    flakeOption = true;
    boxEnv = false;
  };
  memoryLimit = {
    env = "MEMORY_LIMIT";
    group = "Sandbox & resources";
    # #712: a single `nix build .#checks-inbox` peaks near 3.7 GiB RSS
    # (agent-issue-640 dmesg); 4g left ~300MiB headroom and got cgroup
    # OOM-killed. 5g gives real headroom above the observed peak.
    default = "5g";
    doc = "max memory per agent container (--memory); empty string disables the limit";
    flakeOption = true;
    boxEnv = false;
  };
  pidsLimit = {
    env = "PIDS_LIMIT";
    group = "Sandbox & resources";
    default = "512";
    doc = "max processes per agent container (--pids-limit); empty string disables the limit";
    flakeOption = true;
    boxEnv = false;
  };
  jiraBaseURL = {
    env = "JIRA_BASE_URL";
    group = "Repository & identity";
    doc = "Jira site base URL (e.g. https://yourcompany.atlassian.net); required when ISSUE_TRACKER=jira";
    flakeOption = true;
    boxEnv = false;
  };
  jiraProjectKey = {
    env = "JIRA_PROJECT_KEY";
    group = "Repository & identity";
    doc = "Jira project key issues are read from (e.g. ENG); required when ISSUE_TRACKER=jira";
    flakeOption = true;
    boxEnv = false;
  };
  jiraEmail = {
    env = "JIRA_EMAIL";
    group = "Repository & identity";
    doc = "Jira Cloud account email, paired with JIRA_TOKEN for Basic auth; leave empty for Bearer-token auth (Jira Server/Data Center PATs)";
    flakeOption = true;
    boxEnv = false;
  };
  jiraStatusMapping = {
    env = "JIRA_STATUS_MAPPING";
    group = "Lifecycle labels";
    default = "";
    doc = "JSON object mapping dispatch states (dispatchable, inProgress, complete, failed) to native Jira status names, e.g. {'inProgress':'In Progress'}; TransitionState performs the matching workflow transition, falling back to swapping the matching lifecycle label when a state is unmapped or its transition is blocked by the project's workflow";
    flakeOption = true;
    boxEnv = false;
  };
  jiraIncludeComments = {
    env = "JIRA_INCLUDE_COMMENTS";
    group = "Issue discovery";
    doc = "when non-empty, the Jira adapter appends the issue's comment thread to the description it returns; empty (default) keeps the prompt-injection surface tight";
    flakeOption = true;
    boxEnv = false;
  };
  # ── Required runtime inputs ────────────────────────────────────────────────
  repoSlug = {
    env = "REPO_SLUG";
    group = "Repository & identity";
    required = true;
    placeholder = "owner/repo";
    doc = "target GitHub repository the agents work on";
    flakeOption = true;
    boxEnv = true;
  };
  ghToken = {
    env = "GH_TOKEN";
    required = true;
    secret = true;
    placeholder = "fake-token";
    doc = "fine-grained PAT scoped to the target repo — Contents/PR/Issues/Metadata RW";
    boxEnv = true;
  };
  claudeOAuthToken = {
    env = "CLAUDE_CODE_OAUTH_TOKEN";
    secret = true;
    placeholder = "fake-oauth";
    doc = "Claude Code OAuth token (run 'claude setup-token'); set this or ANTHROPIC_API_KEY";
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
    group = "Repository & identity";
    placeholder = "Test Bot";
    doc = "commit identity name; falls back to host git config user.name";
    flakeOption = true;
    boxEnv = true;
  };
  gitUserEmail = {
    env = "GIT_USER_EMAIL";
    group = "Repository & identity";
    placeholder = "bot@example.com";
    doc = "commit identity email; falls back to host git config user.email";
    flakeOption = true;
    boxEnv = true;
  };
  codeForge = {
    env = "CODE_FORGE";
    group = "Repository & identity";
    default = "github";
    doc = "code-landing backend: github (open PR, watch CI, merge) or git (push-only to CODE_FORGE_REMOTE_URL; no PR, CI-watch, or merge gate)";
    choices = [
      "github"
      "git"
    ];
    flakeOption = true;
    boxEnv = true;
  };
  codeForgeRemoteURL = {
    env = "CODE_FORGE_REMOTE_URL";
    group = "Repository & identity";
    doc = "plain git remote URL to clone from and push to (self-hosted git, gitea, GitLab-without-MRs, a bare server repo); required when CODE_FORGE=git, unused otherwise";
    flakeOption = true;
    boxEnv = true;
  };
  # ── Operator-tunable knobs (flakeOption = true; also tune via harness.env) ─
  maxFixAttempts = {
    env = "MAX_FIX_ATTEMPTS";
    group = "Self-healing & retries";
    default = 3;
    doc = "fix-agent passes when CI is genuinely red before marking agent-failed; 0 disables self-healing";
    flakeOption = true;
    boxEnv = false;
  };
  maxRebaseAttempts = {
    env = "MAX_REBASE_ATTEMPTS";
    group = "Self-healing & retries";
    default = 3;
    doc = "rebase-and-retry passes when a green PR conflicts with the base after a sibling merge; 0 disables rebase retries";
    flakeOption = true;
    boxEnv = false;
  };
  preflightStaleBase = {
    env = "PREFLIGHT_STALE_BASE";
    group = "Self-healing & retries";
    default = false;
    doc = "when non-empty, the launcher proactively rebases a green PR that is behind its base (no textual conflict) before merging and re-waits for CI on the rebased tree, drawing on MAX_REBASE_ATTEMPTS for its budget (ADR 0026). Off by default: a green-but-behind PR merges as-is, relying on its green CI as the landing gate — this trades the rare cross-PR semantic break ADR 0026 guarded against (two individually-green PRs that break combined) for the throughput of parallel landings that never wait on an extra rebase+CI cycle. WARNING: enabling this on a highly-parallelized fleet without a merge queue in front of the branch invites near-constant rebase+re-CI thrashing (each landing leaves the others behind again), burning CI minutes and tokens — see the Stale-base preflight docs";
    flakeOption = true;
    boxEnv = false;
  };
  maxJobs = {
    env = "MAX_JOBS";
    group = "Concurrency & dependency waves";
    default = 0;
    doc = "caps the wave size; 0 means uncapped";
    flakeOption = true;
    boxEnv = false;
  };
  continuousDispatch = {
    env = "CONTINUOUS_DISPATCH";
    group = "Concurrency & dependency waves";
    default = false;
    doc = "when non-empty, dispatch runs as a long-running slot-refill loop instead of a single wave (#527): as each Box finishes, the launcher re-discovers the queue and refills the freed slot when the image-freshness probe (#526) reports fresh; a rebuild-needed result stops refilling, lets in-flight Boxes finish, and exits with the new documented code (see README's exit-code table). Off by default; applies to queue discovery only — ISSUE_NUMBER-claimed and selective dispatch ignore it";
    flakeOption = true;
    boxEnv = false;
  };
  overlapGate = {
    env = "OVERLAP_GATE";
    group = "Concurrency & dependency waves";
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
    group = "Branches & merge";
    default = 30;
    doc = "seconds between merge-gate poll iterations";
    flakeOption = true;
    boxEnv = false;
  };
  mergePollTimeout = {
    env = "MERGE_POLL_TIMEOUT";
    group = "Branches & merge";
    default = 1800;
    doc = "total seconds to wait for CI green before abandoning the merge attempt";
    flakeOption = true;
    boxEnv = false;
  };
  spindriftPromptDir = {
    env = "SPINDRIFT_PROMPT_DIR";
    group = "Prompt & skill iteration";
    doc = "host directory mounted over /agent/prompts for zero-rebuild prompt iteration";
    boxEnv = false;
  };
  spindriftSkillsDir = {
    env = "SPINDRIFT_SKILLS_DIR";
    group = "Prompt & skill iteration";
    doc = "host directory mounted read-only over /home/agent/.claude/skills so the headless agent can load operator-provided skills";
    boxEnv = false;
  };
  autoFormat = {
    env = "AUTO_FORMAT";
    group = "Prompt & skill iteration";
    default = false;
    doc = "when non-empty, the implementor auto-detects and runs the project's formatter on changed files before each commit; skips silently when no formatter is found";
    flakeOption = true;
    boxEnv = true;
    boxEnvOnly = true;
  };
  autoLint = {
    env = "AUTO_LINT";
    group = "Prompt & skill iteration";
    default = false;
    doc = "when non-empty, the implementor auto-detects and runs the project's linter on changed files before each commit, applying auto-fix then resolving remaining findings; skips silently when no linter is found";
    flakeOption = true;
    boxEnv = true;
    boxEnvOnly = true;
  };
  issueNumber = {
    env = "ISSUE_NUMBER";
    group = "Issue discovery";
    alias = "issue";
    doc = "dispatch only this issue number, bypassing the LABEL query; empty discovers by LABEL";
    boxEnv = false;
  };
  holdJitterSecs = {
    env = "HOLD_JITTER_SECS";
    group = "Self-healing & retries";
    default = 5;
    doc = "jitter seconds added to 429 hold duration to spread re-dispatch";
    flakeOption = true;
    boxEnv = false;
  };
  transientBackoffSecs = {
    env = "TRANSIENT_BACKOFF_SECS";
    group = "Self-healing & retries";
    default = 30;
    doc = "base backoff seconds per retry for 529/overloaded and network transients";
    flakeOption = true;
    boxEnv = false;
  };
  transientRetryMax = {
    env = "TRANSIENT_RETRY_MAX";
    group = "Self-healing & retries";
    default = 3;
    doc = "max retries for transient exits (529/network backoff; consecutive 429 holds)";
    flakeOption = true;
    boxEnv = false;
  };
}
