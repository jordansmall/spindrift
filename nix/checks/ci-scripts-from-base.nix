# Pins ci.yml's `changes` and `ci-ok` jobs to the PR base's copy of the CI
# scripts (issue #5155): the scripts sit outside .github/workflows/, so a PR
# running its own copy could rewrite the required check's verdict.
{ pkgs, ... }:
let
  inherit (pkgs.lib)
    assertMsg
    hasInfix
    length
    splitString
    ;
  ciSrc = builtins.readFile ../../.github/workflows/ci.yml;
  occurrences = needle: length (splitString needle ciSrc) - 1;
  # `changes` and `ci-ok`.
  scriptJobs = 2;
in
{
  ci-scripts-from-base =
    assert assertMsg (occurrences "git show \"$BASE_SHA:.github/scripts/$f\"" == scriptJobs)
      "ci.yml no longer swaps in the PR base's CI scripts in both `changes` and `ci-ok` — a PR could rewrite its own required-check verdict.";
    assert assertMsg
      (
        !(hasInfix "kind=$(.github/scripts/" ciSrc)
        && !(hasInfix "\n          .github/scripts/ci-ok.sh" ciSrc)
      )
      "ci.yml runs a CI script by path instead of through `bash` — a script the base swap recreated has no executable bit.";
    pkgs.runCommand "ci-scripts-from-base" { } "touch $out";
}
