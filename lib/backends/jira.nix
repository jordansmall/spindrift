# The jira backend row: pure data only (issue #5633). The registry
# (./default.nix) documents the fields and fixes this row's order.
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
}
