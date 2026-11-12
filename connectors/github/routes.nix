# Route paths are subtree roots (ADR 0047). `{repo}` stands for the tracker's
# owner/name slug, which core's `connectortracker.Routes` substitutes before
# the table reaches `connectorhost.LoadNetwork`. This one GET subtree covers
# every read the Connector makes (list, issue, blocked_by) and
# nothing that writes. It is the narrowest table the subtree-only Route model
# can express, not an exact one: it also admits GET `/issues/comments/*`,
# `/issues/events` and `/issues/{n}/timeline`, which no read uses.
[
  {
    method = "GET";
    host = "api.github.com";
    path = "/repos/{repo}/issues";
  }
]
