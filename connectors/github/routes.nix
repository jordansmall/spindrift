# Route paths are subtree roots (ADR 0047). `{repo}` stands for the repository's
# owner/name slug, which core's `connectortracker.Routes` substitutes before
# the table reaches `connectorhost.LoadNetwork`. Every REST write is pinned to
# `{repo}`.
#
# `GET /search/issues` is not pinned to the repository: a Route restricts no
# query string, so the path cannot name `{repo}`. It is read-only, and the
# Connector always sends a `repo:` qualifier, quoting the caller's text so it
# cannot add one. The App token-grant read, `GET /installation/repositories`,
# is unpinned too (see tokengrant.go).
#
# This is the narrowest table the subtree-only Route model can express, not an
# exact one: `GET /issues` also admits `/issues/comments/*` and
# `/issues/events`, `POST /pulls` admits review creation, `PUT /pulls` admits
# update-branch, and `/actions/jobs` admits every GET beside `/{id}/logs`.
# `POST /graphql` admits any document, mutations included, and cannot be
# pinned to the repository; the Connector sends its rollup, comment, identity,
# token-grant, draft-flip, auto-merge, label-removal and close documents there
# (issues #6808, #6881, #6882, #6887). Label removal cannot be a REST
# `DELETE`: the host refuses the escaped `/` in a name such as `agent/ready`.
#
# The job-log Route follows one redirect hop (issue #6893): GitHub answers
# with a 302 to a pre-signed blob URL on a varying host, so no host can be
# pinned. The second request carries no credential and none of the guest's
# headers. A log over the response cap arrives as its tail.
#
# Every Route declares `requestHeaders = apiVersion` (`[ "X-GitHub-Api-Version" ]`): the request
# helper pins the API version on each request, and the host refuses a header
# outside its fixed set unless the Route names it. `POST /graphql` also declares
# `responseHeaders = [ "X-OAuth-Scopes" ]`, which the token-grant read takes from
# its answer. routes_test.go pins both lists to what the Connector sends and reads.
let
  apiVersion = [ "X-GitHub-Api-Version" ];
in
[
  {
    method = "GET";
    host = "api.github.com";
    path = "/repos/{repo}/issues";
    requestHeaders = apiVersion;
  }
  {
    method = "GET";
    host = "api.github.com";
    path = "/repos/{repo}/pulls";
    requestHeaders = apiVersion;
  }
  {
    method = "POST";
    host = "api.github.com";
    path = "/graphql";
    requestHeaders = apiVersion;
    responseHeaders = [ "X-OAuth-Scopes" ];
  }
  {
    method = "GET";
    host = "api.github.com";
    path = "/repos/{repo}/actions/jobs";
    followRedirect = true;
    requestHeaders = apiVersion;
  }
  {
    method = "POST";
    host = "api.github.com";
    path = "/repos/{repo}/pulls";
    requestHeaders = apiVersion;
  }
  {
    method = "PUT";
    host = "api.github.com";
    path = "/repos/{repo}/pulls";
    requestHeaders = apiVersion;
  }
  {
    method = "POST";
    host = "api.github.com";
    path = "/repos/{repo}/issues";
    requestHeaders = apiVersion;
  }
  {
    method = "GET";
    host = "api.github.com";
    path = "/repos/{repo}/compare";
    requestHeaders = apiVersion;
  }
  {
    method = "GET";
    host = "api.github.com";
    path = "/repos/{repo}/git/matching-refs/heads";
    requestHeaders = apiVersion;
  }
  {
    method = "DELETE";
    host = "api.github.com";
    path = "/repos/{repo}/git/refs/heads";
    requestHeaders = apiVersion;
  }
  {
    method = "GET";
    host = "api.github.com";
    path = "/repos/{repo}/branches";
    requestHeaders = apiVersion;
  }
  {
    method = "GET";
    host = "api.github.com";
    path = "/repos/{repo}/rules/branches";
    requestHeaders = apiVersion;
  }
  {
    method = "GET";
    host = "api.github.com";
    path = "/repos/{repo}/labels";
    requestHeaders = apiVersion;
  }
  {
    method = "POST";
    host = "api.github.com";
    path = "/repos/{repo}/labels";
    requestHeaders = apiVersion;
  }
  {
    method = "GET";
    host = "api.github.com";
    path = "/installation/repositories";
    requestHeaders = apiVersion;
  }
  {
    method = "GET";
    host = "api.github.com";
    path = "/search/issues";
    requestHeaders = apiVersion;
  }
]
