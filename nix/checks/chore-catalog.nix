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

  chore-catalog-built-ins-class-lists-ok =
    let
      ok = builtins.tryEval (builtins.deepSeq catalog.classLists "reached");
    in
    assert assertMsg ok.success
      "the built-in catalog's promotionClasses and patchClasses must all sit on their classList";
    pkgs.runCommand "chore-catalog-built-ins-class-lists-ok" { } "touch $out";
}
