# Schema drift guards: every committed generated artifact (Driver name table,
# harness.env.example, launcher flag table, flake-options doc, template
# settings example, man page) must stay in sync with its schema source.
# Shares its renderers with `nix run .#regen` via lib/renderers.nix so the
# guard and the regenerator can never drift from each other (issue #402).
{ pkgs, fixtures, ... }:
let
  inherit (fixtures) harness;
  renderers = import ../../lib/renderers.nix;
in
{
  # cmd/launcher/internal/driver/drivernames_gen.go must match the key list
  # derived from lib/drivers/default.nix. Fails when a Driver is added to the
  # Nix registry but the committed generated file is not regenerated. Shares
  # its renderer with `nix run .#regen` via lib/renderers.nix (issue #436).
  driver-names-gen =
    let
      driverRegistry = import ../../lib/drivers/default.nix { inherit (pkgs) lib; };
      generated = pkgs.writeText "drivernames_gen.go.generated" (
        renderers.renderDriverNamesGo driverRegistry
      );
    in
    pkgs.runCommand "driver-names-gen"
      {
        inherit generated;
        committed = ../../cmd/launcher/internal/driver/drivernames_gen.go;
      }
      ''
        diff "$generated" "$committed" \
          || { echo "cmd/launcher/internal/driver/drivernames_gen.go is out of sync with lib/drivers/default.nix — regenerate it with \`nix run .#regen\`" >&2; exit 1; }
        touch $out
      '';

  # harness.env.example must match the content generated from env-schema.nix.
  # Fails when a new schema knob is added but the committed file is not
  # regenerated (golden-file drift; resolves issue #109). Shares its renderer
  # with `nix run .#regen` (nix/regen.nix) via lib/renderers.nix — the guard
  # and the regenerator cannot drift from each other (issue #402).
  harness-env-example =
    let
      schema = import ../../lib/env-schema.nix;
      generated = pkgs.writeText "harness.env.example.generated" (
        renderers.renderHarnessEnvExample schema
      );
    in
    pkgs.runCommand "harness-env-example"
      {
        inherit generated;
        committed = ../../templates/default/harness.env.example;
      }
      ''
        diff "$generated" "$committed" \
          || { echo "templates/default/harness.env.example is out of sync with lib/env-schema.nix — regenerate it" >&2; exit 1; }
        touch $out
      '';

  # Every env-var string literal in cmd/launcher/main.go must have a
  # matching entry in lib/env-schema.nix, and vice-versa (presence-only;
  # value-level pinning would be refactor-brittle).  A set of known
  # nix-baked vars is excluded from the main.go side.
  launcher-env-coverage =
    let
      schema = import ../../lib/env-schema.nix;
      inherit (pkgs.lib)
        attrValues
        concatStringsSep
        filter
        subtractLists
        ;
      mainGoSrc = builtins.readFile ../../cmd/launcher/main.go;
      # Env var names that main.go reads but that are nix-generated
      # (not user-facing knobs): excluded from the schema-coverage check.
      nixBaked = [
        "IMAGE_ARCHIVE"
        "IMAGE_TAG"
        "IMAGE_DRV"
        "NIX_BUILDER_IMAGE"
        "NIX_VOLUME"
        "FLAKE_IMAGE_ATTR"
        "AGENT_FILES"
        "AGENT_ENV"
        "AGENT_FILES_DRV"
        "AGENT_ENV_DRV"
        "BAKED_PREFETCH"
        "RUNTIME"
        "DRIVER"
        "DRIVER_SKILLS_DIR"
        "DRIVER_SESSION_CACHE_DIR"
        "IMAGE"
        "BOX_ENV_VARS"
      ];
      schemaEnvNames = map (e: e.env) (attrValues schema);
      # Schema knobs forwarded to containers via BOX_ENV_VARS only — the Go
      # binary never reads them directly, so they need no os.Getenv call.
      boxEnvOnly = [
        "MODEL"
        "SCOUT_MODEL"
        "REVIEW_MODEL"
        "FILER_MODEL"
        "DEV_SHELL_NAME"
        "DEV_SHELL_PROBE_TIMEOUT"
        "AUTO_FORMAT"
        "AUTO_LINT"
      ];
      # Forward: every schema name (that Go reads directly) must appear as a
      # string literal in main.go.
      missingFromGo = filter (name: !pkgs.lib.hasInfix ''"${name}"'' mainGoSrc) (
        subtractLists boxEnvOnly schemaEnvNames
      );
      # Reverse: extract names from os.Getenv/getenv calls in main.go.
      parts = builtins.split ''(os\.Getenv|getenv)\("([A-Z_][A-Z0-9_]*)"\)'' mainGoSrc;
      goEnvNames = map (m: builtins.elemAt m 1) (filter builtins.isList parts);
      extraInGo = subtractLists (schemaEnvNames ++ nixBaked) goEnvNames;
    in
    assert pkgs.lib.assertMsg (
      missingFromGo == [ ]
    ) "schema knobs absent from main.go: ${concatStringsSep ", " missingFromGo}";
    assert pkgs.lib.assertMsg (
      extraInGo == [ ]
    ) "main.go reads env vars absent from schema: ${concatStringsSep ", " extraInGo}";
    pkgs.runCommand "launcher-env-coverage" { } "touch $out";

  # tests/helper.bash's set_box_env fixture must export every boxEnv = true
  # schema knob, so entrypoint.bats exercises the same defaults the nix
  # preamble bakes into the image at build time (issue #462). Presence-only,
  # like launcher-env-coverage above — catches a new boxEnv knob added to the
  # schema with no matching export added to set_box_env.
  box-env-fixture-coverage =
    let
      schema = import ../../lib/env-schema.nix;
      inherit (pkgs.lib)
        attrValues
        concatStringsSep
        filter
        hasInfix
        ;
      boxEnvNames = map (e: e.env) (filter (e: e.boxEnv or false) (attrValues schema));
      helperSrc = builtins.readFile ../../tests/helper.bash;
      missing = filter (name: !hasInfix "export ${name}=" helperSrc) boxEnvNames;
    in
    assert pkgs.lib.assertMsg (
      missing == [ ]
    ) "tests/helper.bash's set_box_env is missing boxEnv knobs: ${concatStringsSep ", " missing}";
    pkgs.runCommand "box-env-fixture-coverage" { } "touch $out";

  # cmd/launcher/flagtable_gen.go must match the content generated from
  # env-schema.nix by mkHarness.nix renderFlagTableGo.  Fails when a new
  # schema knob is added but the committed generated file is not regenerated.
  # Shares its renderer with `nix run .#regen` via lib/renderers.nix.
  launcher-flag-table =
    let
      schema = import ../../lib/env-schema.nix;
      generated = pkgs.writeText "flagtable_gen.go.generated" (renderers.renderFlagTableGo schema);
    in
    pkgs.runCommand "launcher-flag-table"
      {
        inherit generated;
        committed = ../../cmd/launcher/flagtable_gen.go;
      }
      ''
        diff "$generated" "$committed" \
          || { echo "cmd/launcher/flagtable_gen.go is out of sync with lib/env-schema.nix — regenerate it" >&2; exit 1; }
        touch $out
      '';

  # docs/flake-options.md must match the reference generated from env-schema.nix.
  # Fails when a flakeOption knob is added or removed but the committed file is
  # not regenerated (same treatment as harness.env.example and flagtable_gen.go).
  # Shares its renderer with `nix run .#regen` via lib/renderers.nix.
  flake-options-doc =
    let
      schema = import ../../lib/env-schema.nix;
      generated = pkgs.writeText "flake-options.md.generated" (renderers.renderFlakeOptionsDoc schema);
    in
    pkgs.runCommand "flake-options-doc"
      {
        inherit generated;
        committed = ../../docs/flake-options.md;
      }
      ''
        diff "$generated" "$committed" \
          || { echo "docs/flake-options.md is out of sync with lib/env-schema.nix — regenerate it" >&2; exit 1; }
        touch $out
      '';

  # templates/default/flake.nix settings example block must cover every section
  # and every knob in the schema-derived flakeOption surface.  Fails when a new
  # section or knob is added to env-schema.nix but the template is not updated.
  template-settings-example =
    let
      schema = import ../../lib/env-schema.nix;
      inherit (pkgs.lib)
        attrNames
        concatStringsSep
        filter
        filterAttrs
        foldl'
        hasInfix
        ;
      flakeOptionEntries = filterAttrs (_: e: e.flakeOption or false) schema;
      # Map sectionAttr -> [knobName] for all flakeOption knobs. groupToAttr
      # comes from lib/renderers.nix — lib/flakeModule.nix imports the same
      # mapping, and flake-options-doc renders from it too.
      sectionKnobs = foldl' (
        acc: knobName:
        let
          entry = flakeOptionEntries.${knobName};
          sectionAttr = renderers.groupToAttr.${entry.group} or null;
        in
        if sectionAttr == null then
          acc
        else
          acc
          // {
            ${sectionAttr} = (acc.${sectionAttr} or [ ]) ++ [ knobName ];
          }
      ) { } (attrNames flakeOptionEntries);
      templateSrc = builtins.readFile ../../templates/default/flake.nix;
      missingSections = filter (s: !(hasInfix s templateSrc)) (attrNames sectionKnobs);
      missingKnobs = pkgs.lib.concatLists (
        pkgs.lib.mapAttrsToList (_section: knobs: filter (k: !(hasInfix k templateSrc)) knobs) sectionKnobs
      );
    in
    assert pkgs.lib.assertMsg (missingSections == [ ])
      "templates/default/flake.nix settings example is missing sections: ${concatStringsSep ", " missingSections}";
    assert pkgs.lib.assertMsg (missingKnobs == [ ])
      "templates/default/flake.nix settings example is missing knobs: ${concatStringsSep ", " missingKnobs}";
    pkgs.runCommand "template-settings-example" { } "touch $out";

  # cmd/launcher/flags.go's groupOrder must list the same groups, in the same
  # order, as lib/renderers.nix groupOrder. Go stays hand-written (issue #105:
  # generation was rejected) — this pins the copy instead of replacing it.
  launcher-grouporder =
    let
      inherit (pkgs.lib) concatStringsSep assertMsg;
      flagsSrc = builtins.readFile ../../cmd/launcher/flags.go;
      markerParts = builtins.split "var groupOrder = " flagsSrc;
      afterMarker =
        if builtins.length markerParts >= 3 then
          builtins.elemAt markerParts 2
        else
          throw "cmd/launcher/flags.go: `var groupOrder = ` declaration not found";
      body = builtins.elemAt (builtins.split "\n}\n" afterMarker) 0 + "\n";
      entryMatches = builtins.split "\"([^\"]*)\",?\n" body;
      goGroupOrder = builtins.filter builtins.isString (
        builtins.concatMap (x: if builtins.isList x then x else [ ]) entryMatches
      );
    in
    assert assertMsg (goGroupOrder == renderers.groupOrder) ''
      cmd/launcher/flags.go groupOrder is out of sync with lib/renderers.nix groupOrder.
        got:  [${concatStringsSep ", " goGroupOrder}]
        want: [${concatStringsSep ", " renderers.groupOrder}]'';
    pkgs.runCommand "launcher-grouporder" { } "touch $out";

  # The generated man page must render (mandoc parses it) and totally cover the
  # schema: every SH section, every OPTIONS group, every non-secret flag, and
  # every secret env var. A new knob with no man-page presence fails here.
  launcher-manpage =
    let
      schema = import ../../lib/env-schema.nix;
      inherit (pkgs.lib)
        filter
        attrValues
        concatMapStrings
        replaceStrings
        unique
        ;
      # Roff renders the flag as \-\- with every hyphen escaped; match that
      # form. toKebab comes from lib/renderers.nix — the same helper the man
      # page itself is rendered through.
      roffFlag = e: "\\-\\-" + replaceStrings [ "-" ] [ "\\-" ] (renderers.toKebab e.env);
      nonSecret = filter (e: !(e.secret or false)) (attrValues schema);
      secretEntries = filter (e: e.secret or false) (attrValues schema);
      groups = unique (map (e: e.group) nonSecret);
      groupChecks = concatMapStrings (g: "need -F '.SS ${g}'\n") groups;
      flagChecks = concatMapStrings (e: "need -F '${roffFlag e}'\n") nonSecret;
      secretChecks = concatMapStrings (e: "need -F '${e.env}'\n") secretEntries;
    in
    pkgs.runCommand "launcher-manpage"
      {
        nativeBuildInputs = [ pkgs.mandoc ];
        man = "${harness.manpage}/share/man/man1/spindrift.1";
      }
      ''
        need() { grep -q "$@" "$man" || { echo "man page missing: $*" >&2; exit 1; }; }
        # Renders without a fatal parse error.
        mandoc -man -Tascii "$man" >/dev/null
        for s in NAME SYNOPSIS DESCRIPTION SUBCOMMANDS OPTIONS ENVIRONMENT FILES EXAMPLES; do
          grep -Eq "^\.SH \"?$s" "$man" || { echo "man page missing .SH $s" >&2; exit 1; }
        done
        ${groupChecks}
        ${flagChecks}
        ${secretChecks}
        touch $out
      '';
}
