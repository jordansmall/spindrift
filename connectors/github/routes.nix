# Route paths are subtree roots (ADR 0047). `{repo}` stands for the repository's
# owner/name slug, which core's `connectortracker.Routes` substitutes before
# the table reaches `connectorhost.LoadNetwork`. The table covers every read
# the Connector makes: the tracker reads (list, issue, blocked_by) under
# `/issues`, the forge PR reads (branch lookup, one PR) under `/pulls`, the
# check rollup over `POST /graphql`, and a failing job's log under
# `/actions/jobs`. The forge write half adds the PR and branch primitives: open
# and merge a PR, compare, the comment on a PR, branch lookup and delete, and
# branch protection. The tracker write half adds the repository's label list and
# create (`GET` and `POST /labels`); label add, comment and issue filing ride
# the `POST /issues` root, and label removal rides `POST /graphql`. An App
# installation token's grant is read from `GET /installation/repositories`.
#
# It is the narrowest table the subtree-only Route model can express, not an
# exact one: `/issues` also admits GET `/issues/comments/*` and `/issues/events`,
# which no read uses; the prior-claim read walks `/issues/{n}/timeline`.
# `POST /graphql` is wider still: a Route restricts neither body nor query, so
# it admits any GraphQL document, mutations included, and no Route can pin it
# to the repository. The Connector sends the rollup query, repository-identity
# queries, classic token-grant queries, the draft-flip and auto-merge
# mutations, and the label-id query and removal mutation there (issues #6808,
# #6881, #6882). Label removal cannot be a REST `DELETE`: the host refuses the
# escaped `/` in a name such as `agent/ready`. Every REST write is pinned to
# `{repo}`. The write roots over-admit too: `POST /pulls` also admits review
# and review-comment creation, and `PUT /pulls` update-branch.
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
  {
    method = "POST";
    host = "api.github.com";
    path = "/repos/{repo}/pulls";
  }
  {
    method = "PUT";
    host = "api.github.com";
    path = "/repos/{repo}/pulls";
  }
  {
    method = "POST";
    host = "api.github.com";
    path = "/repos/{repo}/issues";
  }
  {
    method = "GET";
    host = "api.github.com";
    path = "/repos/{repo}/compare";
  }
  {
    method = "GET";
    host = "api.github.com";
    path = "/repos/{repo}/git/matching-refs/heads";
  }
  {
    method = "DELETE";
    host = "api.github.com";
    path = "/repos/{repo}/git/refs/heads";
  }
  {
    method = "GET";
    host = "api.github.com";
    path = "/repos/{repo}/branches";
  }
  {
    method = "GET";
    host = "api.github.com";
    path = "/repos/{repo}/rules/branches";
  }
  {
    method = "GET";
    host = "api.github.com";
    path = "/repos/{repo}/labels";
  }
  {
    method = "POST";
    host = "api.github.com";
    path = "/repos/{repo}/labels";
  }
  {
    method = "GET";
    host = "api.github.com";
    path = "/installation/repositories";
  }
]
