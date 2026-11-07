# Eval-level pins for the Gate vocabulary (issue #6589): mkHarness throws when
# a fragment row names a gate outside lib/gate-vocabulary.nix, so a typo'd gate
# cannot silently render nothing. mkHarnessWith forces only
# `.spindrift.drvPath`, which walks the assert chain without a real build.
{
  pkgs,
  nixpkgs,
  system,
  ...
}:
let
  inherit (pkgs.lib) assertMsg;
  vocab = import ../../lib/gate-vocabulary.nix;
  mkHarnessWith =
    gateVocabulary: fragments:
    (import ../../lib/mkHarness.nix {
      inherit
        nixpkgs
        system
        fragments
        gateVocabulary
        ;
      packages = p: [ p.hello ];
    }).spindrift.drvPath;
  evaluatesWith =
    gateVocabulary: fragments:
    (builtins.tryEval (builtins.seq (mkHarnessWith gateVocabulary fragments) "reached")).success;
  evaluates = evaluatesWith vocab;

  typoRow = {
    gate = "FILER_ENABLD";
    fragment = "skill-preamble.md";
    var = "TYPO_STEP";
  };
  # typoRow's registry with the typo'd name added to the vocabulary.
  vocabWithTypo = vocab // {
    gates = vocab.gates // {
      FILER_ENABLD = "test-only gate standing in for a typo";
    };
  };
  defaultRows = import ../../lib/fragments.nix;
in
{
  gate-vocabulary-default-registry-evaluates =
    assert assertMsg (evaluates defaultRows)
      "mkHarness must accept the default fragment registry: every row's gate is in lib/gate-vocabulary.nix";
    pkgs.runCommand "gate-vocabulary-default-registry-evaluates" { } "touch $out";

  gate-vocabulary-unknown-gate-appended-throws =
    assert assertMsg (!evaluates (defaultRows ++ [ typoRow ]))
      "mkHarness must throw when a row appended to the default registry names a gate outside the vocabulary";
    pkgs.runCommand "gate-vocabulary-unknown-gate-appended-throws" { } "touch $out";

  gate-vocabulary-unknown-gate-replacement-throws =
    assert assertMsg (!evaluates [ typoRow ])
      "mkHarness must throw when a Consumer registry replacing the default names a gate outside the vocabulary";
    pkgs.runCommand "gate-vocabulary-unknown-gate-replacement-throws" { } "touch $out";

  # Positive control: only the vocabulary differs from the case above, so that
  # case's failure is the gate assert and not some other assert.
  gate-vocabulary-unknown-gate-replacement-control-evaluates =
    assert assertMsg (evaluatesWith vocabWithTypo [
      typoRow
    ]) "the [ typoRow ] registry must evaluate once its gate is in the vocabulary";
    pkgs.runCommand "gate-vocabulary-unknown-gate-replacement-control-evaluates" { } "touch $out";

  gate-vocabulary-non-string-gate-throws =
    assert assertMsg (
      !evaluates [ (typoRow // { gate = null; }) ]
    ) "mkHarness must throw catchably when a row's gate is not a string";
    pkgs.runCommand "gate-vocabulary-non-string-gate-throws" { } "touch $out";
}
