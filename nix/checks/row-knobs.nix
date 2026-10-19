# Eval-level pins for lib/row-knobs.nix, the fold that gives registry rows
# their own knobs in the env schema (ADR 0068 decision 2, issue #5634).
# tryEval exposes only success or failure, never the thrown text, so the
# "message names both sources" pins read the helper's pure `collisions` list.
{ pkgs, ... }:
let
  inherit (pkgs.lib) assertMsg hasInfix;
  rowKnobs = import ../../lib/row-knobs.nix;

  shared = {
    sharedKnob = {
      env = "SHARED_KNOB";
      doc = "shared";
    };
  };
  stubKnob = {
    env = "STUB_KNOB";
    doc = "a row-owned knob";
  };
  sharedSource = "lib/env-schema.nix";
  mkRegistry = source: rows: { inherit source rows; };

  clean = {
    inherit shared sharedSource;
    registries = [
      (mkRegistry "lib/stubs" [
        {
          name = "stub";
          knobs = {
            inherit stubKnob;
          };
        }
        { name = "plain"; }
      ])
    ];
  };

  sharedClash = {
    inherit shared sharedSource;
    registries = [
      (mkRegistry "lib/stubs" [
        {
          name = "stub";
          knobs.stubKnob = {
            env = "SHARED_KNOB";
            doc = "reuses a shared env name";
          };
        }
      ])
    ];
  };

  rowClash = {
    inherit shared sharedSource;
    registries = [
      (mkRegistry "lib/stubs" [
        {
          name = "one";
          knobs.oneKnob = stubKnob;
        }
      ])
      (mkRegistry "lib/others" [
        {
          name = "two";
          knobs.twoKnob = stubKnob;
        }
      ])
    ];
  };

  keyClash = {
    inherit shared sharedSource;
    registries = [
      (mkRegistry "lib/stubs" [
        {
          name = "stub";
          knobs.sharedKnob = stubKnob;
        }
      ])
    ];
  };

  keyDupe = {
    inherit shared sharedSource;
    registries = [
      (mkRegistry "lib/stubs" [
        {
          name = "one";
          knobs.sameKey = stubKnob;
        }
        {
          name = "two";
          knobs.sameKey = {
            env = "OTHER_KNOB";
            doc = "same key, different env name";
          };
        }
      ])
    ];
  };

  folded = rowKnobs.fold clean;
  realSchema = import ../../lib/env-schema.nix;
  failsToFold = args: !(builtins.tryEval (builtins.deepSeq (rowKnobs.fold args) true)).success;
  rowsMissingFile = builtins.concatMap (
    registry:
    builtins.concatMap (
      row:
      let
        path = "${registry.source}/${row.name}.nix";
      in
      if row ? knobs && !builtins.pathExists (../.. + "/${path}") then [ path ] else [ ]
    ) registry.rows
  ) (import ../../lib/row-knob-registries.nix);
  messages = args: builtins.concatStringsSep "\n" (rowKnobs.collisions args);
in
{
  row-knobs =
    assert assertMsg (
      folded.stubKnob == stubKnob && folded.sharedKnob == shared.sharedKnob
    ) "row-knobs: a row's knob must appear in the fold under its own key, entry unchanged";
    assert assertMsg (
      builtins.attrNames folded == [
        "sharedKnob"
        "stubKnob"
      ]
    ) "row-knobs: the fold must add exactly the rows' knobs to the shared entries";
    assert assertMsg (rowKnobs.collisions clean == [ ]) "row-knobs: a clean fold reports no collisions";
    assert assertMsg (failsToFold sharedClash)
      "row-knobs: a row knob reusing a shared env name must fail eval";
    assert assertMsg (
      hasInfix "lib/stubs/stub.nix" (messages sharedClash)
      && hasInfix "lib/env-schema.nix" (messages sharedClash)
      && hasInfix "SHARED_KNOB" (messages sharedClash)
    ) "row-knobs: the shared-env collision message must name both sources and the env";
    assert assertMsg (failsToFold rowClash) "row-knobs: two rows declaring one env name must fail eval";
    assert assertMsg (
      hasInfix "lib/stubs/one.nix" (messages rowClash)
      && hasInfix "lib/others/two.nix" (messages rowClash)
      && hasInfix "STUB_KNOB" (messages rowClash)
    ) "row-knobs: the row-vs-row collision message must name both rows' sources and the env";
    assert assertMsg (failsToFold keyClash) "row-knobs: a row knob reusing a shared key must fail eval";
    assert assertMsg (
      hasInfix "lib/stubs/stub.nix" (messages keyClash)
      && hasInfix "lib/env-schema.nix" (messages keyClash)
    ) "row-knobs: the key collision message must name both sources";
    assert assertMsg (failsToFold keyDupe) "row-knobs: two rows declaring one key must fail eval";
    assert assertMsg (
      hasInfix "lib/stubs/one.nix" (messages keyDupe) && hasInfix "lib/stubs/two.nix" (messages keyDupe)
    ) "row-knobs: the duplicate-key collision message must name both rows' sources";
    assert assertMsg
      (
        realSchema.forgejoToken.env == "FORGEJO_TOKEN"
        && realSchema.forgejoBaseURL.env == "FORGEJO_BASE_URL"
        && realSchema.boxForgejoToken.env == "BOX_FORGEJO_TOKEN"
      )
      "row-knobs: forgejo's row-owned knobs must appear in the real folded schema under their env names";
    assert assertMsg (rowsMissingFile == [ ])
      "row-knobs: a registry row carrying knobs must have a file at <source>/<name>.nix; missing: ${builtins.concatStringsSep ", " rowsMissingFile}";
    assert assertMsg (
      realSchema ? label && realSchema.label.env == "LABEL"
    ) "row-knobs: lib/env-schema.nix must still export its entries through the fold";
    pkgs.runCommand "row-knobs" { } "touch $out";
}
