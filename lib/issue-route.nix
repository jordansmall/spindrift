# ISSUE_ROUTE (ADR 0075): an Issue route is kind=value pairs, one flake
# attrset or the env string's comma-joined form. The one Nix-side parser and
# renderer; cmd/launcher/internal/forge.ParseRoute is the Go mirror, so a
# string that fails here would fail there.
{ lib }:
let
  parsePair =
    route: pair:
    let
      m = builtins.match "([^=]*)=(.*)" pair;
      kind = if m == null then "" else lib.strings.trim (builtins.elemAt m 0);
      value = if m == null then "" else lib.strings.trim (builtins.elemAt m 1);
    in
    if kind == "" || value == "" then
      throw "mkHarness: ISSUE_ROUTE ${builtins.toJSON route}: ${builtins.toJSON (lib.strings.trim pair)} is not kind=value"
    else
      {
        inherit kind value;
      };

  # [ { kind; value; } ] in the order given; a string keeps its own order.
  pairs =
    route:
    if builtins.isAttrs route then
      map (
        kind:
        if lib.hasInfix "," kind || lib.hasInfix "," route.${kind} then
          throw "mkHarness: ISSUE_ROUTE ${builtins.toJSON kind}=${builtins.toJSON route.${kind}}: a comma cannot appear in a kind or value (the env string joins pairs with commas)"
        else
          parsePair route "${kind}=${route.${kind}}"
      ) (builtins.attrNames route)
    else if lib.strings.trim route == "" then
      [ ]
    else
      map (parsePair route) (lib.splitString "," route);
in
{
  # The env string: kind=value pairs, comma-joined. Attrset names sort, so the
  # same route renders the same string whatever order it was written in.
  render = route: lib.concatMapStringsSep "," (p: "${p.kind}=${p.value}") (pairs route);

  # null when every kind in route is declared, else the error message. Pure so
  # a check can pin the message, which tryEval cannot read.
  kindsError =
    {
      tracker,
      declared,
      route,
    }:
    let
      undeclared = lib.subtractLists declared (map (p: p.kind) (pairs route));
    in
    if undeclared == [ ] then
      null
    else
      "mkHarness: ISSUE_ROUTE uses route kind(s) ${lib.concatStringsSep ", " undeclared} that ISSUE_TRACKER=${tracker} does not declare (lib/backends/${tracker}.nix routeKinds: ${
        if declared == [ ] then "none" else lib.concatStringsSep ", " declared
      }) -- declare the kind there with a filter op and a stamp op, or drop it from the route";
}
