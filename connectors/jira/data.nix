# The jira Connector as data: the Connector's whole tracker path. The Connector
# is jira's only tracker: it answers every read, claim, transition, label,
# comment and filed finding of an ISSUE_TRACKER=jira run, and `shadow` and
# `primary` are refused on the row because no native adapter remains. Core turns
# this into the `spindrift.connectors.jira` row, confined to the Route table in
# ./routes.nix.
#
# The row authorizes with Basic credentials from core's JIRA_EMAIL and
# JIRA_TOKEN: on Jira Cloud the account email plus an API token, on Server/Data
# Center a username plus password or token. Server/Data Center PATs are Bearer,
# which one row cannot express beside Basic, so a PAT does not work.
{ core }:
{
  name = "jira";
  module = pkgs: "${import ./package.nix { inherit pkgs core; }}/jira.wasm";
  routes = import ./routes.nix;
  # Core's JIRA_TOKEN: the host attaches it, so the untrusted wasm never holds
  # it.
  credential = "jiraToken";
  scheme = "basic";
  identity = "jiraEmail";
  # The site URL is core's JIRA_BASE_URL; the base is not a secret, so a core
  # knob may carry it.
  base = "jiraBaseURL";
  # The tracker-axis prompt fragments (issue #6857).
  fragments = import ./fragments.nix;
}
