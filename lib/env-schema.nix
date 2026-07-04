# Registry of every runtime env knob the harness exposes.  One entry per knob;
# generators in mkHarness.nix and flakeModule.nix derive ALL per-knob output from
# this single source — adding an entry here propagates to preambles, flakeModule
# options, the entrypoint defaults block, BOX_ENV_VARS, and harness.env.example
# without further edits.
#
# Fields (all optional except env and doc):
#   env          string  env-var name (SCREAMING_SNAKE_CASE)
#   default      any     baked-in default; absent means runtime-required or empty
#   placeholder  string  friendly value shown in harness.env.example for required
#                        non-secret vars (e.g. REPO_SLUG=owner/repo)
#   required     bool    runtime-required (no sensible default; validate() aborts)
#   secret       bool    never a flakeOption; shown as an empty placeholder in example
#   doc          string  one-line description rendered into harness.env.example
#   flakeOption  bool    consumer-tunable via the flakeModule declarative surface
#   boxEnv       bool    forwarded from the launcher host into the Box container
{
  # ── Consumer-tunable (flakeOption = true) ──────────────────────────────────
  label = {
    env = "LABEL";
    default = "ready-for-agent";
    doc = "issues carrying this label are dispatchable (the launch button)";
    flakeOption = true;
    boxEnv = false;
  };
  baseBranch = {
    env = "BASE_BRANCH";
    default = "main";
    doc = "default branch agent PRs merge into";
    flakeOption = true;
    boxEnv = true;
  };
  maxParallel = {
    env = "MAX_PARALLEL";
    default = 3;
    doc = "maximum concurrent agent containers";
    flakeOption = true;
    boxEnv = false;
  };
  branchPrefix = {
    env = "BRANCH_PREFIX";
    default = "agent/issue-";
    doc = "prefix for agent-cut branches";
    flakeOption = true;
    boxEnv = true;
  };
  inProgressLabel = {
    env = "IN_PROGRESS_LABEL";
    default = "agent-in-progress";
    doc = "label swapped on from LABEL when an issue enters the queue";
    flakeOption = true;
    boxEnv = true;
  };
  failedLabel = {
    env = "FAILED_LABEL";
    default = "agent-failed";
    doc = "label swapped on when the agent box exits non-zero";
    flakeOption = true;
    boxEnv = false;
  };
  completeLabel = {
    env = "COMPLETE_LABEL";
    default = "agent-complete";
    doc = "label the launcher swaps on after a successful merge";
    flakeOption = true;
    boxEnv = true;
  };
  model = {
    env = "MODEL";
    default = "claude-opus-4-8";
    doc = "primary Claude model for the agent (zero-rebuild runtime switch)";
    flakeOption = true;
    boxEnv = true;
  };
  scoutModel = {
    env = "SCOUT_MODEL";
    default = "";
    doc = "scout subagent model tier; empty omits --agents from the claude invocation";
    flakeOption = true;
    boxEnv = true;
  };
  reviewModel = {
    env = "REVIEW_MODEL";
    default = "";
    doc = "reviewer subagent model tier; empty omits --agents from the claude invocation";
    flakeOption = true;
    boxEnv = true;
  };
  # ── Required runtime inputs ────────────────────────────────────────────────
  repoSlug = {
    env = "REPO_SLUG";
    required = true;
    placeholder = "owner/repo";
    doc = "target GitHub repository the agents work on";
    boxEnv = true;
  };
  ghToken = {
    env = "GH_TOKEN";
    required = true;
    secret = true;
    doc = "fine-grained PAT scoped to the target repo — Contents/PR/Issues/Metadata RW";
    boxEnv = true;
  };
  claudeOAuthToken = {
    env = "CLAUDE_CODE_OAUTH_TOKEN";
    secret = true;
    doc = "Claude Code OAuth token (run 'claude setup-token'); set this or ANTHROPIC_API_KEY";
    boxEnv = true;
  };
  anthropicAPIKey = {
    env = "ANTHROPIC_API_KEY";
    secret = true;
    doc = "Anthropic API key; set this or CLAUDE_CODE_OAUTH_TOKEN";
    boxEnv = true;
  };
  gitUserName = {
    env = "GIT_USER_NAME";
    doc = "commit identity name; falls back to host git config user.name";
    boxEnv = true;
  };
  gitUserEmail = {
    env = "GIT_USER_EMAIL";
    doc = "commit identity email; falls back to host git config user.email";
    boxEnv = true;
  };
  # ── Runtime-only knobs (no flakeOption; tune via harness.env) ─────────────
  maxFixAttempts = {
    env = "MAX_FIX_ATTEMPTS";
    default = 3;
    doc = "fix-agent passes when CI is genuinely red before marking agent-failed; 0 disables self-healing";
    boxEnv = false;
  };
  maxJobs = {
    env = "MAX_JOBS";
    default = 0;
    doc = "dependency-wave concurrency cap; 0 means unlimited";
    boxEnv = false;
  };
  depsPollSecs = {
    env = "DEPS_POLL_SECS";
    default = 30;
    doc = "seconds between dependency-wave poll iterations";
    boxEnv = false;
  };
  depsWaitSecs = {
    env = "DEPS_WAIT_SECS";
    default = 7200;
    doc = "total seconds to wait for dependency-wave completion before aborting";
    boxEnv = false;
  };
  mergePollInterval = {
    env = "MERGE_POLL_INTERVAL";
    default = 30;
    doc = "seconds between merge-gate poll iterations";
    boxEnv = false;
  };
  mergePollTimeout = {
    env = "MERGE_POLL_TIMEOUT";
    default = 1800;
    doc = "total seconds to wait for CI green before abandoning the merge attempt";
    boxEnv = false;
  };
  spindriftPromptDir = {
    env = "SPINDRIFT_PROMPT_DIR";
    doc = "host directory mounted over /agent/prompts for zero-rebuild prompt iteration";
    boxEnv = false;
  };
}
