# The jira backend row: data plus the env knobs it owns (issues #5633,
# #5635). The registry (./default.nix) documents the fields and fixes this
# row's order; lib/row-knobs.nix folds `knobs` into lib/env-schema.nix.
{
  name = "jira";
  goVar = "Jira";
  validAsTracker = true;
  tokenEnvVar = "JIRA_TOKEN";
  doctorTokenHint = "JIRA_TOKEN";
  doctorSlugHint = "JIRA_BASE_URL / JIRA_PROJECT_KEY";
  # The Box has no Jira client and JIRA_TOKEN is host-only, so the research
  # verdict (issue #5245) and a work run's IF BLOCKED comment (issue #5263)
  # relay through the host.
  inBoxUnwritableTracker = true;
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
    jiraEmail = {
      env = "JIRA_EMAIL";
      group = "issues";
      doc = "Jira Cloud account email, paired with JIRA_TOKEN for Basic auth; setting it also selects Jira Cloud's search APIs; leave empty for Bearer-token auth (Jira Server/Data Center PATs, which keep the v2 /search)";
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
      doc = "DEPRECATED no-op: Jira comments always reach the prompt (the last 10, attributed, through the adapter's comment lister) whether or not this is set. Kept so existing configs still evaluate; will be removed";
      flakeOption = true;
      nixSubPath = "jira.includeComments";
      boxEnv = false;
    };
    jiraToken = {
      env = "JIRA_TOKEN";
      secret = true;
      hostConfig = true;
      doc = "Jira API token (Cloud: paired with JIRA_EMAIL for Basic auth; Server/Data Center: used alone as a Bearer PAT); required when ISSUE_TRACKER=jira";
      boxEnv = false;
    };
  };
}
