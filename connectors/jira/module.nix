# The jira Connector's flake-parts module: the Connector's whole tracker path.
# The Connector is jira's only tracker: it answers every read, claim, transition,
# label, comment and filed finding of an ISSUE_TRACKER=jira run, and `shadow` and
# `primary` are refused on the row because no native adapter remains.
#
# The row authorizes with Basic credentials from core's JIRA_EMAIL and
# JIRA_TOKEN: on Jira Cloud the account email plus an API token, on Server/Data
# Center a username plus password or token. Server/Data Center PATs are Bearer,
# which one row cannot express beside Basic, so a PAT does not work.
{ core }:
{ ... }:
let
  routes = import ./routes.nix;
in
{
  # The batteries root does not carry jira; the key lets a Consumer that imports
  # it twice, or through two paths, dedup instead of failing "registered twice".
  key = "spindrift-connector-jira";

  # Depends on the core it was handed; the module system dedups by path, so a
  # Consumer that imports the same core module is unaffected.
  imports = [ core.flakeModules.default ];

  spindrift.connectors.jira.routes = routes;

  spindrift.connectors.jira.fragments = import ./fragments.nix;

  spindrift.connectors.jira.backend = {
    module = pkgs: "${import ./package.nix { inherit pkgs core; }}/jira.wasm";
    inherit routes;
    credential = "jiraToken";
    scheme = "basic";
    identity = "jiraEmail";
    # The site URL is core's JIRA_BASE_URL; the base is not a
    # secret, so a core knob may carry it.
    base = "jiraBaseURL";
  };
}
