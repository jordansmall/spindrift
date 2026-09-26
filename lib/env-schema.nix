# Registry of every runtime env knob. Generators in lib/mkHarness.nix and
# lib/flakeModule.nix derive all per-knob output from these entries, so one new
# entry reaches the preambles, flake options, entrypoint defaults, BOX_ENV_VARS,
# and harness.env.example with no further edit. Field semantics live with their
# consumers: lib/renderers.nix, lib/nixpath.nix, nix/checks/schema-drift.nix.
let
  backends = import ./backends/default.nix;
in
{
  label = {
    env = "LABEL";
    group = "issues";
    flag = "dispatch-label";
    default = "ready-for-agent";
    doc = "issues carrying this label are dispatchable";
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
    # Forwarded into the Box (issue #1429): lib/fragments.nix's PR-body
    # reference gates pick their case from the tracker. The launcher reads it
    # directly too, so no boxEnvOnly.
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
    doc = "when enabled and ISSUE_TRACKER=local, the PR body includes a non-auto-closing `Local-issue: <slug>` breadcrumb; off (default) keeps the private local ticket slug out of the PR body; ISSUE_TRACKER=github is unaffected and keeps `Closes #ISSUE_NUMBER` either way";
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
    doc = "label the launcher swaps on when CI turns green; the agent is done at that point, and merging is a separate step";
    flakeOption = true;
    nixSubPath = "labels.complete";
    boxEnv = true;
  };
  mergeMode = {
    env = "MERGE_MODE";
    group = "git";
    flag = "merge-policy";
    default = "manual";
    doc = "post-green merge policy: immediate (merge on green), auto (enqueue GitHub's native auto-merge; the repo must have Allow auto-merge enabled), manual (leave the PR open for human approval)";
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
    doc = "how the launcher brings a behind branch current before landing: rebase (linear history) or merge (merge the base in); applies to both the preflight-stale-base proactive sync and the reactive on-conflict sync during an immediate merge (github Code Forge PR-landing path only)";
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
    doc = "reasoning-effort level for the main (coordinator) agent, changeable at runtime without a rebuild; passed through without normalization, so the value must be valid for the active Driver: claude accepts low/medium/high/xhigh/max (appended as --effort <level>), and opencode's cross-provider variant selector accepts a provider-specific set (appended as --variant <level>); unset emits no argument for either Driver, which keeps the Driver's own default effort";
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
    doc = "Claude model for the main (coordinator) agent, changeable at runtime without a rebuild; worker-tier defaults are unaffected";
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
    doc = "reviewer subagent model tier; empty omits the reviewer entry from --agents; the flag itself is omitted only when no subagent model is set. DEPRECATED for non-orchestrator use: superseded by the byName/roster options (agents.models.byName for a one-agent override, agents.models.roster for the full list; see docs/reference.md). Under ORCHESTRATOR, the code-owned review pass replaces the roster reviewer entry and takes its model from this value, captured before the roster entry is deleted and falling back to the coordinator model when unset. An explicit dispatch-time value (REVIEW_MODEL=... / --review-model ...) overrides even that baked value on an already-built image, with no rebuild (issue #3171). Precedence is the dispatch-time env first, then the baked roster reviewer entry, then the coordinator model; unset or empty at dispatch leaves the baked chain unchanged.";
    flakeOption = true;
    nixSubPath = "models.review";
    boxEnv = true;
    boxEnvOnly = true;
  };
  reviewEffort = {
    env = "REVIEW_EFFORT";
    group = "agents";
    doc = "value for the --effort flag of the orchestrator's code-owned review pass (issue #2387); passed through without normalization, with the same accepted values as EFFORT for the active Driver. Overrides the roster reviewer entry's own effort (rosterDefaults.reviewer.effort by default), whether the roster is the built-in default or an explicit Consumer-supplied one (lib/mkHarness.nix applies the override after normalization, issue #2512). Empty follows the roster; a non-empty value overrides it. An explicit roster arg always wins over the four legacy per-agent model knobs (scoutModel/reviewModel/filerModel/workerModel), but this override applies whatever the roster source (lib/roster.nix). Meaningful only under ORCHESTRATOR: the resolved value reaches the orchestrator through the prompt-assembly Handoff's ReviewEffort field (issue #2512), the same way ReviewModel does. Like REVIEW_MODEL, an explicit dispatch-time value (REVIEW_EFFORT=... / --review-effort ...) overrides the baked value on an already-built image, with no rebuild (issue #3171). Precedence is the dispatch-time env first, then the baked roster reviewer entry, then the coordinator fallback; unset or empty at dispatch leaves the baked chain unchanged (see MIGRATING.md).";
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
    doc = "filer subagent model tier; empty (default) omits the filer entry from --agents and leaves the filer unprovisioned; setting a model opts in (recommended: claude-haiku-4-5-20251001). DEPRECATED: superseded by the byName/roster options (agents.models.byName for a one-agent override, agents.models.roster for the full list; see docs/reference.md); these per-agent knobs still work but will be removed.";
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
    doc = "seconds to wait for the devShell probe before falling back to the baked toolchain";
    flakeOption = true;
    nixSubPath = "devShell.probeTimeout";
    boxEnv = true;
    boxEnvOnly = true;
  };
  networkMode = {
    env = "NETWORK_MODE";
    group = "infra";
    default = "open";
    doc = "Box network posture, rendered into the right flag or syntax for each runtime/OCI backend. 'open' (default) puts bwrap in its own network namespace behind a hardened pasta helper, which gives working egress with host loopback blocked, matching rootless podman (issue #2666); on OCI it applies no flag and leaves the runtime's own default network. 'host' is a bwrap-only opt-out that restores the pre-#2666 shared host network namespace; on OCI it renders nothing, the same no-flag default as 'open', a harmless no-op. 'no-host-loopback' keeps internet egress but denies host loopback on podman (renders pasta, no --map-gw); on docker/nerdctl it renders their own default bridge network, which is correct but does not yet deny host loopback there; on runtime=bwrap it is unsupported and eval throws, since it would render nothing different from the isolated-by-default 'open'. 'none' is fully offline and documented as test-only, since a Driver can't reach its Provider under it. Mutually exclusive at eval time with the raw PODMAN_NETWORK/BWRAP_UNSHARE_NET escape-hatch knobs (network.podman/network.bwrapUnshare); setting both throws, and there is no precedence rule";
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
    # `kind` opts this flag into presence parsing (issue #2145): it takes
    # `--bwrap-unshare-net` bare or `--bwrap-unshare-net=<value>`, but never a
    # space-separated value.
    kind = "bool";
    doc = "when non-empty, forces bwrap's network-namespace isolation on (pasta-backed since issue #2666, no longer DNS-breaking); redundant with the isolate-by-default 'open' posture, and the one pairing where it would matter, NETWORK_MODE=host, fails nix eval (see network.mode)";
    flakeOption = true;
    nixSubPath = "network.bwrapUnshare";
    boxEnv = false;
  };
  memoryLimit = {
    env = "MEMORY_LIMIT";
    group = "infra";
    # #712: one `nix build .#checks-inbox` peaks near 3.7 GiB RSS, so a 4g cap
    # got OOM-killed; 5g leaves real headroom. #2379: the cap only matters where
    # podman runs inside a fixed-RAM VM (macOS/Windows), which is why
    # nix/dogfood-defaults.nix leaves it unset on native Linux.
    default = "5g";
    doc = "max memory per agent Box: hard --memory cap under OCI; under bwrap, a per-Box cgroup v2 memory.max when the host delegates a writable cgroup subtree, else best-effort (warns and runs uncapped, ADR 0042); empty string disables the limit";
    flakeOption = true;
    nixSubPath = "limits.memory";
    boxEnv = false;
    emptyDisables = true;
  };
  pidsLimit = {
    env = "PIDS_LIMIT";
    group = "infra";
    default = "512";
    doc = "max processes per agent Box: hard --pids-limit cap under OCI; under bwrap, a per-Box cgroup v2 pids.max when delegation is available, else best-effort (warns and runs uncapped, ADR 0042); empty string disables the limit";
    flakeOption = true;
    nixSubPath = "limits.pids";
    boxEnv = false;
    emptyDisables = true;
  };
  registryProxyRoutesFile = {
    env = "REGISTRY_PROXY_ROUTES_FILE";
    group = "infra";
    doc = "path to a TOML routes file declaring registry routes (ADR 0045). Each route binds match-host and serves the whole matched host under its prefix. Host-rooted serving (ADR 0047) is the only serving model: the route forwards the verbatim remaining path to the upstream origin, and the proxy checks every request unconditionally against a path-set the launcher derives host-side from the Target repo's own committed registry config at dispatch time. A request outside that derived set gets a 403 naming the policy and the derived set, before any upstream dial and with no credential attached. There is no off switch; allow (below) is the only recourse for a path shape the derived set misses. A route therefore needs a snapshot of the Target repo's committed config to derive from, and the launch fails naming the route when none is available. Under CODE_FORGE=local that snapshot is the bare Accumulation repo's own BASE_BRANCH ref (ADR 0033), read host-side before any Box starts and never from a checkout around the launcher's cwd, so a dirty or unpushed working tree can neither widen nor narrow the derived set. Under every other Code Forge it is a Target-repo checkout resolved from the launcher's cwd and positively identified against the forge's own remote (ADR 0047 #3310). Optional auth-scheme: bearer (default), basic, or header:<Name>. Optional upstream-origin: an absolute http(s) origin, meaning scheme://host with an optional port and no path, query, fragment, or userinfo; anything else is rejected at parse time with an error naming the route. It overrides the origin the launcher would otherwise derive from the Target repo's committed config, for the two cases that config cannot always supply on its own: a non-default scheme or an explicit port, and a host serving only ecosystems with no committed in-tree config to derive an origin from. A declared origin replaces only the derived origin, never the derived path-set, so a route on a host where the Target repo does declare a registry still enforces exactly what was derived for it. A route declaring upstream-origin for a host the Target repo declares nothing on resolves on that key alone and enforces exactly its own allow plus its own declared paths; when it declares none, that is the empty set, and therefore default-deny (ADR 0047 #3261). Optional [routes.ecosystems.<name>] blocks, one per ecosystem this route serves, where <name> is an ecosystem spindrift knows; an unknown name is rejected at parse time with an error naming the route and the name (ADR 0048 #3405). Every block accepts path, the one typed key every ecosystem row shares, validated by the same canonical-path rules the retired per-ecosystem path keys used: an absolute path already in canonical form (a leading slash, no trailing slash, no '.', '..', or empty segment), not the bare root '/', and containing no dollar sign, backtick, or backslash; anything else is rejected with an error naming the route. Beyond path, the ecosystem's own row validates any further key its block accepts. The one such key today is cargo's registries, which names the Target repo's [registries.NAME] entries this route serves, each restricted to letters, digits, '-', and '_'. The cargo binding to a named registry uses source replacement rendered into the Box's $CARGO_HOME/config.toml, keyed off the repo's own un-rewritten .cargo/config.toml, not a rewrite of that file. When registries is present it restricts which of the repo's declared registries get a replacement stanza on this route; when absent, every declared registry whose index host matches this route gets one. The resulting CARGO_REGISTRIES_<PROXY-SOURCE-NAME>_TOKEN placeholder is keyed to cargo's own replacement source name, not the repo's registry name, because cargo binds credential lookups to the replacement source once a source is replaced (ADR 0044 #3201 amendment). When the repo's own .cargo/config.toml already claims a declared registry's index URL under its own [source.NAME], the render reuses that name instead of minting spindrift-upstream-<name>, so the stanzas merge instead of colliding. On a repo that also replaces crates-io with that source, a plain crates-io fetch chains from crates-io to that named source to this route, so the crates-io route's own prefix may see no traffic and this route's credential and enforcement policy govern those fetches (ADR 0044 #3248 amendment). The gradle ecosystem has no committed in-tree config to derive a path from, so an operator declares gradle's path in its block directly; omitting gradle's block leaves its binding inert, with no repository redirection at all, and gradle falls back to whatever repositories the build declares itself. The go ecosystem likewise has no committed in-tree config to derive a path from, because a go.mod names no registry host, so an operator declares go's path in its block directly. The declared path joins the route's enforced path-set as operator-owned policy, and when present GOPROXY is bound to the Forwarder URL carrying that full path. Omitting go's block leaves go unbound through that route, with no GOPROXY export at all, so go falls back to its own default public proxy, not to a bare-root URL nobody declared for it. Optional allow: empty in the normal case; a list of extra path patterns that extend a route's derived enforced path-set for a path shape the Target repo's committed config doesn't expose, e.g. an Artifactory-style sibling download endpoint (ADR 0047 #3258). The parser validates each pattern, which must already be in canonical absolute-path form (a leading slash, no trailing slash, and no '.' or '..' segment), and rejects anything else with an error naming the route and the offending pattern. It rejects the bare root pattern '/' too, since that would authorize the whole host, which is exactly the off switch this key must never be. A request matching an allow pattern forwards with the route's credential exactly like a request matching a derived path, through the same 403-or-forward check and code path. allow only ever widens the enforced set and never disables or narrows enforcement; there is no enforce = false, and this key doesn't add one. Optional credential source reference, exactly one when present; a route with no credential key is unauthenticated, a plain pass-through. A match-host and a credential alone make a complete route. A routes file that still declares a key retired by ADR 0047 (#3261) or ADR 0048 (#3405), namely upstream-base-url, enforce-allowlist, cargo-registries, gradle-path, or go-path, is rejected at parse time with an error naming the route and the retired key(s). The error prints the equivalent replacement stanza, a [[routes]] entry for the first two and a [routes.ecosystems.<name>] block for the other three, so migrating is one mechanical edit. The file carries credential source REFERENCES (env var names, file paths), never secret values. The proxy strips the inbound Authorization header from every proxied request before attaching any route credential (ADR 0047). Unset disables the registry proxy entirely. This file is the only place to declare registry routes. The proxy routes strictly by a per-route path prefix slugged from each route's match-host, and refuses a request under no known prefix before dialing any upstream";
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
    doc = "JSON object mapping dispatch states (dispatchable, inProgress, complete, failed) to native Jira status names, e.g. {'inProgress':'In Progress'}; TransitionState performs the matching workflow transition, falling back to swapping the matching lifecycle label when a state is unmapped or the project's workflow blocks its transition";
    flakeOption = true;
    nixSubPath = "jira.statusMapping";
    boxEnv = false;
  };
  jiraIncludeComments = {
    env = "JIRA_INCLUDE_COMMENTS";
    group = "issues";
    default = false;
    kind = "bool";
    doc = "when enabled, the Jira adapter appends the issue's comment thread to the description it returns; off (default) keeps comment text, a prompt-injection path, out of the agent prompt";
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
    doc = "JSON array of research verdict objects [{verdict,label,description}], order preserved, defining the research dispatch's verdict vocabulary and each verdict's terminal label (ADR 0022); empty (default) uses the built-in three verdicts and their labels from lib/research-verdicts.nix's defaultVerdicts, with no behavior change. The launcher validates the posted verdict against this set and applies the mapped label on Settle, and the research prompt's verdict contract is rendered from it";
    flakeOption = true;
    legacySettingsExempt = true;
    nixSubPath = "research.verdicts";
    boxEnv = false;
  };
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
    doc = "fine-grained PAT scoped to the target repo with Contents/PR/Issues/Metadata RW; required unless CODE_FORGE and ISSUE_TRACKER are both local";
    boxEnv = true;
  };
  ghTokenRefreshFile = {
    env = "GH_TOKEN_REFRESH_FILE";
    group = "forge";
    doc = "path to a file the launcher re-reads and swaps into GH_TOKEN whenever its content changes. An external minter (e.g. a workflow step re-minting a GitHub App installation token, which keeps the App private key in the workflow instead of the launcher) can then keep the credential fresh across a run that outlives the token's ~1h lifetime (#1027); empty (default) leaves GH_TOKEN static for the whole run";
    flakeOption = true;
    boxEnv = false;
  };
  boxGhToken = {
    env = "BOX_GH_TOKEN";
    secret = true;
    doc = "opt-in two-actor separation (ADR 0016): a second machine user's fine-grained PAT for the Box only. The launcher keeps using its own GH_TOKEN for merges, labels, and all host-side forge calls, while the Box receives this value as its GH_TOKEN instead; empty (default) leaves the single-token flow unchanged. Pair it with a repository ruleset that bars this user from updating the base branch and bypass-lists only the launcher's user; see docs/reference.md's two-actor separation recipe";
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
    doc = "opencode github-copilot Provider auth store (JSON), the whole auth slice opencode reads natively (ADR 0009 amendment, #260); the github-copilot Provider is OAuth-only. Mint once on a host with 'opencode auth login -p github-copilot', then export the github-copilot slice of ~/.local/share/opencode/auth.json (exact jq recipe in docs/reference.md). Required when DRIVER=opencode and MODEL=github-copilot/<model>; ignored under the claude Driver";
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
    doc = "opt-in two-actor separation (ADR 0016 analog): a second machine user's Forgejo PAT for the Box only. The launcher keeps using its own FORGEJO_TOKEN for merges, labels, and all host-side forge calls, while the Box receives this value as its FORGEJO_TOKEN instead; empty (default) leaves the single-token flow unchanged. Pair it with a Forgejo branch-protection rule that bars this user from updating the base branch and allows only the launcher's user to push and merge; see docs/reference.md's Forgejo two-actor separation recipe";
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
    doc = "code-landing backend: github (open PR, watch CI, merge), git (push-only to CODE_FORGE_REMOTE_URL; no PR, CI-watch, or merge gate), local (host-mediated landing onto the Accumulation repo's Integration branch by rebase and fast-forward, never a merge commit; no PR, CI-watch, or network; ADR 0033, issue #1889), or forgejo (push-only to a Forgejo/Gitea instance authenticated by FORGEJO_TOKEN; agent branch, rebase, and merge under MERGE_MODE, no PR support yet; ADR 0038)";
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
    doc = "host path to the bare Accumulation repo (ADR 0033), which the Box mounts read-only and the launcher lands into host-side; when CODE_FORGE=local it defaults to .spindrift/accum.git under the launcher's working directory (created and seeded automatically), and an explicit value overrides that default; unused otherwise";
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
    doc = "whether the Box writes to the Code Forge and Issue Tracker directly (read-write) or the launcher makes every write host-side on its behalf (read-only), a third axis independent of CODE_FORGE and ISSUE_TRACKER (issue #1914). At nix build (Consumer eval) time, nix checks read-only against the selected forge's and tracker's registry capability bits (issue #2526): it is permitted only when the forge implements bundle-relay and host-side draft-PR creation and the tracker implements host-posted comments, and otherwise the build throws naming the missing seam. The local, github, and forgejo backends all pass the check today (ADR extending 0032/0033 to github, docs/adr/0034-host-mediated-github-forge-and-issue-access.md). The launcher's own startup gate now only catches a runtime override of this value that bypasses the nix validation";
    choices = [
      "read-write"
      "read-only"
    ];
    flakeOption = true;
    nixSubPath = "boxAccess";
    # Forwarded into the Box, but the prompt fragments gate on the derived
    # BOX_WRITE_ENABLED signal rather than this raw value (issue #1951), so an
    # unset or garbled value can never fall open into the write-capable path.
    # The launcher reads this knob directly too, so no boxEnvOnly.
    boxEnv = true;
  };
  maxFixAttempts = {
    env = "MAX_FIX_ATTEMPTS";
    group = "dispatch";
    default = 3;
    doc = "fix-agent passes to run on a real CI failure before the issue is marked agent-failed; 0 disables self-healing";
    flakeOption = true;
    intKind = "nonneg";
    nixSubPath = "retry.maxFix";
    boxEnv = false;
  };
  signalCarrier = {
    env = "BOX_SIGNAL_CARRIER";
    group = "dispatch";
    default = "log";
    doc = "transport for the three mid-run signal channels (comment, PR intent, issue intent) between the launcher and the Box: 'log' (default) keeps the existing behaviour for every Consumer, nonce-guarded marker lines in the Box log; 'socket' sends those channels over the launcher-owned Signal socket instead (ADR 0052, docs/adr/0052-mid-run-signals-cross-the-seam-over-a-launcher-socket.md); requesting 'socket' where the transport cannot work is a startup error, never a silent fallback to 'log'. Not a flakeOption: the launcher reads this runtime-only knob itself, so it stays out of the Launcher input document's settings map. ChildCommand's withoutKeys(s.Env, s.Knobs) filter (cmd/launcher/internal/daemon/command.go) passes it through the daemon's own child environment instead of stripping it.";
    choices = [
      "log"
      "socket"
    ];
    # In-box prompt assembly (cmd/launcher/internal/promptassembly) reads this
    # knob to select the log- or socket-variant of each signal fragment, so it
    # must reach the Box environment too (issue #3726).
    boxEnv = true;
  };
  maxRebaseAttempts = {
    env = "MAX_REBASE_ATTEMPTS";
    group = "dispatch";
    default = 3;
    doc = "rebase-and-retry passes when a green PR conflicts with the base after a sibling merge; 0 disables rebase retries";
    flakeOption = true;
    intKind = "nonneg";
    nixSubPath = "retry.maxRebase";
    # Forwarded into the Box so the driver-exec outcome-backstop verb takes its
    # push-retry bound from launcher-delivered plumbing rather than a
    # hand-copied default (issue #2157).
    boxEnv = true;
  };
  maxBudgetTokens = {
    env = "MAX_BUDGET_TOKENS";
    group = "dispatch";
    default = 0;
    doc = "cumulative token cap across every attempt dispatched so far, counting the initial run, every fix pass, and any retried attempt within each (issue #2575). At the cap, selfHealGate stops dispatching further fix passes (issue #2001); the value is also forwarded into the Box, where the orchestrator's own review loop then commits to a terminal land pass instead of another BLOCK-triggered review round (issue #2694); 0 disables the token budget cap";
    flakeOption = true;
    intKind = "nonneg";
    nixSubPath = "budget.tokens";
    # Forwarded into the Box (issue #2694): the in-Box orchestrator's review
    # loop caps its cumulative token spend against this same bound.
    boxEnv = true;
  };
  maxBudgetUSD = {
    # The default is a float, not the bare int 0: lib/flakeModule.nix infers the
    # Consumer-facing option type from builtins.isInt, and types.int would
    # reject the fractional caps this knob exists for. Falling through to
    # types.str instead means a Consumer flake sets it quoted, e.g. "4.44".
    env = "MAX_BUDGET_USD";
    group = "dispatch";
    default = 0.0;
    doc = "cumulative cost cap in USD across every attempt dispatched so far, counting the initial run, every fix pass, and any retried attempt within each (issue #2575). At the cap, selfHealGate stops dispatching further fix passes (issue #2001); the value is also forwarded into the Box, where the orchestrator's own review loop then commits to a terminal land pass instead of another BLOCK-triggered review round (issue #2694); 0 disables the cost budget cap; give it as a quoted string in flake settings since it may be fractional, e.g. 4.44";
    flakeOption = true;
    nixSubPath = "budget.usd";
    # Forwarded into the Box (issue #2694): the in-Box orchestrator's review
    # loop caps its cumulative USD spend against this same bound.
    boxEnv = true;
  };
  preflightStaleBase = {
    env = "PREFLIGHT_STALE_BASE";
    group = "git";
    default = false;
    kind = "bool";
    doc = "when enabled, the launcher rebases a green PR that is behind its base (no textual conflict) before merging and waits for CI again on the rebased tree, drawing its budget from MAX_REBASE_ATTEMPTS (ADR 0026). Off by default: a green-but-behind PR merges as-is, with its green CI as the landing gate. That accepts the rare cross-PR semantic break ADR 0026 guarded against (two PRs that are each green but break when combined) in exchange for parallel landings that never wait on an extra rebase and CI cycle. WARNING: enabling this on a highly parallel fleet without a merge queue in front of the branch causes near-constant rebase and re-CI churn (each landing leaves the others behind again), burning CI minutes and tokens; see the Stale-base preflight docs";
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
    doc = "when enabled, dispatch runs as a long-running slot-refill loop instead of a single wave (#527): as each Box finishes, the launcher re-discovers the queue and refills the freed slot when the image-freshness probe (#526) reports fresh; a rebuild-needed result stops refilling, lets in-flight Boxes finish, and exits with the new documented code (see the exit-code table in docs/reference.md's Dispatch exit codes section). Off by default; applies to queue discovery only, so ISSUE_NUMBER-claimed and selective dispatch ignore it. DEPRECATED: superseded by the daemon (apps.daemon, nix run .#daemon), which runs one single-Box launcher invocation per slot, each pinned to its own fetched revision, instead of one long-lived launcher process holding the whole pool (#3547). It is not removed: it stays available for operators who want no daemon at all, and it remains the Console's engine";
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
  daemonApp = {
    env = "DAEMON_APP";
    group = "dispatch";
    default = ".#";
    doc = "flake app attribute the daemon re-invokes for each child Dispatch, pinned to the fetched revision; this is the Consumer's own CLI app, e.g. .# or .#dogfood-bwrap; read by the daemon only, the launcher itself ignores it";
    flakeOption = true;
    launcherIgnores = true;
    # Postdates the ADR 0037 Pass 2 freeze -- never had a settings.<section>
    # alias to preserve, so no lib/legacy-settings-section.nix row.
    legacySettingsExempt = true;
    boxEnv = false;
  };
  daemonAwakeWindow = {
    env = "DAEMON_AWAKE_WINDOW";
    group = "dispatch";
    default = "";
    doc = "daily local-time span the daemon is allowed to start a new Box, as 'HH:MM-HH:MM IANA-zone', e.g. '22:00-06:00 Europe/London'; an end before the start wraps past midnight; empty (default) means always awake; it gates only the start of a Box, and a Box already running finishes regardless; the zone is explicit and never inherited from the host; read by the daemon only, the launcher itself ignores it";
    flakeOption = true;
    launcherIgnores = true;
    # Postdates the ADR 0037 Pass 2 freeze -- never had a settings.<section>
    # alias to preserve, so no lib/legacy-settings-section.nix row.
    legacySettingsExempt = true;
    boxEnv = false;
  };
  daemonSelfApp = {
    env = "DAEMON_SELF_APP";
    group = "dispatch";
    default = ".#daemon";
    doc = "flake app attribute of the daemon itself, which the daemon evaluates at each fetched tip so it can halt when its own build changed; distinct from DAEMON_APP, the child Dispatch app; a Consumer that re-exports the daemon under another attribute name (e.g. .#dogfood-bwrap-daemon) must set this to match; read by the daemon only, the launcher itself ignores it";
    flakeOption = true;
    launcherIgnores = true;
    # Postdates the ADR 0037 Pass 2 freeze -- never had a settings.<section>
    # alias to preserve, so no lib/legacy-settings-section.nix row.
    legacySettingsExempt = true;
    boxEnv = false;
  };
  daemonIdleFloor = {
    env = "DAEMON_IDLE_FLOOR";
    group = "dispatch";
    default = "5m";
    doc = "wait before the daemon's first no-work check against a kind, and the poll interval while a jammed kind is backing off; each further consecutive no-work check against that kind doubles the wait, up to DAEMON_IDLE_CAP; a Go time.ParseDuration string, validated by the daemon at startup; read by the daemon only, the launcher itself ignores it";
    flakeOption = true;
    launcherIgnores = true;
    # Postdates the ADR 0037 Pass 2 freeze -- never had a settings.<section>
    # alias to preserve, so no lib/legacy-settings-section.nix row.
    legacySettingsExempt = true;
    boxEnv = false;
  };
  daemonIdleCap = {
    env = "DAEMON_IDLE_CAP";
    group = "dispatch";
    default = "30m";
    doc = "ceiling the daemon's per-kind idle backoff doubles up to, starting from DAEMON_IDLE_FLOOR; a Go time.ParseDuration string, validated by the daemon at startup; read by the daemon only, the launcher itself ignores it";
    flakeOption = true;
    launcherIgnores = true;
    # Postdates the ADR 0037 Pass 2 freeze -- never had a settings.<section>
    # alias to preserve, so no lib/legacy-settings-section.nix row.
    legacySettingsExempt = true;
    boxEnv = false;
  };
  daemonFailureBackoff = {
    env = "DAEMON_FAILURE_BACKOFF";
    group = "dispatch";
    default = "1m";
    doc = "how long a slot waits after an unclassified child failure before refilling itself; a Go time.ParseDuration string, validated by the daemon at startup; read by the daemon only, the launcher itself ignores it";
    flakeOption = true;
    launcherIgnores = true;
    # Postdates the ADR 0037 Pass 2 freeze -- never had a settings.<section>
    # alias to preserve, so no lib/legacy-settings-section.nix row.
    legacySettingsExempt = true;
    boxEnv = false;
  };
  daemonBreakerThreshold = {
    env = "DAEMON_BREAKER_THRESHOLD";
    group = "dispatch";
    default = 5;
    doc = "pool-wide unclassified failures within DAEMON_BREAKER_WINDOW that trip the circuit breaker and halt the whole daemon; a positive integer, validated by the daemon at startup; read by the daemon only, the launcher itself ignores it";
    flakeOption = true;
    launcherIgnores = true;
    intKind = "positive";
    # Postdates the ADR 0037 Pass 2 freeze -- never had a settings.<section>
    # alias to preserve, so no lib/legacy-settings-section.nix row.
    legacySettingsExempt = true;
    boxEnv = false;
  };
  daemonBreakerWindow = {
    env = "DAEMON_BREAKER_WINDOW";
    group = "dispatch";
    default = "15m";
    doc = "trailing window the circuit breaker counts DAEMON_BREAKER_THRESHOLD unclassified failures within; a Go time.ParseDuration string, validated by the daemon at startup; read by the daemon only, the launcher itself ignores it";
    flakeOption = true;
    launcherIgnores = true;
    # Postdates the ADR 0037 Pass 2 freeze -- never had a settings.<section>
    # alias to preserve, so no lib/legacy-settings-section.nix row.
    legacySettingsExempt = true;
    boxEnv = false;
  };
  researchReservation = {
    env = "RESEARCH_RESERVATION";
    group = "dispatch";
    default = 1;
    doc = "how many of the daemon's MAX_PARALLEL slots prefer research Dispatches over work. It is a floor, not a ceiling: those slots take research only while research has queued work, and either kind can fill the whole pool when the other has backed off after an empty result; 0 is work-first with research on the leftovers, and a value equal to MAX_PARALLEL is research-first; read by the daemon only, the launcher itself ignores it, and inert when the daemon is restricted to one kind by its positional verb; the daemon rejects a value above MAX_PARALLEL at startup. The default is not tuned; issue #3541 left the final default out of scope";
    flakeOption = true;
    launcherIgnores = true;
    intKind = "nonneg";
    # Postdates the ADR 0037 Pass 2 freeze -- never had a settings.<section>
    # alias to preserve, so no lib/legacy-settings-section.nix row.
    legacySettingsExempt = true;
    boxEnv = false;
  };
  mergePollInterval = {
    env = "MERGE_POLL_INTERVAL";
    group = "git";
    default = 30;
    doc = "seconds between merge-gate poll iterations. Raising it does not reduce rate-limit use: the gate sends one strictly serialized GraphQL query at a time, and only while a PR is landing (the pollers whose cadence matters are the continuous refill ticker and the console backlog poll). The interval is also reused as four fixed-call-count delays (SUCCESS confirm, check-registration window, merge-blocked retry, fix-pass confirm), which stretch with it for no rate-limit benefit; see docs/reference.md before raising it (issue #3249)";
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
    doc = "master feature flag (issue #1996; canonicalized #2047): when enabled, entrypoint.sh renders its prompt and --agents JSON for the orchestrator path, where the implementor pass hands off to the in-box Go orchestrator instead of calling driver-exec directly; every other orchestrator-conditioned branch (e.g. the filer's write-mechanism gate) reads this same switch. Off by default, which leaves the direct driver-exec path unchanged. The off path is legacy and slated for removal once this defaults on in production with a sustained A/B win (ADR 0035 amendment)";
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
    doc = "jitter seconds added to the 429 hold duration to spread out re-dispatches";
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
    doc = "base backoff seconds per retry for 529/overloaded and transient network errors";
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
