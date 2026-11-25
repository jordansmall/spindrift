# One entry of the Consumer's accepted baseline (`spindrift-connector-routes.json`,
# keyed by Connector name) as backend row fields. The flake module and the
# dogfood's direct mkHarness rows both read the file through this, so the
# equivalence check holds. A list entry is Routes only; an object entry also
# records the Connector's whole grant, which doctor diffs beside the Routes.
rec {
  grantKeys = [
    "credentialEnv"
    "credentialScheme"
    "baseEnv"
    "gitRemote"
    "gitRemoteEnv"
  ];

  # The values `backendrows.validate` accepts. A row holding another fails
  # every verb, `spindrift connectors accept` included, so it must not load.
  schemes = [
    ""
    "bearer"
    "basic"
  ];

  # Why `grant` is no well-formed grant, or null. `what` names it in the error.
  grantProblem =
    what: grant:
    let
      keys = builtins.attrNames grant;
      unknown = builtins.filter (k: !(builtins.elem k grantKeys)) keys;
      nonString = builtins.filter (k: !(builtins.isString grant.${k})) keys;
    in
    if !(builtins.isAttrs grant) then
      "${what} must be an object"
    else if unknown != [ ] then
      "${what} has unknown key(s) ${builtins.concatStringsSep ", " unknown}; allowed: ${builtins.concatStringsSep ", " grantKeys}"
    else if nonString != [ ] then
      "${what} must hold strings; ${builtins.concatStringsSep ", " nonString} is not"
    else if !(builtins.elem (grant.credentialScheme or "") schemes) then
      "${what} has credentialScheme '${grant.credentialScheme}'; allowed: bearer, basic"
    else if (grant.gitRemote or "") != "" && (grant.gitRemoteEnv or "") != "" then
      "${what} sets both gitRemote and gitRemoteEnv; they are mutually exclusive"
    else
      null;

  # The row fields for `entry` of Connector `name` in `file`.
  rowFields =
    file: name: entry:
    if builtins.isList entry then
      { acceptedRoutes = entry; }
    else if builtins.isAttrs entry then
      let
        grant = removeAttrs entry [ "routes" ];
        problem = grantProblem "${file}: the entry for Connector '${name}'" grant;
      in
      if problem != null then
        throw problem
      else
        {
          acceptedRoutes = entry.routes or [ ];
          acceptedGrant = grant;
        }
    else
      throw "${file}: the entry for Connector '${name}' must be a list of Routes or an object";
}
