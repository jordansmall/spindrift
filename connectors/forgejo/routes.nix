# Route paths are subtree roots (ADR 0047). `{base}` stands for the Forgejo
# instance's base URL, which the backend row names via its `base` knob: core
# resolves the host from it, and prepends any path prefix (a sub-path install)
# to each route path, before the table reaches `connectorhost.LoadNetwork`
# (lib/connector-routes.nix, `backendrows.BaseHost`). `{repo}` is the
# repository's owner/name slug, which core substitutes. The table covers every
# request the Connector makes. The reads are the repository itself (the probe
# and the allowed merge styles), the PR list, one PR and its files, the compare
# for how far a PR is behind, the head commit's statuses, a failing job's log
# (its run and jobs lists, then the log), a branch's lookup and protection
# flag, the issue list (also read for the open-issue count, the labeled scan
# and the text search), one issue, its comments and native dependencies, the
# repository's labels, and the owner's org labels under `/orgs`. The writes
# are open and merge a PR and the draft flip under `/pulls`, a PR's and an
# issue's comment and the filing of an issue under `/issues`, an issue's label
# replace and close under `/issues`, a label's create under `/labels`, and a
# branch's delete under `/branches`.
#
# It is the narrowest table the subtree-only Route model can express, not an
# exact one. The repository read cannot be pinned to the repository document
# itself: its root also admits every GET under the repository, which is why
# the other reads but the log need no root of their own, and the Connector
# sends only the ones above. The log read rides its own `/actions/jobs` root,
# listed first because the first match wins, so a log over the response cap
# arrives as its tail, not as a failed read. Forgejo serves the log directly,
# with no redirect; `followRedirect` is what selects tail mode. The write roots
# over-admit too: `POST /pulls` also admits review creation, `PATCH /pulls` any
# PR edit, `POST /issues` any issue's comment or label add, `PUT /issues` any
# issue's label replace, `PATCH /issues` any issue edit, and `POST /labels` any
# label create. The org-label read cannot be pinned to the owner, since core
# substitutes only `{repo}`: `GET /orgs` admits every org read, as the GitHub
# table's unpinned `GET /search/issues` does; the route admits no write.
#
# The issue count reads `X-Total-Count` off a `GET /issues` answer, which the
# repository root admits, so that Route declares `responseHeaders`; the host hands
# the guest no response header outside its fixed set unless the Route names it.
[
  {
    method = "GET";
    host = "{base}";
    path = "/api/v1/repos/{repo}/actions/jobs";
    followRedirect = true;
  }
  {
    method = "GET";
    host = "{base}";
    path = "/api/v1/repos/{repo}";
    responseHeaders = [ "X-Total-Count" ];
  }
  {
    method = "POST";
    host = "{base}";
    path = "/api/v1/repos/{repo}/pulls";
  }
  {
    method = "PATCH";
    host = "{base}";
    path = "/api/v1/repos/{repo}/pulls";
  }
  {
    method = "POST";
    host = "{base}";
    path = "/api/v1/repos/{repo}/issues";
  }
  {
    method = "DELETE";
    host = "{base}";
    path = "/api/v1/repos/{repo}/branches";
  }
  {
    method = "PUT";
    host = "{base}";
    path = "/api/v1/repos/{repo}/issues";
  }
  {
    method = "PATCH";
    host = "{base}";
    path = "/api/v1/repos/{repo}/issues";
  }
  {
    method = "POST";
    host = "{base}";
    path = "/api/v1/repos/{repo}/labels";
  }
  {
    method = "GET";
    host = "{base}";
    path = "/api/v1/orgs";
  }
]
