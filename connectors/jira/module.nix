# The jira Connector's flake-parts module: the Connector's tracker read path.
# The native jira adapter stays authoritative and every binary still uses it
# for every decision. The backend row below declares the Connector and its
# Route table; nothing in the launcher reads jira through it yet. Leave the
# row's `shadow` option off: the read shadow names a GitHub-shaped target (repo
# slug, no dialect), not jira's project key and API dialect, until the
# cut-over wires them (issue #6753).
#
# The row authorizes with Basic credentials, the Jira Cloud form (account email
# plus API token). Server/Data Center PATs are Bearer, which one row cannot
# express beside Basic; that choice waits for the cut-over too.
{ core }:
{ ... }:
let
  routes = import ./routes.nix;
in
{
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
      doc = "API token the jira Connector's backend row authorizes its reads with, paired with SPINDRIFT_JIRA_CONNECTOR_EMAIL for Basic auth (must be read-only); host-only, never reaches the Box; unused until the jira cut-over (issue #6753)";
      flakeOption = false;
      boxEnv = false;
    };
    jiraConnectorEmail = {
      env = "SPINDRIFT_JIRA_CONNECTOR_EMAIL";
      group = "dispatch";
      doc = "Jira Cloud account email the jira Connector's backend row pairs with SPINDRIFT_JIRA_CONNECTOR_TOKEN for Basic auth; host-only, never reaches the Box; unused until the jira cut-over (issue #6753)";
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
    # The site URL is the native adapter's own JIRA_BASE_URL; the base is not a
    # secret, so a core knob may carry it.
    base = "jiraBaseURL";
  };
}
