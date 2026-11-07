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
  mkHarnessWith =
    fragments:
    (import ../../lib/mkHarness.nix {
      inherit nixpkgs system fragments;
      packages = p: [ p.hello ];
    }).spindrift.drvPath;
  evaluates = fragments: (builtins.tryEval (builtins.seq (mkHarnessWith fragments) "reached")).success;

  typoRow = {
    gate = "FILER_ENABLD";
    fragment = "skill-preamble.md";
    var = "TYPO_STEP";
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
}
