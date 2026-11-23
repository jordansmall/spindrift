# Each backend row's Consumer stanza (issue #7415): what a Consumer of that
# backend configures, composed from the row (lib/backends/<name>.nix) and its
# Connector's knobs (nix/connector-knobs.nix).
#
# Returns `{ stanzas; errors; }`. `stanzas` is one entry per backend row, in
# registry order: `{ name; displayName; settings; secrets; flakeInput;
# moduleImport; }`, where `settings` is the row's own `flakeOption` knobs and
# `secrets` is the row's `tokenEnvVar` schema entry (when it has one) followed
# by the Connector knobs its `consumerStanza.connectorKnobs` lists. Forcing
# `stanzas` throws when `errors` is non-empty; `errors` is also exposed so a
# check can read the messages, which builtins.tryEval cannot.
{
  lib,
  backendRows ? import ../lib/backends,
  connectorRows ? (import ./connector-knobs.nix { inherit lib; }).rows,
  schema ? import ../lib/env-schema.nix,
}:
let
  compose =
    row:
    let
      file = "lib/backends/${row.name}.nix";
      stanza = row.consumerStanza or null;
      listed = if stanza == null then [ ] else stanza.connectorKnobs;
      declared = (connectorRows.${row.name} or { }).knobs or { };
      tokenEntry =
        if row ? tokenEnvVar then
          lib.findFirst (e: (e.env or null) == row.tokenEnvVar) null (lib.attrValues schema)
        else
          null;
      missing = lib.optional (stanza == null) "${file}: row declares no consumerStanza";
      unknown = map (
        k: "${file}: consumerStanza.connectorKnobs names ${k}, which its Connector does not declare"
      ) (lib.filter (k: !(declared ? ${k})) listed);
      unlisted =
        map
          (
            k:
            "${file}: its Connector declares the secret knob ${k} (${declared.${k}.env}), which consumerStanza.connectorKnobs does not list"
          )
          (lib.filter (k: (declared.${k}.secret or false) && !(lib.elem k listed)) (lib.attrNames declared));
      noToken = lib.optional (
        row ? tokenEnvVar && tokenEntry == null
      ) "${file}: tokenEnvVar ${row.tokenEnvVar} is not in lib/env-schema.nix";
    in
    {
      errors = missing ++ unknown ++ unlisted ++ noToken;
      stanza = {
        inherit (row) name displayName;
        settings = lib.filterAttrs (_: k: k.flakeOption or false) (row.knobs or { });
        secrets = lib.optional (tokenEntry != null) tokenEntry ++ map (k: declared.${k}) listed;
        flakeInput = stanza.flakeInput or null;
        moduleImport = stanza.moduleImport or null;
      };
    };
  composed = map compose backendRows;
  # A backend Connector no row names would be dropped from the env example.
  orphans = map (n: "nix/connector-knobs.nix: backend Connector ${n} matches no lib/backends row") (
    lib.filter (n: (connectorRows.${n} ? backend) && !(lib.any (r: r.name == n) backendRows)) (
      lib.attrNames connectorRows
    )
  );
  errors = lib.concatMap (c: c.errors) composed ++ orphans;
in
{
  inherit errors;
  stanzas =
    if errors == [ ] then
      map (c: c.stanza) composed
    else
      throw "nix/consumer-stanzas.nix:\n${lib.concatStringsSep "\n" errors}";
}
