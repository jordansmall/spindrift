# Issue #6891: BOX_FORGE_AND_ISSUE_ACCESS defaults to read-write, which flips to
# read-only in a coming minor release (issue #6757). A Consumer that never set
# the knob gets a build warning, carried to the launcher as
# `deprecatedDefaults` in the input document; an explicit value stays silent.
{
  pkgs,
  fixtures,
  nixpkgs,
  system,
  ...
}:
let
  inherit (pkgs.lib) assertMsg hasInfix;
  knob = "BOX_FORGE_AND_ISSUE_ACCESS";
  withAccess =
    access:
    import ../../lib/mkHarness.nix {
      inherit nixpkgs system;
      packages = p: [ p.hello ];
      defaults.boxForgeAndIssueAccess = access;
    };
  explicitReadWrite = withAccess "read-write";
  explicitReadOnly = withAccess "read-only";
  defaulted = fixtures.minimalDirect.internals;
in
{
  box-access-default-warns-and-travels-in-document =
    assert assertMsg (defaulted.deprecatedDefaults ? ${knob})
      "an unset ${knob} must record a deprecatedDefaults entry, got: ${builtins.toJSON defaulted.deprecatedDefaults}";
    assert assertMsg (hasInfix "#6757" defaulted.deprecatedDefaults.${knob})
      "the notice must name the parent issue #6757, got: ${defaulted.deprecatedDefaults.${knob}}";
    assert assertMsg (hasInfix "MIGRATING.md" defaulted.deprecatedDefaults.${knob})
      "the notice must point at MIGRATING.md, got: ${defaulted.deprecatedDefaults.${knob}}";
    pkgs.runCommand "box-access-default-warns-and-travels-in-document"
      { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        jq -e --arg k ${knob} '.deprecatedDefaults[$k] | test("read-only")' \
          ${defaulted.runInputDocumentFile}
        touch $out
      '';

  box-access-explicit-values-are-silent =
    assert assertMsg (
      explicitReadWrite.internals.deprecatedDefaults == { }
    ) "an explicit read-write must not record a deprecation, got: ${builtins.toJSON explicitReadWrite.internals.deprecatedDefaults}";
    assert assertMsg (
      explicitReadOnly.internals.deprecatedDefaults == { }
    ) "an explicit read-only must not record a deprecation, got: ${builtins.toJSON explicitReadOnly.internals.deprecatedDefaults}";
    pkgs.runCommand "box-access-explicit-values-are-silent"
      { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        jq -e 'has("deprecatedDefaults") | not' ${explicitReadWrite.internals.runInputDocumentFile}
        jq -e 'has("deprecatedDefaults") | not' ${explicitReadOnly.internals.runInputDocumentFile}
        touch $out
      '';
}
