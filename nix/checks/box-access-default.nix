# Issue #6899: BOX_FORGE_AND_ISSUE_ACCESS defaults to read-only. An explicit
# read-write is deprecated (issue #6757): mkHarness warns at build time and
# exposes the message as `internals.boxAccessWarnings`. The run input document
# carries no notice; the launcher prints its own for the effective value.
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
  msgs = i: builtins.concatStringsSep "\n" i.boxAccessWarnings;

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
  box-access-default-is-read-only-and-silent =
    assert assertMsg (
      defaulted.boxAccessWarnings == [ ]
    ) "an unset ${knob} must not warn, got: ${msgs defaulted}";
    assert assertMsg (
      explicitReadOnly.internals.boxAccessWarnings == [ ]
    ) "an explicit read-only must not warn, got: ${msgs explicitReadOnly.internals}";
    pkgs.runCommand "box-access-default-is-read-only-and-silent" { nativeBuildInputs = [ pkgs.jq ]; } ''
      jq -e --arg k ${knob} '.settings[$k] == "read-only"' ${defaulted.runInputDocumentFile}
      touch $out
    '';

  box-access-unset-is-read-only-on-every-forge =
    let
      names = builtins.attrNames unsetPairs;
    in
    assert assertMsg (builtins.all (
      n: unsetInternals.${n}.boxAccessWarnings == [ ]
    ) names) "an unset ${knob} must not warn on any of: ${builtins.concatStringsSep ", " names}";
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

  box-access-explicit-read-write-warns =
    let
      msg = msgs explicitReadWrite.internals;
      goSrc = builtins.readFile ../../cmd/launcher/internal/inputdoc/inputdoc.go;
    in
    assert assertMsg (
      builtins.length explicitReadWrite.internals.boxAccessWarnings == 1
    ) "an explicit read-write must record exactly one warning, got: ${msg}";
    assert assertMsg (hasInfix "#6757" msg) "the notice must name the parent issue #6757, got: ${msg}";
    assert assertMsg (hasInfix "#6899" msg) "the notice must name issue #6899, got: ${msg}";
    assert assertMsg (hasInfix "MIGRATING.md" msg) "the notice must point at MIGRATING.md, got: ${msg}";
    # The Go string literal and builtins.toJSON escape this ASCII text alike
    # (only `"` is escaped), so the Go const is the build notice verbatim.
    assert assertMsg
      (hasInfix (builtins.toJSON (builtins.head explicitReadWrite.internals.boxAccessWarnings)) goSrc)
      "lib/mkHarness.nix boxAccessWarnings and BoxAccessReadWriteNotice in cmd/launcher/internal/inputdoc/inputdoc.go must carry the same text";
    pkgs.runCommand "box-access-explicit-read-write-warns" { } "touch $out";

  # nix/dogfood-defaults.nix pins read-only; fixtures.harness mirrors the
  # dogfood flake's mkHarness call (pinned in connector-driver-rows.nix).
  box-access-dogfood-is-silent =
    assert assertMsg (
      fixtures.harness.internals.boxAccessWarnings == [ ]
    ) "spindrift's own flake must not warn on ${knob}, got: ${msgs fixtures.harness.internals}";
    pkgs.runCommand "box-access-dogfood-is-silent" { } "touch $out";

  # Only the build-time warning is out of reach from the flake path; this
  # proves forge.boxAccess lands in the document the wrapper runs with.
  box-access-flake-option-reaches-document =
    let
      unset = flakeDocument { };
      readWrite = flakeDocument { forge.boxAccess = "read-write"; };
    in
    pkgs.runCommand "box-access-flake-option-reaches-document" { nativeBuildInputs = [ pkgs.jq ]; } ''
      jq -e --arg k ${knob} '.settings[$k] == "read-only"' "$(cat ${unset})"
      jq -e --arg k ${knob} '.settings[$k] == "read-write"' "$(cat ${readWrite})"
      touch $out
    '';
}
