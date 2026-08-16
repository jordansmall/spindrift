# Eval-level pins for lib/mkHarness.nix's readOnlyCapabilityOk assert (issue
# #2526 slice 2): the read-only capability matrix (BOX_FORGE_AND_ISSUE_ACCESS
# x CODE_FORGE x ISSUE_TRACKER) checked against lib/backends/default.nix's
# relayCapable/hostPostingCapable registry rows. Same tryEval throw-guard
# idiom as nix/checks/research-verdicts.nix, exercised through the real
# mkHarness.nix entry point since the integration point that matters is
# mkHarness's own assert chain, not a bare expression -- unlike
# nix/checks/prompts.nix's build-time-reject-* checks, which force
# `.spindrift` itself, mkHarnessWith below forces only `.spindrift.drvPath`,
# enough to walk the assert chain without paying for a real build (see
# mkHarnessWith's own comment).
{
  pkgs,
  nixpkgs,
  system,
  ...
}:
let
  inherit (pkgs.lib) assertMsg;

  # Forces the harness's read-only capability assert to evaluate without
  # paying for a real image/package build -- .spindrift.drvPath is enough to
  # walk the assert chain in lib/mkHarness.nix. Deliberately narrower than
  # nix/checks/prompts.nix's build-time-reject-* checks, which force
  # `.spindrift` itself: those checks need the fully realized attrset for
  # other assertions they make; this file only needs the assert chain to
  # run, so .drvPath alone (a string derived without building anything)
  # already suffices and stays cheaper.
  mkHarnessWith =
    defaults:
    (import ../../lib/mkHarness.nix {
      inherit nixpkgs system;
      packages = p: [ p.hello ];
      inherit defaults;
    }).spindrift.drvPath;
in
{
  # read-only + CODE_FORGE=git: git has no relayCapable bit (no PR concept,
  # no host-mediation seam for the bundle-relay hand-off), so the assert must
  # throw (message-content coverage lives in the Go-level table test, not
  # here: builtins.tryEval below only proves a throw happened at all,
  # discarding the thrown message itself -- cmd/launcher's
  # TestReadOnlyCapabilityGate_Table pins the analogous Go-side gate's own
  # message wording by substring, a sibling check on a sibling gate, not a
  # check on this Nix assert's own message).
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

  # read-only + ISSUE_TRACKER=jira: jira has no hostPostingCapable bit (no
  # host-posted comment/issue-filing seam), so the assert must throw
  # (message-content coverage lives in the Go-level table test, not here --
  # same reasoning as the CODE_FORGE=git check above).
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

  # read-only + CODE_FORGE=github + ISSUE_TRACKER=github: both axes are
  # capability-satisfied (github is relayCapable and hostPostingCapable), so
  # the assert must not throw.
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

  # read-only + CODE_FORGE=local + ISSUE_TRACKER=local: both axes are
  # capability-satisfied (local is relayCapable and hostPostingCapable by
  # construction), so the assert must not throw.
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

  # read-only + CODE_FORGE=forgejo + ISSUE_TRACKER=forgejo: both axes are
  # capability-satisfied (forgejo is relayCapable and hostPostingCapable),
  # so the assert must not throw.
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

  # read-write (the default) + CODE_FORGE=git + ISSUE_TRACKER=jira: the
  # assert is read-only-only -- an incapable pair on either axis must never
  # be rejected outside read-only.
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
