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

  # Why a third-party backend `row` (as mkHarness's checkBackendRow returns it)
  # may not run, or null. Mirrors doctor's `Widening` and `GrantChanges`
  # (backendrows.go), but stricter on the grant: a Routes-only entry has none,
  # so every part the row declares counts as added. A part the row declares
  # absent is a narrowing and stays silent.
  refusal =
    { name, row }:
    let
      acceptCommand = "Run `nix run .#connectors-accept` (the `spindrift connectors accept` verb) in the Consumer flake's root, then review and commit the diff.";
      file = "spindrift-connector-routes.json";
      accepted = row.acceptedRoutes;
      grant = row.acceptedGrant or { };
      covers =
        l: a:
        a.method == l.method
        && a.host == l.host
        && ((a.followRedirect or false) || !(l.followRedirect or false))
        && (
          a.path == l.path || (builtins.substring 0 (builtins.stringLength a.path + 1) l.path == "${a.path}/")
        );
      uncovered = builtins.filter (l: !(builtins.any (a: covers l a) accepted)) row.routes;
      routeLine =
        r:
        "\n  added Route ${r.method} ${r.host} ${r.path}"
        + (if r.followRedirect or false then " (followRedirect)" else "");
      scheme = s: if s == "" then "bearer" else s;
      remote = g: if (g.gitRemoteEnv or "") != "" then "env ${g.gitRemoteEnv}" else g.gitRemote or "";
      # A Routes-only entry accepts no scheme; in a grant, an absent one is bearer.
      acceptedScheme = if row ? acceptedGrant then scheme (grant.credentialScheme or "") else "";
      part =
        what: declared: was:
        if declared == "" || declared == was then
          [ ]
        else if was == "" then
          [ "\n  added ${what} ${declared}" ]
        else
          [ "\n  changed ${what} ${declared} (accepted: ${was})" ];
      changes =
        part "credential variable" (row.credentialEnv or "") (grant.credentialEnv or "")
        ++ part "authorization scheme" (scheme (row.credentialScheme or "")) acceptedScheme
        ++ part "base" (row.baseEnv or "") (grant.baseEnv or "")
        ++ part "Git remote" (remote row) (remote grant);
      added = map routeLine uncovered ++ changes;
    in
    if !(row ? acceptedRoutes) then
      "mkHarness: third-party Connector '${name}' has no entry in the accepted baseline (${file}), and nothing from it runs before it is accepted. ${acceptCommand}"
    else if added == [ ] then
      null
    else
      "mkHarness: third-party Connector '${name}' declares more than its entry in the accepted baseline (${file}):${builtins.concatStringsSep "" added}\n${acceptCommand}";

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
