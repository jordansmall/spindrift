# Route paths are subtree roots (ADR 0047). `{base}` stands for the Jira site's
# base URL, which the backend row names via its `base` knob: core resolves the
# host from it, and prepends any path prefix (a Server install under /jira) to
# each route path, before the table reaches `connectorhost.LoadNetwork`
# (lib/connector-routes.nix, `backendrows.BaseHost`). The table covers every
# request the Connector makes: the JQL search under `/rest/api/2/search`, the
# issue, comment and createmeta reads under `/rest/api/2/issue`, the doctor's
# credential probe (`/myself`) and the project's statuses (`/project/{repo}/statuses`,
# `{repo}` being the Jira project key, substituted by `connectortracker.Routes`)
# and the site-wide label list (`/label`); Cloud's approximate count, a POST that
# only counts; and the lifecycle writes core asks for: PUT `/rest/api/2/issue`
# (the label edit) and POST `/rest/api/2/issue` (a workflow transition, under
# `/issue/{key}/transitions`).
#
# It is the narrowest table the subtree-only Route model can express, not an
# exact one: `/rest/api/2/search` also admits GET `/rest/api/2/search/jql`,
# which Cloud's walk needs, and `/rest/api/2/issue` also admits any other GET
# under an issue (worklogs, attachments), none of which the Connector uses.
# The POST subtree also admits issue create and comments, which the Connector
# does not use either.
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
  {
    method = "PUT";
    host = "{base}";
    path = "/rest/api/2/issue";
  }
  {
    method = "POST";
    host = "{base}";
    path = "/rest/api/2/issue";
  }
  {
    method = "GET";
    host = "{base}";
    path = "/rest/api/2/label";
  }
  {
    method = "GET";
    host = "{base}";
    path = "/rest/api/2/myself";
  }
  {
    method = "GET";
    host = "{base}";
    path = "/rest/api/2/project/{repo}/statuses";
  }
  {
    method = "POST";
    host = "{base}";
    path = "/rest/api/3/search/approximate-count";
  }
]
