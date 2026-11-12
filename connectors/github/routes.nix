# Route paths are subtree roots (ADR 0047). `{repo}` stands for the repository's
# owner/name slug, which core's `connectortracker.Routes` substitutes before
# the table reaches `connectorhost.LoadNetwork`. The table covers every read
# the Connector makes: the tracker reads (list, issue, blocked_by) under
# `/issues`, the forge PR reads (branch lookup, one PR) under `/pulls`, the
# check rollup over `POST /graphql`, and a failing job's log under
# `/actions/jobs`.
#
# It is the narrowest table the subtree-only Route model can express, not an
# exact one: `/issues` also admits GET `/issues/comments/*`, `/issues/events`
# and `/issues/{n}/timeline`, which no read uses. `POST /graphql` is wider
# still: a Route restricts neither body nor query, so it admits any GraphQL
# document, mutations included. Issue #6808 accepts that: the Connector sends
# only the rollup query, and every write stays on the native adapter.
# `/actions/jobs` is a root too: the log read is `GET /actions/jobs/{id}/logs`,
# but the root also admits every other GET under `/actions/jobs`, which
# no read uses. Its Route follows one redirect hop (issue #6893): GitHub
# answers the log read with a 302 to a pre-signed blob URL on a host that
# varies, so no host can be pinned. The host sends the second request with no
# credential and none of the guest's headers. A log over the response cap
# arrives as its tail, not as a failed read.
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
  {
    method = "GET";
    host = "api.github.com";
    path = "/repos/{repo}/actions/jobs";
    followRedirect = true;
  }
]
