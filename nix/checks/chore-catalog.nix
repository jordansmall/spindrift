# Eval-level pins for lib/chore-catalog.nix, the single declaration of the
# built-in butler Chores (ADR 0056, issue #3991), plus mkHarness.nix's
# BUTLER_CHORES-vs-prompt-file assertion (issue #3991).
{
  pkgs,
  nixpkgs,
  system,
  ...
}:
let
  inherit (pkgs.lib)
    assertMsg
    concatStringsSep
    sort
    lessThan
    hasSuffix
    removeSuffix
    ;
  catalog = import ../../lib/chore-catalog.nix;
  choresDir = ../../templates/default/prompts/chores;
  promptStems = sort lessThan (
    map (removeSuffix ".md") (
      builtins.filter (n: hasSuffix ".md" n) (builtins.attrNames (builtins.readDir choresDir))
    )
  );
  catalogNamesSorted = sort lessThan catalog.names;
  tuningRow = builtins.head (builtins.filter (c: c.name == "tuning") catalog.chores);

  # Mirrors nix/checks/network-mode.nix's mkHarnessWith: forces only
  # `.spindrift.drvPath`, which walks mkHarness's assert chain without paying
  # for a real build. `choresDirArg` defaults to the built-in prompt dir; only
  # the Consumer-declared-chore case below overrides it.
  mkHarnessWith =
    {
      defaults,
      choresDirArg ? choresDir,
    }:
    (import ../../lib/mkHarness.nix {
      inherit nixpkgs system;
      packages = p: [ p.hello ];
      choresDir = choresDirArg;
      inherit defaults;
    }).spindrift.drvPath;
in
{
  chore-catalog-names-match-prompt-dir =
    assert assertMsg (catalogNamesSorted == promptStems)
      "lib/chore-catalog.nix's names (${concatStringsSep ", " catalogNamesSorted}) must equal templates/default/prompts/chores/*.md stems (${concatStringsSep ", " promptStems}) -- a prompt file added/removed without a matching catalog entry (or vice versa)";
    pkgs.runCommand "chore-catalog-names-match-prompt-dir" { } "touch $out";

  # A typo'd BUTLER_CHORES entry must fail eval, naming the offender, rather
  # than surface only after a Box claims a Ledger that never runs (issue
  # #3991).
  butler-chores-unknown-chore-throws =
    let
      broken = builtins.tryEval (
        builtins.seq (mkHarnessWith {
          defaults = {
            butlerChores = "bgus";
          };
        }) "unreached"
      );
    in
    assert assertMsg (!broken.success)
      "mkHarness.nix must throw when BUTLER_CHORES names a chore with no <name>.md prompt file in choresDir";
    pkgs.runCommand "butler-chores-unknown-chore-throws" { } "touch $out";

  butler-chores-built-ins-do-not-throw =
    let
      ok = builtins.tryEval (
        builtins.seq (mkHarnessWith {
          defaults = {
            butlerChores = "bugs docs-drift";
          };
        }) "reached"
      );
    in
    assert assertMsg ok.success
      "mkHarness.nix must not throw when BUTLER_CHORES names built-in chores that each have a prompt file";
    pkgs.runCommand "butler-chores-built-ins-do-not-throw" { } "touch $out";

  # A Consumer-declared Chore (a custom choresDir, ADR 0056) with a matching
  # prompt file must pass too -- the assertion checks choresDir, not the
  # built-in catalog.
  butler-chores-prompt-ok-with-custom-chores-dir =
    let
      ok = builtins.tryEval (
        builtins.seq (mkHarnessWith {
          defaults = {
            butlerChores = "custom-chore";
          };
          choresDirArg = ../fixtures/chores-consumer-declared;
        }) "reached"
      );
    in
    assert assertMsg ok.success
      "mkHarness.nix must not throw when BUTLER_CHORES names a Consumer-declared chore with a matching <name>.md in a custom choresDir";
    pkgs.runCommand "butler-chores-prompt-ok-with-custom-chores-dir" { } "touch $out";

  # The closed class list (issue #4766): every promotion and patch default
  # must sit on its chore's classList, or a Box could never classify into it.
  chore-catalog-promotion-class-off-list-throws =
    let
      broken = builtins.tryEval (
        builtins.deepSeq (catalog.checkClassLists [
          {
            name = "demo";
            classList = [ "a" ];
            promotionClasses = [
              "a"
              "b"
            ];
            patchClasses = [ ];
          }
        ]) "unreached"
      );
    in
    assert assertMsg (!broken.success)
      "checkClassLists must throw when a chore's promotionClasses names a class missing from its classList";
    pkgs.runCommand "chore-catalog-promotion-class-off-list-throws" { } "touch $out";

  chore-catalog-patch-class-off-list-throws =
    let
      broken = builtins.tryEval (
        builtins.deepSeq (catalog.checkClassLists [
          {
            name = "demo";
            classList = [ "a" ];
            promotionClasses = [ "a" ];
            patchClasses = [ "c" ];
          }
        ]) "unreached"
      );
    in
    assert assertMsg (
      !broken.success
    ) "checkClassLists must throw when a chore's patchClasses names a class missing from its classList";
    pkgs.runCommand "chore-catalog-patch-class-off-list-throws" { } "touch $out";

  # A records-scoped Chore (ADR 0062) never promotes or patches; the catalog
  # rejects a row that declares either. These override the real tuning row,
  # so the rule is pinned against the shipped scopeSource, not a stand-in.
  chore-catalog-records-scoped-promotion-throws =
    let
      broken = builtins.tryEval (
        builtins.deepSeq (catalog.checkClassLists [
          (tuningRow // { promotionClasses = [ (builtins.head tuningRow.classList) ]; })
        ]) "unreached"
      );
    in
    assert assertMsg (
      !broken.success
    ) "checkClassLists must throw when a records-scoped chore declares promotionClasses";
    pkgs.runCommand "chore-catalog-records-scoped-promotion-throws" { } "touch $out";

  chore-catalog-records-scoped-patch-throws =
    let
      broken = builtins.tryEval (
        builtins.deepSeq (catalog.checkClassLists [
          (tuningRow // { patchClasses = [ (builtins.head tuningRow.classList) ]; })
        ]) "unreached"
      );
    in
    assert assertMsg (
      !broken.success
    ) "checkClassLists must throw when a records-scoped chore declares patchClasses";
    pkgs.runCommand "chore-catalog-records-scoped-patch-throws" { } "touch $out";

  # scopeSource is a closed vocabulary: a typo must not silently make a
  # records-scoped Chore tree-scoped.
  chore-catalog-unknown-scope-source-throws =
    let
      broken = builtins.tryEval (
        builtins.deepSeq (catalog.checkClassLists [
          (tuningRow // { scopeSource = "record"; })
        ]) "unreached"
      );
    in
    assert assertMsg (
      !broken.success
    ) "checkClassLists must throw when a chore's scopeSource is neither \"tree\" nor \"records\"";
    pkgs.runCommand "chore-catalog-unknown-scope-source-throws" { } "touch $out";

  # The rules above key on scopeSource, so pin tuning itself to "records".
  chore-catalog-tuning-is-records-scoped =
    assert assertMsg (
      (tuningRow.scopeSource or "tree") == "records"
    ) "lib/chore-catalog.nix's tuning row must declare scopeSource = \"records\" (ADR 0062)";
    pkgs.runCommand "chore-catalog-tuning-is-records-scoped" { } "touch $out";

  # ADR 0070: tuning's PRs are merged by hand.
  chore-catalog-tuning-merges-by-hand =
    assert assertMsg (builtins.elem "tuning" catalog.mergeByHand)
      "lib/chore-catalog.nix: the tuning row must set mergeByHand = true (ADR 0070)";
    pkgs.runCommand "chore-catalog-tuning-merges-by-hand" { } "touch $out";

  # The hold's PR comment names findingLabel, so a mergeByHand row without
  # one must fail eval rather than render an empty label.
  chore-catalog-merge-by-hand-needs-finding-label-throws =
    let
      broken = builtins.tryEval (
        builtins.deepSeq (catalog.checkClassLists [
          (builtins.removeAttrs tuningRow [ "findingLabel" ])
        ]) "unreached"
      );
    in
    assert assertMsg (
      !broken.success
    ) "checkClassLists must throw when a mergeByHand chore declares no findingLabel";
    pkgs.runCommand "chore-catalog-merge-by-hand-needs-finding-label-throws" { } "touch $out";

  # A records-scoped Chore's due test reads its minimum new-Record count from
  # a knob, so the row must name one of its own knobs (issue #5700).
  chore-catalog-records-scoped-needs-min-records-knob-throws =
    let
      broken = builtins.tryEval (
        builtins.deepSeq (catalog.checkClassLists [
          (builtins.removeAttrs tuningRow [ "minRecordsKnob" ])
        ]) "unreached"
      );
    in
    assert assertMsg (
      !broken.success
    ) "checkClassLists must throw when a records-scoped chore declares no minRecordsKnob";
    pkgs.runCommand "chore-catalog-records-scoped-needs-min-records-knob-throws" { } "touch $out";

  chore-catalog-min-records-knob-must-be-own-knob-throws =
    let
      broken = builtins.tryEval (
        builtins.deepSeq (catalog.checkClassLists [
          (tuningRow // { minRecordsKnob = "butlerTuningNoSuchKnob"; })
        ]) "unreached"
      );
    in
    assert assertMsg (
      !broken.success
    ) "checkClassLists must throw when minRecordsKnob names a key outside the chore's own knobs";
    pkgs.runCommand "chore-catalog-min-records-knob-must-be-own-knob-throws" { } "touch $out";

  chore-catalog-tuning-min-records-knob =
    assert assertMsg
      (
        catalog.minRecordsKnobs == {
          tuning = "BUTLER_TUNING_MIN_RECORDS";
        }
      )
      "lib/chore-catalog.nix: minRecordsKnobs must map tuning to BUTLER_TUNING_MIN_RECORDS (issue #5700)";
    pkgs.runCommand "chore-catalog-tuning-min-records-knob" { } "touch $out";

  chore-catalog-tuning-min-records-default =
    assert assertMsg
      (
        catalog.minRecordsDefaults == {
          tuning = tuningRow.knobs.butlerTuningMinRecords.default;
        }
      )
      "lib/chore-catalog.nix: minRecordsDefaults must map tuning to the row's butlerTuningMinRecords default (issue #5791)";
    pkgs.runCommand "chore-catalog-tuning-min-records-default" { } "touch $out";

  chore-catalog-built-ins-class-lists-ok =
    let
      ok = builtins.tryEval (builtins.deepSeq catalog.classLists "reached");
    in
    assert assertMsg ok.success
      "the built-in catalog's promotionClasses and patchClasses must all sit on their classList";
    pkgs.runCommand "chore-catalog-built-ins-class-lists-ok" { } "touch $out";
}
