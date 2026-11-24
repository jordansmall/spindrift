# The jira backend row: data plus the env knobs it owns (issues #5633,
# #5635). The registry (./default.nix) documents the fields and fixes this
# row's order; lib/row-knobs.nix folds `knobs` into lib/env-schema.nix.
{
  name = "jira";
  goVar = "Jira";
  displayName = "Jira";
  validAsTracker = true;
  connectorOnly = true;
  tokenEnvVar = "JIRA_TOKEN";
  # The core knob a jira Connector row names as its Basic identity (issue #7501).
  identityEnvVar = "JIRA_EMAIL";
  consumerStanza = {
    connectorKnobs = [ ];
    flakeInput = ''
      jira = {
        url = "github:jordansmall/spindrift?dir=connectors/jira";
        inputs.core.follows = "spindrift";
      };
    '';
    moduleImport = "inputs.jira.flakeModules.default";
  };
  # The knob holding the Tracker scope (ADR 0075 decision 3); a row without one
  # scopes by REPO_SLUG.
  trackerScopeKnob = "jiraProjectKey";
  doctorTokenHint = "JIRA_TOKEN";
  doctorSlugHint = "JIRA_BASE_URL / JIRA_PROJECT_KEY";
  # The Box has no Jira client and JIRA_TOKEN is host-only, so the research
  # verdict (issue #5245) and a work run's IF BLOCKED comment (issue #5263)
  # relay through the host; its linked-issue reads in prompts key off this too
  # (issue #6344).
  inBoxUnwritableTracker = true;
  # Comments and issue filing (#6352) both post host-side, so the Box needs no
  # Jira client under read-only.
  hostPostingCapable = true;
  # jira reads through its own tracker folder (issue #6857). It declares a write
  # axis only because the registry requires one beside a read axis: GITHUB
  # still drives its write-side gates, and inBoxUnwritableTracker keeps it off
  # the direct write path.
  trackerAxisRead = "JIRA";
  trackerAxisWrite = "GITHUB";
  # Issues carry applied labels though there is no registry (see
  # IssuesWearLabels in cmd/launcher/internal/backend/registry.go).
  issuesWearLabels = true;
  # ADR 0075: the Jira Connector filters by label and by Component, and stamps
  # both when it files.
  routeKinds = [
    "label"
    "component"
  ];
  # Build-time check of the merged defaults, run by lib/mkHarness.nix when this
  # row is the selected tracker (issue #2539): an unknown-key JIRA_STATUS_MAPPING
  # or an unmet JIRA_LIFECYCLE fails the build here, and stays dead config on
  # every other tracker.
  validateKnobs =
    defaults:
    let
      jsm = import ../jira-status-mapping.nix;
      mapping = defaults.jiraStatusMapping or "";
      lifecycleErr = jsm.lifecycleError (defaults.jiraLifecycle or "hybrid") mapping;
    in
    builtins.seq (jsm.parse mapping) (if lifecycleErr != null then throw lifecycleErr else true);
  knobs = {
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
    jiraIssueType = {
      env = "JIRA_ISSUE_TYPE";
      group = "issues";
      default = "Task";
      doc = "Jira issue type host-side issue filing (PostIssue) creates findings as; it must exist on the project";
      flakeOption = true;
      legacySettingsExempt = true;
      nixSubPath = "jira.issueType";
      boxEnv = false;
    };
    jiraLifecycle = {
      env = "JIRA_LIFECYCLE";
      group = "issues";
      default = "hybrid";
      doc = "how the work lifecycle is kept on Jira: hybrid (the default) moves issues through the JIRA_STATUS_MAPPING statuses and falls back to swapping lifecycle labels for a state that is unmapped or whose transition is unavailable, and reads and the claim check also match the labels; status makes the mapped status the whole work state, so every work state (dispatchable, inProgress, complete, failed, ambiguous) must be mapped, no lifecycle label is ever applied (stale ones are still stripped), and an unavailable transition is commented on the issue and settles as transition-refused. Research states stay on labels in both modes";
      flakeOption = true;
      legacySettingsExempt = true;
      nixSubPath = "jira.lifecycle";
      boxEnv = false;
    };
    jiraEmail = {
      env = "JIRA_EMAIL";
      group = "issues";
      doc = "Basic-auth identity the jira Connector's backend row pairs with JIRA_TOKEN: the Jira Cloud account email (Server/Data Center: the username); host-only, never reaches the Box";
      flakeOption = true;
      nixSubPath = "jira.email";
      boxEnv = false;
    };
    jiraStatusMapping = {
      env = "JIRA_STATUS_MAPPING";
      group = "issues";
      default = "";
      doc = "JSON object mapping dispatch states (dispatchable, inProgress, complete, failed, ambiguous) to native Jira status names, e.g. {'inProgress':'In Progress'}, where a value may instead be a {'status':'Done','resolution':'Done'} object whose resolution is sent with the transition; TransitionState performs the matching workflow transition, falling back to swapping the matching lifecycle label when a state is unmapped or its transition is blocked by the project's workflow (JIRA_LIFECYCLE=hybrid only; JIRA_LIFECYCLE=status requires every work state mapped and never falls back); only failed and ambiguous may share a status";
      flakeOption = true;
      nixSubPath = "jira.statusMapping";
      boxEnv = false;
    };
    jiraIncludeComments = {
      env = "JIRA_INCLUDE_COMMENTS";
      group = "issues";
      default = false;
      kind = "bool";
      doc = "DEPRECATED no-op: Jira comments always reach the prompt (the last 10, attributed, through the adapter's comment lister) whether or not this is set. Kept so existing configs still evaluate; will be removed";
      flakeOption = true;
      nixSubPath = "jira.includeComments";
      boxEnv = false;
    };
    jiraToken = {
      env = "JIRA_TOKEN";
      secret = true;
      hostConfig = true;
      doc = "credential the jira Connector's backend row authorizes its calls with, paired with JIRA_EMAIL for Basic auth: a Jira Cloud API token (Server/Data Center: the password or token); needs permission to edit and transition issues in the project, since the Connector writes labels, comments and workflow transitions through it; host-only, never reaches the Box; read on every ISSUE_TRACKER=jira run";
      boxEnv = false;
    };
  };
}
