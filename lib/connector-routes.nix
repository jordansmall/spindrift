# A network Connector's Route table, validated at evaluation with the rules the
# Go host applies at load (connectorhost.Route.validate and
# registryvocab.CheckPathPattern; ADR 0045/0047, issue #6805), so a bad table
# fails the build instead of a live run. Builtins only, so anything can import
# it.
#
# `problems` is a pure list of messages because builtins.tryEval cannot see
# thrown text; `check` throws them.
let
  # The fixed set the Go host admits (connectorhost.httpMethods).
  httpMethods = [
    "GET"
    "HEAD"
    "POST"
    "PUT"
    "PATCH"
    "DELETE"
  ];
  methodOk = m: builtins.elem m httpMethods;

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

  # `followRedirect`, `documents`, `requestHeaders` and `responseHeaders` are
  # optional; the Go host allows `followRedirect` on GET only and `documents`
  # on POST only.
  knownKeys = [
    "documents"
    "followRedirect"
    "host"
    "method"
    "path"
    "requestHeaders"
    "responseHeaders"
  ];
  required = [
    "host"
    "method"
    "path"
  ];

  # Header names compare case-insensitively (Go compares the canonical form).
  letters = s: builtins.genList (i: builtins.substring i 1 s) 26;
  lower = builtins.replaceStrings (letters "ABCDEFGHIJKLMNOPQRSTUVWXYZ") (
    letters "abcdefghijklmnopqrstuvwxyz"
  );

  hasPrefix = pre: s: builtins.substring 0 (builtins.stringLength pre) s == pre;

  # connectorhost.validHeaderName: an RFC 9110 token.
  headerNameOk = n: builtins.match "[A-Za-z0-9!#$%&'*+.^_`|~-]+" n != null;

  # connectorhost.neverRequestHeader / neverResponseHeader, on a lowercased
  # name: the host owns the credential and the connection's identity, and
  # keeps cookies.
  neverRequest =
    n:
    builtins.elem n [
      "authorization"
      "cookie"
      "host"
      "forwarded"
      "via"
      "x-real-ip"
    ]
    || hasPrefix "x-forwarded-" n
    || hasPrefix "proxy-" n;
  neverResponse =
    n:
    builtins.elem n [
      "set-cookie"
      "set-cookie2"
    ];

  # The first problem in one route's declared header names, or null
  # (connectorhost.checkDeclaredHeaders).
  headerProblem =
    field: never: names:
    let
      go =
        seen: ns:
        if ns == [ ] then
          null
        else
          let
            n = builtins.head ns;
            l = lower n;
          in
          if !(headerNameOk n) then
            ''${field}: "${n}" is not a valid header name''
          else if never l then
            ''${field}: "${n}" can never be declared''
          else if builtins.elem l seen then
            ''${field}: "${n}" is declared twice''
          else
            go (seen ++ [ l ]) (builtins.tail ns);
    in
    go [ ] names;

  headersProblem =
    r:
    let
      req = headerProblem "requestHeaders" neverRequest (r.requestHeaders or [ ]);
    in
    if req != null then
      req
    else
      headerProblem "responseHeaders" neverResponse (r.responseHeaders or [ ]);

  headerListOk = l: builtins.isList l && builtins.all builtins.isString l;
  documentsShapeOk = d: builtins.isAttrs d && builtins.all builtins.isString (builtins.attrValues d);

  # connectorhost.checkDeclaredDocuments: the first problem in a route's
  # `documents`, in name order, or null. Texts compare byte for byte.
  documentsProblem =
    docs:
    let
      names = builtins.attrNames docs;
      go =
        seen: ns:
        if ns == [ ] then
          null
        else
          let
            n = builtins.head ns;
            t = docs.${n};
            twin = builtins.filter (m: seen.${m} == t) (builtins.attrNames seen);
          in
          if builtins.match "[A-Za-z][A-Za-z0-9_]*" n == null then
            ''documents: "${n}" is not a valid name''
          else if t == "" then
            ''documents: "${n}" has no text''
          else if twin != [ ] then
            ''documents: "${builtins.head twin}" and "${n}" declare the same text''
          else
            go (seen // { ${n} = t; }) (builtins.tail ns);
    in
    if names == [ ] then "documents: must declare at least one" else go { } names;

  routeProblem =
    r:
    if
      !(
        builtins.isAttrs r
        && builtins.all (k: r ? ${k} && builtins.isString r.${k}) required
        && builtins.all (k: builtins.elem k knownKeys) (builtins.attrNames r)
        && (r ? followRedirect -> builtins.isBool r.followRedirect)
        && (r ? documents -> documentsShapeOk r.documents)
        && (r ? requestHeaders -> headerListOk r.requestHeaders)
        && (r ? responseHeaders -> headerListOk r.responseHeaders)
      )
    then
      "is not an attrset of exactly the strings `method`, `host` and `path`, an optional boolean `followRedirect`, an optional attrset of strings `documents` and optional string lists `requestHeaders` and `responseHeaders`"
    else if !(methodOk r.method) then
      "method is not one of ${builtins.concatStringsSep ", " httpMethods}"
    else if !(hostOk r.host) then
      "host is not a bare host[:port]"
    else if (r.followRedirect or false) && r.method != "GET" then
      "followRedirect is allowed on GET only"
    else
      (
        let
          h = headersProblem r;
          d =
            if !(r ? documents) then
              null
            else if r.method != "POST" then
              "documents are allowed on POST only"
            else
              documentsProblem r.documents;
        in
        if h != null then
          h
        else if d != null then
          d
        else
          pathProblem r.path
      );

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
