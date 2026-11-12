# A network Connector's Route table, validated at evaluation with the rules the
# Go host applies at load (connectorhost.Route.validate and
# registryvocab.CheckPathPattern; ADR 0045/0047, issue #6805), so a bad table
# fails the build instead of a live run. Builtins only, so anything can import
# it.
#
# `problems` is a pure list of messages because builtins.tryEval cannot see
# thrown text; `check` throws them.
let
  # RFC 7230 token, uppercase only: the Go side requires method == ToUpper.
  methodOk = m: builtins.match "[!#$%&'*+.^_`|~0-9A-Z-]+" m != null;

  # A bare host[:port]: no userinfo, path, query, fragment or trailing ":".
  # The whole-host literal `{base}` stands for the host of the row's base URL;
  # the Go host resolves it at load and validates the result.
  hostOk =
    h:
    h == "{base}"
    || builtins.match "([A-Za-z0-9._~!$&'()*+,;=-]+|[[][0-9A-Fa-f:.]+[]])(:[0-9]+)?" h != null;

  # path.Clean(p) == p, for a path with a leading "/".
  canonical =
    p:
    builtins.substring 0 1 p == "/"
    && (
      p == "/"
      || (
        builtins.substring (builtins.stringLength p - 1) 1 p != "/"
        && builtins.all (s: s != "" && s != "." && s != "..") (
          builtins.filter builtins.isString (builtins.split "/" (builtins.substring 1 (-1) p))
        )
      )
    );

  pathProblem =
    p:
    if builtins.match ".*[?#].*" p != null then
      ''contains "?" or "#"''
    else if p == "" || !(canonical p) then
      ''not an absolute path already in canonical form (leading "/", no trailing "/", no "." or ".." segment)''
    else if p == "/" then
      ''"/" would blanket-authorize the whole host''
    else
      null;

  # `followRedirect` is optional; the Go host allows it on GET only.
  knownKeys = [
    "followRedirect"
    "host"
    "method"
    "path"
  ];
  required = [
    "host"
    "method"
    "path"
  ];

  routeProblem =
    r:
    if
      !(
        builtins.isAttrs r
        && builtins.all (k: r ? ${k} && builtins.isString r.${k}) required
        && builtins.all (k: builtins.elem k knownKeys) (builtins.attrNames r)
        && (r ? followRedirect -> builtins.isBool r.followRedirect)
      )
    then
      "is not an attrset of exactly the strings `method`, `host` and `path` and an optional boolean `followRedirect`"
    else if !(methodOk r.method) then
      "method is not an uppercase HTTP token"
    else if !(hostOk r.host) then
      "host is not a bare host[:port]"
    else if (r.followRedirect or false) && r.method != "GET" then
      "followRedirect is allowed on GET only"
    else
      pathProblem r.path;

  describe =
    r:
    if
      builtins.isAttrs r
      && builtins.all (k: builtins.isString (r.${k} or null)) [
        "method"
        "host"
        "path"
      ]
    then
      "${r.method} ${r.host} ${r.path}"
    else
      builtins.toJSON r;

  problems =
    name: routes:
    if !(builtins.isList routes) || routes == [ ] then
      [
        "Connector '${name}': the route table is empty or not a list; a network Connector needs at least one route"
      ]
    else
      builtins.concatMap (
        r:
        let
          p = routeProblem r;
        in
        if p == null then [ ] else [ "Connector '${name}': route '${describe r}': ${p}" ]
      ) routes;
in
{
  inherit problems;
  check =
    name: routes:
    let
      ps = problems name routes;
    in
    if ps == [ ] then routes else throw (builtins.concatStringsSep "\n" ps);
}
