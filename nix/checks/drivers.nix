# Eval-level pins for lib/drivers/default.nix (issue #624): the registry's
# required-attribute shape assertion, on top of nix/checks/bats.nix's use of
# the same renderPreamble output the image bakes in.
{ pkgs, ... }:
let
  lib = pkgs.lib;
  driverRegistry = import ../../lib/drivers/default.nix { inherit lib; };
  claudeEntry = import ../../lib/drivers/claude.nix { inherit lib; };
  inherit (pkgs.lib) assertMsg hasInfix;
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

  drivers-render-preamble-shape =
    let
      out = driverRegistry.renderPreamble {
        bin = "stub-cli";
        flagsCommon = "--stub-flag --two";
        skillsDirRelative = ".stub/skills";
        outcomeExtractFnBody = "echo stub-outcome\n";
        sessionFlagsFnBody = "echo stub-session\n";
      };
    in
    assert assertMsg (hasInfix "DRIVER_BIN=stub-cli" out)
      "renderPreamble must bake DRIVER_BIN from the Driver entry's bin, got: ${out}";
    assert assertMsg (hasInfix "DRIVER_FLAGS_COMMON='--stub-flag --two'" out)
      "renderPreamble must shell-escape DRIVER_FLAGS_COMMON from the Driver entry's flagsCommon, got: ${out}";
    assert assertMsg (hasInfix ''DRIVER_SKILLS_DIR="''${HOME:-}/.stub/skills"'' out)
      "renderPreamble must compute DRIVER_SKILLS_DIR from \$HOME at run time, got: ${out}";
    assert assertMsg (hasInfix "_driver_extract_outcome() {\necho stub-outcome" out)
      "renderPreamble must fold in the Driver entry's outcomeExtractFnBody, got: ${out}";
    assert assertMsg (hasInfix "_driver_session_flags() {\necho stub-session" out)
      "renderPreamble must fold in the Driver entry's sessionFlagsFnBody, got: ${out}";
    pkgs.runCommand "drivers-render-preamble-shape" { } "touch $out";

  # DRIVER_SKILLS_DIR computes from $HOME at run time (issue #624) instead of
  # the absolute /home/agent/.claude/skills literal mkHarness.nix used to
  # bake -- a deliberate change from a second, independently-baked copy of
  # /home/agent to the one the image's own `HOME=/home/agent` Env entry
  # (lib/image.nix) already sets, not a behaviour change. This actually
  # sources and runs the rendered preamble with HOME set to the image's
  # fixed value and asserts the resulting DRIVER_SKILLS_DIR is byte-identical
  # to what mkHarness.nix baked before -- an empirical no-behaviour-change
  # proof, not just a literal-source-bytes comparison.
  drivers-claude-skills-dir-matches-image-home =
    let
      preamble = driverRegistry.renderPreamble claudeEntry;
      script = pkgs.writeText "drivers-claude-skills-dir.sh" ''
        HOME=/home/agent
        ${preamble}
        printf '%s' "$DRIVER_SKILLS_DIR"
      '';
    in
    pkgs.runCommand "drivers-claude-skills-dir-matches-image-home"
      {
        nativeBuildInputs = [ pkgs.bash ];
      }
      ''
        resolved="$(bash ${script})"
        expected="/home/agent/.claude/skills"
        if [ "$resolved" != "$expected" ]; then
          echo "DRIVER_SKILLS_DIR resolved to '$resolved' under the image's own HOME, expected '$expected'" >&2
          exit 1
        fi
        touch $out
      '';
}
