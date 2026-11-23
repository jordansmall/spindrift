# The jira Connector's flake-parts module: the Connector's whole tracker path.
# The Connector is jira's only tracker: it answers every read, claim, transition,
# label, comment and filed finding of an ISSUE_TRACKER=jira run, and `shadow` and
# `primary` are refused on the row because no native adapter remains.
#
# The row authorizes with Basic credentials, the Jira Cloud form (account email
# plus API token). Server/Data Center PATs are Bearer, which one row cannot
# express beside Basic, so a Server/Data Center site needs a
# username and password or token in the two knobs below, not a PAT.
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

  # Dedicated knobs, not core's jiraToken/jiraEmail: the Connector is untrusted
  # wasm, so a core secret must never reach it (lib/flakeModule.nix refuses one).
  spindrift.connectors.jira.knobs = {
    jiraConnectorToken = {
      env = "SPINDRIFT_JIRA_CONNECTOR_TOKEN";
      group = "dispatch";
      secret = true;
      doc = "API token the jira Connector's backend row authorizes its calls with, paired with SPINDRIFT_JIRA_CONNECTOR_EMAIL for Basic auth (needs permission to edit and transition issues in the project: the Connector writes labels, comments and workflow transitions through it); host-only, never reaches the Box; read on every ISSUE_TRACKER=jira run";
      flakeOption = false;
      boxEnv = false;
    };
    jiraConnectorEmail = {
      env = "SPINDRIFT_JIRA_CONNECTOR_EMAIL";
      group = "dispatch";
      doc = "Jira Cloud account email (Server/Data Center: username) the jira Connector's backend row pairs with SPINDRIFT_JIRA_CONNECTOR_TOKEN for Basic auth; host-only, never reaches the Box; read on every ISSUE_TRACKER=jira run";
      flakeOption = false;
      boxEnv = false;
    };
  };

  spindrift.connectors.jira.routes = routes;

  spindrift.connectors.jira.fragments = import ./fragments.nix;

  spindrift.connectors.jira.backend = {
    module = pkgs: "${import ./package.nix { inherit pkgs core; }}/jira.wasm";
    inherit routes;
    credential = "jiraConnectorToken";
    scheme = "basic";
    identity = "jiraConnectorEmail";
    # The site URL is core's JIRA_BASE_URL; the base is not a
    # secret, so a core knob may carry it.
    base = "jiraBaseURL";
  };
}
