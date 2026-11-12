# Route paths are subtree roots (ADR 0047). `{base}` stands for the Jira site's
# base URL, which the backend row names via its `base` knob: core resolves the
# host from it, and prepends any path prefix (a Server install under /jira) to
# each route path, before the table reaches `connectorhost.LoadNetwork`
# (lib/connector-routes.nix, `backendrows.BaseHost`). The table covers every
# read the Connector makes: the JQL search under `/rest/api/2/search`, and the
# issue and comment reads under `/rest/api/2/issue`.
#
# It is the narrowest table the subtree-only Route model can express, not an
# exact one: `/rest/api/2/search` also admits GET `/rest/api/2/search/jql`,
# which Cloud's walk needs, and `/rest/api/2/issue` also admits any other GET
# under an issue (transitions, worklogs, attachments) and `/issue/createmeta`,
# none of which a read uses. Every write stays on the native adapter.
[
  {
    method = "GET";
    host = "{base}";
    path = "/rest/api/2/search";
  }
  {
    method = "GET";
    host = "{base}";
    path = "/rest/api/2/issue";
  }
]
