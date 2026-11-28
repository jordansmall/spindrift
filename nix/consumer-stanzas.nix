# Each backend row's Consumer stanza (issue #7415): what a Consumer of that
# backend configures, composed from the row (lib/backends/<name>.nix) and its
# Connector's knobs (nix/connector-knobs.nix).
#
# Returns `{ stanzas; errors; }`. `stanzas` is one entry per backend row, in
# registry order: `{ name; displayName; settings; secrets; flakeInput;
# connectorData; credential; }`, where `settings` is the row's own `flakeOption`
# knobs, `secrets` is the row's `tokenEnvVar` schema entry (when it has one),
# then its Connector's Basic `identity` when that names a core schema knob (a
# Connector-owned identity is a listed connector knob instead), then the
# Connector knobs its `consumerStanza.connectorKnobs` lists, and
# `credential` is the env of the knob its row's `backend.credential` names: a
# Connector knob, or the row's own backend's core token (flakeModule, not this
# file, validates the name; null when the backend has no Connector). Forcing
# `stanzas` throws when `errors` is non-empty; `errors` is also exposed so a
# check can read the messages, which builtins.tryEval cannot.
{
  lib,
  backendRows ? import ../lib/backends,
  connectorRows ? (import ./connector-knobs.nix { inherit lib; }).rows,
  schema ? import ../lib/env-schema.nix,
}:
let
  quickstartAxes = import ../lib/quickstart-axes.nix;
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
      identityEntry =
        let
          id = ((connectorRows.${row.name} or { }).backend or { }).identity or null;
        in
        if id != null && !(declared ? ${id}) then schema.${id} else null;
      ownSettings = lib.filterAttrs (_: k: k.flakeOption or false) (row.knobs or { });
      # A knob serves the axis its group names unless it sets `axes` itself.
      axisOfGroup = {
        issues = "tracker";
        forge = "forge";
      };
      axesOf = k: k.axes or (lib.optional (axisOfGroup ? ${k.group or ""}) axisOfGroup.${k.group or ""});
      badAxes = lib.concatMap (
        name:
        let
          k = ownSettings.${name};
          axes = axesOf k;
          where = "${file}: settings knob ${name}";
          list = lib.concatStringsSep ", ";
          unknownAxes = lib.filter (a: !(lib.elem a quickstartAxes)) axes;
          # Checked first: a non-string-list `axes` must reach the named error,
          # not lib.elem or concatStringsSep.
          strings = builtins.isList axes && lib.all builtins.isString axes;
        in
        lib.optional (
          !strings
        ) "${where}: `axes` must be a list of strings drawn from ${list quickstartAxes}"
        ++ lib.optional (strings && axes == [ ]) (
          if k ? axes then
            "${where}: `axes` must be non-empty, drawn from ${list quickstartAxes}"
          else
            "${where}: group \"${k.group or ""}\" implies no axis, so set `axes` from ${list quickstartAxes}"
        )
        ++ lib.optional (
          strings && unknownAxes != [ ]
        ) "${where}: `axes` has unknown axis(es): ${list unknownAxes} (valid: ${list quickstartAxes})"
      ) (lib.attrNames ownSettings);
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
      errors = missing ++ badAxes ++ unknown ++ unlisted ++ noToken;
      stanza = {
        inherit (row) name displayName;
        settings = lib.mapAttrs (_: k: k // { axes = axesOf k; }) ownSettings;
        secrets =
          lib.optional (tokenEntry != null) tokenEntry
          ++ lib.optional (identityEntry != null) identityEntry
          ++ map (k: declared.${k}) listed;
        flakeInput = stanza.flakeInput or null;
        connectorData = stanza.connectorData or null;
        credential =
          let
            connector = connectorRows.${row.name} or { };
          in
          if connector ? backend then
            (declared.${connector.backend.credential} or schema.${connector.backend.credential}).env
          else
            null;
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
  stanzas =
    if errors == [ ] then
      map (c: c.stanza) composed
    else
      throw "nix/consumer-stanzas.nix:\n${lib.concatStringsSep "\n" errors}";
in
{
  inherit errors stanzas;
  # A template's env example lists the core rows (no Connector, so a null
  # credential) plus exactly the Connectors its flake takes as
  # `?dir=connectors/<name>` inputs, so editing the inputs and running regen
  # keeps the two together.
  templateStanzas =
    templateDir:
    let
      inputs = (import (templateDir + "/flake.nix")).inputs;
      matches = lib.concatMap (
        i: lib.optional (i ? url) (builtins.match ".*[?&]dir=connectors/([^&#/]+).*" i.url)
      ) (lib.attrValues inputs);
      connectorNames = lib.concatMap (m: lib.optional (m != null) (lib.head m)) matches;
      # A dir named differently from its row would otherwise drop its secrets.
      unmatched = lib.filter (n: !(lib.any (s: s.name == n) stanzas)) connectorNames;
    in
    if unmatched != [ ] then
      throw "nix/consumer-stanzas.nix: ${templateDir} takes connectors/${lib.concatStringsSep ", connectors/" unmatched}, matching no lib/backends row name"
    else
      lib.filter (s: s.credential == null || lib.elem s.name connectorNames) stanzas;
}
