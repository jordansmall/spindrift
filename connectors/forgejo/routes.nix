# Route paths are subtree roots (ADR 0047). `{base}` stands for the Forgejo
# instance's base URL, which the backend row names via its `base` knob: core
# resolves the host from it, and prepends any path prefix (a sub-path install)
# to each route path, before the table reaches `connectorhost.LoadNetwork`
# (lib/connector-routes.nix, `backendrows.BaseHost`). `{repo}` is the
# repository's owner/name slug, which core substitutes. The table covers every
# request the Connector makes: the PR list and one PR under `/pulls`, the
# head commit's combined status and statuses under `/commits`, and a branch's
# lookup and delete under `/branches`.
#
# It is the narrowest table the subtree-only Route model can express, not an
# exact one: `/commits` also admits any other GET under a commit, and
# `/branches` also admits GET of the branch list.
[
  {
    method = "GET";
    host = "{base}";
    path = "/api/v1/repos/{repo}/pulls";
  }
  {
    method = "GET";
    host = "{base}";
    path = "/api/v1/repos/{repo}/commits";
  }
  {
    method = "GET";
    host = "{base}";
    path = "/api/v1/repos/{repo}/branches";
  }
  {
    method = "DELETE";
    host = "{base}";
    path = "/api/v1/repos/{repo}/branches";
  }
]
