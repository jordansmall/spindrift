# Eval-level pins for lib/drivers/default.nix (issue #624): the registry's
# required-attribute shape assertion and its registry-owned preamble/function
# renderers, on top of the byte-identity equivalence checks in equivalence.nix
# that already cover mkHarness.nix's generated output.
{ pkgs, ... }:
let
  driverRegistry = import ../../lib/drivers/default.nix { inherit (pkgs) lib; };
  inherit (pkgs.lib) assertMsg;
in
{
  drivers-assert-shape-missing-attribute-throws =
    let
      incomplete = {
        name = "incomplete";
        package = pkgs: pkgs.hello;
        bin = "incomplete";
        flagsCommon = "";
        skillsDirRelative = ".incomplete/skills";
        outcomeExtractFnBody = "";
        # sessionFlagsFnBody and agentsJsonTemplate deliberately omitted.
      };
      result = builtins.tryEval (driverRegistry.assertShape "incomplete" incomplete);
    in
    assert assertMsg (
      !result.success
    ) "assertShape must throw when a Driver entry is missing a required attribute";
    pkgs.runCommand "drivers-assert-shape-missing-attribute-throws" { } "touch $out";
}
