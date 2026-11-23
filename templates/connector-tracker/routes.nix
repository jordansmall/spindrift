# Route paths are subtree roots (ADR 0047). `{base}` stands for the tracker's
# base URL, which the backend row names via its `base` knob: core resolves the
# host from it, and prepends any path prefix to each route path, before the
# table reaches `connectorhost.LoadNetwork` (lib/connector-routes.nix,
# `backendrows.BaseHost`). `{repo}` is the tracker's project, substituted by
# `connectortracker.Routes`. List every request the Connector makes and no
# more: this table is the whole of its network authority. The stub declares the
# one subtree main.go's reads use.
[
  {
    method = "GET";
    host = "{base}";
    path = "/api/v1/repos/{repo}";
  }
]
