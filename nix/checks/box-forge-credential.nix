# Issue #6900: BOX_FORGE_CREDENTIAL (#7140's opt-in) is a deprecated no-op,
# since every read-only Box already holds no forge credential. mkHarness warns
# on any explicit value and exposes the message as
# `internals.boxForgeCredentialWarnings`; no pairing with the access mode
# throws any more. Removal is tracked in #6902.
{
  pkgs,
  nixpkgs,
  system,
  ...
}:
let
  inherit (pkgs.lib) assertMsg hasInfix;

  harnessWith =
    defaults:
    import ../../lib/mkHarness.nix {
      inherit nixpkgs system;
      packages = p: [ p.hello ];
      inherit defaults;
    };
  warnings = defaults: (harnessWith defaults).internals.boxForgeCredentialWarnings;
  evals =
    defaults:
    (builtins.tryEval (builtins.seq (harnessWith defaults).spindrift.drvPath "reached")).success;
  goSrc = builtins.readFile ../../cmd/launcher/internal/inputdoc/inputdoc.go;
in
{
  box-forge-credential-unset-is-silent =
    assert assertMsg (warnings { } == [ ]) "an unset BOX_FORGE_CREDENTIAL must not warn";
    pkgs.runCommand "box-forge-credential-unset-is-silent" { } "touch $out";

  box-forge-credential-set-warns =
    let
      forNone = warnings { boxForgeCredential = "none"; };
      forForwarded = warnings { boxForgeCredential = "forwarded"; };
    in
    assert assertMsg (
      builtins.length forNone == 1 && builtins.length forForwarded == 1
    ) "BOX_FORGE_CREDENTIAL=none and =forwarded must each record exactly one warning";
    assert assertMsg (hasInfix "deprecated and has no effect" (
      builtins.head forNone
    )) "the notice must say the knob is a deprecated no-op";
    assert assertMsg (
      hasInfix "#6900" (builtins.head forNone) && hasInfix "#6902" (builtins.head forNone)
    ) "the notice must name issues #6900 and #6902";
    # The Go string literal and builtins.toJSON escape this ASCII text alike
    # (only `"` is escaped), so the Go const is the build notice verbatim.
    assert assertMsg (hasInfix (builtins.toJSON (builtins.head forNone)) goSrc)
      "lib/mkHarness.nix boxForgeCredentialWarnings and BoxForgeCredentialNotice in cmd/launcher/internal/inputdoc/inputdoc.go must carry the same text";
    pkgs.runCommand "box-forge-credential-set-warns" { } "touch $out";

  box-forge-credential-changes-no-pairing =
    assert assertMsg (builtins.all evals (
      builtins.concatMap
        (access: [
          {
            boxForgeAndIssueAccess = access;
            boxForgeCredential = "none";
          }
          {
            boxForgeAndIssueAccess = access;
            boxForgeCredential = "forwarded";
          }
        ])
        [
          "read-only"
          "read-write"
        ]
    )) "mkHarness.nix must evaluate BOX_FORGE_CREDENTIAL under either access mode";
    pkgs.runCommand "box-forge-credential-changes-no-pairing" { } "touch $out";
}
