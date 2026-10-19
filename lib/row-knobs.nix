# Folds the knobs that registry rows own into the env schema (ADR 0068
# decision 2, issue #5634). Builtins only: lib/env-schema.nix has no `lib` in
# scope. Generic over registries -- a registry is `{ source; rows; }` where
# `source` is the repo path of the directory holding one `<row.name>.nix` per
# row. A row's optional `knobs` attrset is keyed by env-schema key (the key is
# the identity, and doubles as the generated Go field name) and each value is
# an env-schema entry.
#
# Two declarations of one env name or one key are a collision, reported with
# both sources. `collisions` is a pure list of messages because
# builtins.tryEval cannot see thrown text; `fold` throws them.
let
  claimsOf =
    {
      shared,
      registries,
      sharedSource,
    }:
    let
      sharedClaims = map (key: {
        source = sharedSource;
        kind = "entry";
        inherit key;
        env = shared.${key}.env or null;
      }) (builtins.attrNames shared);
      rowClaims = builtins.concatMap (
        registry:
        builtins.concatMap (
          row:
          map (key: {
            source = "${registry.source}/${row.name}.nix";
            kind = "knob";
            inherit key;
            env = row.knobs.${key}.env or null;
          }) (builtins.attrNames (row.knobs or { }))
        ) registry.rows
      ) registries;
    in
    sharedClaims ++ rowClaims;

  describe = claim: "${claim.source} ${claim.kind} ${claim.key}";

  collisionsOf =
    {
      shared,
      registries,
      sharedSource,
    }:
    let
      claims = claimsOf { inherit shared registries sharedSource; };
      # groupBy keeps list order, so the first claim in a group is the
      # earliest declaration and every later one is reported against it.
      repeats =
        what: group:
        builtins.concatMap (
          name:
          let
            first = builtins.head group.${name};
          in
          map (claim: "${what} ${name} is declared twice: by ${describe first} and by ${describe claim}") (
            builtins.tail group.${name}
          )
        ) (builtins.attrNames group);
    in
    repeats "env" (
      builtins.groupBy (claim: claim.env) (builtins.filter (claim: claim.env != null) claims)
    )
    ++ repeats "key" (builtins.groupBy (claim: claim.key) claims);
in
{
  collisions = collisionsOf;

  fold =
    args@{ shared, registries, ... }:
    let
      problems = collisionsOf args;
      folded = builtins.foldl' (
        acc: registry: builtins.foldl' (acc: row: acc // (row.knobs or { })) acc registry.rows
      ) { } registries;
    in
    if problems != [ ] then throw (builtins.concatStringsSep "\n" problems) else shared // folded;
}
