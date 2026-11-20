# BOX_FORGE_AND_ISSUE_ACCESS is single-valued read-only (issue #6901). The
# read-write value was removed: an explicit one must fail the build with the
# migration message, and read-only (explicit or the default) stays silent.
{
  pkgs,
  fixtures,
  nixpkgs,
  system,
  flake-parts,
  ...
}:
let
  inherit (pkgs.lib) assertMsg hasInfix;
  knob = "BOX_FORGE_AND_ISSUE_ACCESS";
  schema = import ../../lib/env-schema.nix;
  removedChoiceError = import ../../lib/removed-choice-error.nix { lib = pkgs.lib; };
  mkHarnessWith =
    access:
    import ../../lib/mkHarness.nix {
      inherit nixpkgs system;
      packages = p: [ p.hello ];
      defaults.boxForgeAndIssueAccess = access;
    };
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

  # Every forge, with the knob unset and a tracker its read-only capability
  # gate accepts (issue #6899).
  unsetPairs = {
    github = {
      codeForge = "github";
      issueTracker = "github";
    };
    forgejo = {
      codeForge = "forgejo";
      issueTracker = "forgejo";
    };
    local = {
      codeForge = "local";
      issueTracker = "local";
    };
    git = {
      codeForge = "git";
      issueTracker = "github";
    };
  };
  unsetInternals = builtins.mapAttrs (
    _: defaults:
    (import ../../lib/mkHarness.nix {
      inherit nixpkgs system defaults;
      packages = p: [ p.hello ];
    }).internals
  ) unsetPairs;

  # The run input document the published wrapper points at, as a store path.
  flakeDocument =
    spindrift:
    pkgs.runCommand "box-access-flake-option-document"
      {
        spindriftPkg = consumerWith spindrift;
      }
      ''
        grep -o '/nix/store/[^ ]*launcher-run-input.json' "$spindriftPkg/bin/spindrift" > $out
      '';
in
{
  box-access-default-is-read-only =
    assert assertMsg (
      (mkHarnessWith "read-only") ? spindrift
    ) "an explicit read-only must build, since it is the one valid value";
    pkgs.runCommand "box-access-default-is-read-only" { nativeBuildInputs = [ pkgs.jq ]; } ''
      jq -e --arg k ${knob} '.settings[$k] == "read-only"' ${defaulted.runInputDocumentFile}
      touch $out
    '';

  box-access-unset-is-read-only-on-every-forge =
    let
      names = builtins.attrNames unsetPairs;
    in
    pkgs.runCommand "box-access-unset-is-read-only-on-every-forge" { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        ${builtins.concatStringsSep "\n" (
          map (n: ''
            jq -e --arg k ${knob} --arg f ${unsetPairs.${n}.codeForge} \
              '.settings[$k] == "read-only" and .settings.CODE_FORGE == $f' \
              ${unsetInternals.${n}.runInputDocumentFile}
          '') names
        )}
        touch $out
      '';

  # The migration message, compared by value: tryEval cannot tell it from the
  # generic choices error, which fails the same build.
  box-access-read-write-rejected =
    let
      err = removedChoiceError schema { boxForgeAndIssueAccess = "read-write"; };
      rejected = builtins.tryEval (mkHarnessWith "read-write").spindrift;
      viaFlake = builtins.tryEval (consumerWith {
        forge.boxAccess = "read-write";
      });
      enumValues = builtins.attrNames (schema.boxForgeAndIssueAccess.removedChoices);
    in
    assert assertMsg (err != null) "a removed read-write value must produce an error";
    assert assertMsg (hasInfix "${knob}=read-write was removed" err)
      "the error must name the knob, got: ${err}";
    assert assertMsg (hasInfix "#6901" err) "the error must name issue #6901, got: ${err}";
    assert assertMsg (hasInfix "host-mediated" err)
      "the error must say every Box is host-mediated, got: ${err}";
    assert assertMsg (hasInfix "MIGRATING.md" err) "the error must point at MIGRATING.md, got: ${err}";
    assert assertMsg (
      removedChoiceError schema { boxForgeAndIssueAccess = "read-only"; } == null
    ) "read-only must raise no removed-choice error";
    assert assertMsg (
      removedChoiceError schema { } == null
    ) "an unset knob must raise no removed-choice error";
    assert assertMsg (enumValues == [ "read-write" ]) "read-write must be the only removed value";
    assert assertMsg (
      !rejected.success
    ) "mkHarness must reject defaults.boxForgeAndIssueAccess = \"read-write\"";
    assert assertMsg (
      !viaFlake.success
    ) "the flake module must reject forge.boxAccess = \"read-write\"";
    pkgs.runCommand "box-access-read-write-rejected" { } "touch $out";

  # The flake option reaches the document, and read-write never gets that far.
  box-access-flake-option-reaches-document =
    let
      unset = flakeDocument { };
      explicit = flakeDocument { forge.boxAccess = "read-only"; };
    in
    pkgs.runCommand "box-access-flake-option-reaches-document" { nativeBuildInputs = [ pkgs.jq ]; } ''
      jq -e --arg k ${knob} '.settings[$k] == "read-only"' "$(cat ${unset})"
      jq -e --arg k ${knob} '.settings[$k] == "read-only"' "$(cat ${explicit})"
      touch $out
    '';
}
