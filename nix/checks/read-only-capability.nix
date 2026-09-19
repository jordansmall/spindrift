# These checks pin lib/mkHarness.nix's readOnlyCapabilityOk assert (issue
# #2526 slice 2) at eval time: the BOX_FORGE_AND_ISSUE_ACCESS x CODE_FORGE x
# ISSUE_TRACKER matrix against lib/backends/default.nix's relayCapable and
# hostPostingCapable rows. Each case goes through the real mkHarness.nix entry
# point, so it exercises mkHarness's own assert chain and not a bare copy.
{
  pkgs,
  nixpkgs,
  system,
  ...
}:
let
  inherit (pkgs.lib) assertMsg;

  # .drvPath walks lib/mkHarness.nix's assert chain without paying for a real
  # image build. Forcing `.spindrift` itself, as nix/checks/prompts.nix does,
  # would build more than these checks need.
  mkHarnessWith =
    defaults:
    (import ../../lib/mkHarness.nix {
      inherit nixpkgs system;
      packages = p: [ p.hello ];
      inherit defaults;
    }).spindrift.drvPath;
in
{
  # git has no relayCapable bit (no PR concept, nowhere for the host to
  # mediate the bundle relay), so the assert must throw. tryEval discards the
  # thrown message, so wording is pinned Go-side in cmd/launcher's
  # TestReadOnlyCapabilityGate_Table, not here.
  read-only-capability-rejects-relay-incapable-code-forge =
    let
      broken = builtins.tryEval (
        builtins.seq (mkHarnessWith {
          boxForgeAndIssueAccess = "read-only";
          codeForge = "git";
        }) "unreached"
      );
    in
    assert assertMsg (!broken.success)
      "mkHarness.nix must throw when BOX_FORGE_AND_ISSUE_ACCESS=read-only and CODE_FORGE=git (relay-incapable)";
    pkgs.runCommand "read-only-capability-rejects-relay-incapable-code-forge" { } "touch $out";

  # jira has no hostPostingCapable bit (the host cannot post comments or file
  # issues for it), so the assert must throw. Message wording is pinned
  # Go-side, as for the CODE_FORGE=git check above.
  read-only-capability-rejects-host-posting-incapable-issue-tracker =
    let
      broken = builtins.tryEval (
        builtins.seq (mkHarnessWith {
          boxForgeAndIssueAccess = "read-only";
          issueTracker = "jira";
        }) "unreached"
      );
    in
    assert assertMsg (!broken.success)
      "mkHarness.nix must throw when BOX_FORGE_AND_ISSUE_ACCESS=read-only and ISSUE_TRACKER=jira (host-posting-incapable)";
    pkgs.runCommand "read-only-capability-rejects-host-posting-incapable-issue-tracker" { }
      "touch $out";

  read-only-capability-accepts-capable-github-pair =
    let
      ok = builtins.tryEval (
        builtins.seq (mkHarnessWith {
          boxForgeAndIssueAccess = "read-only";
          codeForge = "github";
          issueTracker = "github";
        }) "reached"
      );
    in
    assert assertMsg ok.success
      "mkHarness.nix must not throw when BOX_FORGE_AND_ISSUE_ACCESS=read-only and both CODE_FORGE/ISSUE_TRACKER are capability-satisfied (github/github)";
    pkgs.runCommand "read-only-capability-accepts-capable-github-pair" { } "touch $out";

  # The local backend holds both capability bits by construction: the host
  # already does the relaying and the posting itself.
  read-only-capability-accepts-capable-local-pair =
    let
      ok = builtins.tryEval (
        builtins.seq (mkHarnessWith {
          boxForgeAndIssueAccess = "read-only";
          codeForge = "local";
          issueTracker = "local";
        }) "reached"
      );
    in
    assert assertMsg ok.success
      "mkHarness.nix must not throw when BOX_FORGE_AND_ISSUE_ACCESS=read-only and both CODE_FORGE/ISSUE_TRACKER are capability-satisfied (local/local)";
    pkgs.runCommand "read-only-capability-accepts-capable-local-pair" { } "touch $out";

  read-only-capability-accepts-capable-forgejo-pair =
    let
      ok = builtins.tryEval (
        builtins.seq (mkHarnessWith {
          boxForgeAndIssueAccess = "read-only";
          codeForge = "forgejo";
          issueTracker = "forgejo";
        }) "reached"
      );
    in
    assert assertMsg ok.success
      "mkHarness.nix must not throw when BOX_FORGE_AND_ISSUE_ACCESS=read-only and both CODE_FORGE/ISSUE_TRACKER are capability-satisfied (forgejo/forgejo)";
    pkgs.runCommand "read-only-capability-accepts-capable-forgejo-pair" { } "touch $out";

  # The assert fires only under read-only: outside it, an incapable pair on
  # either axis must never be rejected.
  read-only-capability-is-a-no-op-under-read-write =
    let
      ok = builtins.tryEval (
        builtins.seq (mkHarnessWith {
          codeForge = "git";
          issueTracker = "jira";
        }) "reached"
      );
    in
    assert assertMsg ok.success
      "mkHarness.nix must not throw for an incapable CODE_FORGE/ISSUE_TRACKER pair when BOX_FORGE_AND_ISSUE_ACCESS is not read-only";
    pkgs.runCommand "read-only-capability-is-a-no-op-under-read-write" { } "touch $out";
}
