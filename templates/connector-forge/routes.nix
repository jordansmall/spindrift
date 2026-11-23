# Route paths are subtree roots (ADR 0047). `{base}` stands for the forge's
# base URL, which the backend row names via its `base` knob: core resolves the
# host from it, and prepends any path prefix to each route path, before the
# table reaches `connectorhost.LoadNetwork` (lib/connector-routes.nix,
# `backendrows.BaseHost`). `{repo}` is the repository's owner/name slug,
# substituted by `connectortracker.Routes`. List every request the Connector
# makes and no more: this table is the whole of its network authority. The Connector
# declares the narrowest subtree per write main.go makes: the merge and its
# auto-merge enqueue under `/pulls`, the comment under `/issues`, the draft
# flip under `/pulls`, and a branch's delete under `/branches`. The reads share
# the repository root. A route admits its whole subtree, so each is wider than
# the one request: `POST /pulls` also admits review creation, `PATCH /pulls`
# any PR edit, and `POST /issues` any issue's comment or label add.
[
  {
    method = "GET";
    host = "{base}";
    path = "/api/v1/repos/{repo}";
  }
  {
    method = "POST";
    host = "{base}";
    path = "/api/v1/repos/{repo}/pulls";
  }
  {
    method = "POST";
    host = "{base}";
    path = "/api/v1/repos/{repo}/issues";
  }
  {
    method = "PATCH";
    host = "{base}";
    path = "/api/v1/repos/{repo}/pulls";
  }
  {
    method = "DELETE";
    host = "{base}";
    path = "/api/v1/repos/{repo}/branches";
  }
]
