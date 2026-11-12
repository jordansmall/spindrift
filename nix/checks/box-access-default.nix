# Issue #6891: BOX_FORGE_AND_ISSUE_ACCESS defaults to read-write, which flips to
# read-only in a coming minor release (issue #6757). A Consumer that never set
# the knob gets a build warning, carried to the launcher as
# `deprecatedDefaults` in the input document; an explicit value stays silent.
{
  pkgs,
  fixtures,
  nixpkgs,
  system,
  config,
  flake-parts,
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

  # The flake outputs expose no `internals`, so read the run input document
  # the published `spindrift` wrapper points at.
  consumerWith =
    spindrift:
    (flake-parts.lib.mkFlake
      {
        inputs = {
          inherit nixpkgs;
          self.outPath = ../../.;
        };
      }
      {
        systems = [ system ];
        imports = [ ../../lib/flakeModule.nix ];
        perSystem.spindrift = {
          infra.image.packages = p: [ p.hello ];
        }
        // spindrift;
      }
    ).packages.${system}.spindrift;
  silentCheck =
    name: spindriftPkg:
    pkgs.runCommand name
      {
        inherit spindriftPkg;
        nativeBuildInputs = [ pkgs.jq ];
      }
      ''
        doc=$(grep -o '/nix/store/[^ ]*launcher-run-input.json' "$spindriftPkg/bin/spindrift")
        jq -e 'has("deprecatedDefaults") | not' "$doc"
        touch $out
      '';
in
{
  box-access-default-warns-and-travels-in-document =
    assert assertMsg (defaulted.deprecatedDefaults ? ${knob})
      "an unset ${knob} must record a deprecatedDefaults entry, got: ${builtins.toJSON defaulted.deprecatedDefaults}";
    assert assertMsg (hasInfix "#6757"
      defaulted.deprecatedDefaults.${knob}
    ) "the notice must name the parent issue #6757, got: ${defaulted.deprecatedDefaults.${knob}}";
    assert assertMsg (hasInfix "MIGRATING.md"
      defaulted.deprecatedDefaults.${knob}
    ) "the notice must point at MIGRATING.md, got: ${defaulted.deprecatedDefaults.${knob}}";
    pkgs.runCommand "box-access-default-warns-and-travels-in-document"
      { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        jq -e --arg k ${knob} '.deprecatedDefaults[$k] | test("read-only")' \
          ${defaulted.runInputDocumentFile}
        touch $out
      '';

  box-access-explicit-values-are-silent =
    assert assertMsg (explicitReadWrite.internals.deprecatedDefaults == { })
      "an explicit read-write must not record a deprecation, got: ${builtins.toJSON explicitReadWrite.internals.deprecatedDefaults}";
    assert assertMsg (explicitReadOnly.internals.deprecatedDefaults == { })
      "an explicit read-only must not record a deprecation, got: ${builtins.toJSON explicitReadOnly.internals.deprecatedDefaults}";
    pkgs.runCommand "box-access-explicit-values-are-silent" { nativeBuildInputs = [ pkgs.jq ]; } ''
      jq -e 'has("deprecatedDefaults") | not' ${explicitReadWrite.internals.runInputDocumentFile}
      jq -e 'has("deprecatedDefaults") | not' ${explicitReadOnly.internals.runInputDocumentFile}
      touch $out
    '';

  # nix/dogfood-defaults.nix pins read-only, so spindrift's own flake never
  # prints the notice it ships.
  box-access-dogfood-is-silent = silentCheck "box-access-dogfood-is-silent" config.packages.spindrift;

  box-access-flake-option-is-silent = silentCheck "box-access-flake-option-is-silent" (consumerWith {
    forge.boxAccess = "read-write";
  });
}
