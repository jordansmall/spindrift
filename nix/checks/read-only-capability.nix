# These checks pin lib/mkHarness.nix's readOnlyCapabilityOk assert (issue
# #2526 slice 2) at eval time: the BOX_FORGE_AND_ISSUE_ACCESS x CODE_FORGE x
# ISSUE_TRACKER matrix against lib/backends/default.nix's relayCapable and
# hostPostingCapable rows. Each case goes through the real mkHarness.nix entry
# point, so it exercises mkHarness's own assert chain and not a bare copy.
# These cases trust the relayCapable bit; whether the bit matches the forge
# adapter's real capabilities is checked Go-side, by cmd/launcher's
# TestRelayCapableMatchesReadOnlyForgeInterfaces (issue #2628).
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
  # git holds relayCapable (issue #6898): the host relays its bundle with the
  # plain git credential, so the eval assert accepts it under read-only. No
  # registered forge is relay-incapable now; the Go gate's rejection path is
  # exercised with a synthetic row in cmd/launcher/readonly_gate_test.go.
  read-only-capability-accepts-git-code-forge =
    let
      ok = builtins.tryEval (
        builtins.seq (mkHarnessWith {
          boxForgeAndIssueAccess = "read-only";
          codeForge = "git";
        }) "reached"
      );
    in
    assert assertMsg ok.success
      "mkHarness.nix must not throw when BOX_FORGE_AND_ISSUE_ACCESS=read-only and CODE_FORGE=git (relay-capable)";
    pkgs.runCommand "read-only-capability-accepts-git-code-forge" { } "touch $out";

  # jira is host-posting capable and git is relay-capable, so the pair passes
  # both axes.
  read-only-capability-accepts-jira-tracker-on-git-forge =
    let
      ok = builtins.tryEval (
        builtins.seq (mkHarnessWith {
          boxForgeAndIssueAccess = "read-only";
          codeForge = "git";
          issueTracker = "jira";
        }) "reached"
      );
    in
    assert assertMsg ok.success
      "mkHarness.nix must not throw when BOX_FORGE_AND_ISSUE_ACCESS=read-only and CODE_FORGE=git with ISSUE_TRACKER=jira";
    pkgs.runCommand "read-only-capability-accepts-jira-tracker-on-git-forge" { } "touch $out";

  # jira holds hostPostingCapable: comments and issue filing post host-side, so
  # it pairs with any relay-capable forge under read-only.
  read-only-capability-accepts-jira-tracker-on-github =
    let
      ok = builtins.tryEval (
        builtins.seq (mkHarnessWith {
          boxForgeAndIssueAccess = "read-only";
          codeForge = "github";
          issueTracker = "jira";
        }) "reached"
      );
    in
    assert assertMsg ok.success
      "mkHarness.nix must not throw when BOX_FORGE_AND_ISSUE_ACCESS=read-only and ISSUE_TRACKER=jira on CODE_FORGE=github";
    pkgs.runCommand "read-only-capability-accepts-jira-tracker-on-github" { } "touch $out";

  read-only-capability-accepts-jira-tracker-on-forgejo =
    let
      ok = builtins.tryEval (
        builtins.seq (mkHarnessWith {
          boxForgeAndIssueAccess = "read-only";
          codeForge = "forgejo";
          issueTracker = "jira";
        }) "reached"
      );
    in
    assert assertMsg ok.success
      "mkHarness.nix must not throw when BOX_FORGE_AND_ISSUE_ACCESS=read-only and ISSUE_TRACKER=jira on CODE_FORGE=forgejo";
    pkgs.runCommand "read-only-capability-accepts-jira-tracker-on-forgejo" { } "touch $out";

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

  # The assert fires only under read-only: outside it, the capability bits are
  # never consulted at all (the git/jira pair was the last rejected pairing).
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
      "mkHarness.nix must not throw for any CODE_FORGE/ISSUE_TRACKER pair when BOX_FORGE_AND_ISSUE_ACCESS is not read-only";
    pkgs.runCommand "read-only-capability-is-a-no-op-under-read-write" { } "touch $out";
}
