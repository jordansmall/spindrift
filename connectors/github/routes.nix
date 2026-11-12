# Route paths are subtree roots (ADR 0047). `{repo}` stands for the repository's
# owner/name slug, which core's `connectortracker.Routes` substitutes before
# the table reaches `connectorhost.LoadNetwork`. The table covers every read
# the Connector makes: the tracker reads (list, issue, blocked_by) under
# `/issues`, the forge PR reads (branch lookup, one PR) under `/pulls`, and the
# check rollup over `POST /graphql`.
#
# It is the narrowest table the subtree-only Route model can express, not an
# exact one: `/issues` also admits GET `/issues/comments/*`, `/issues/events`
# and `/issues/{n}/timeline`, which no read uses. `POST /graphql` is wider
# still: a Route restricts neither body nor query, so it admits any GraphQL
# document, mutations included. Issue #6808 accepts that: the Connector sends
# only the rollup query, and every write stays on the native adapter.
[
  {
    method = "GET";
    host = "api.github.com";
    path = "/repos/{repo}/issues";
  }
  {
    method = "GET";
    host = "api.github.com";
    path = "/repos/{repo}/pulls";
  }
  {
    method = "POST";
    host = "api.github.com";
    path = "/graphql";
  }
]
