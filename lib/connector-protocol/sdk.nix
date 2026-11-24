# How the SDK spells the Connector protocol's values in Go (issue #7505). It holds
# no protocol value: every key names a value in the description (default.nix), and
# render.nix's renderSDK throws on one that is not there. A name not listed here is
# derived, and unexported: `cap<Suffix>` for a capability, `method<Suffix>` for a
# method, and for the other rows the description's own name in Go case. Only what
# a Connector imports is listed, because the SDK's exported names must not change.
{
  # Limits the SDK publishes.
  connector.limits = [ "maxHTTPResponse" ];

  forge.caps = {
    "forge.branches" = "CapBranches";
    "forge.pushOnly" = "CapPushOnly";
    "forge.prs" = "CapPRs";
    "forge.autoMerge" = "CapAutoMerge";
    "forge.ciRun" = "CapCIRun";
    "forge.branchProtection" = "CapBranchProtection";
    "forge.probe" = "CapProbe";
    "forge.draftPRs" = "CapDraftPRs";
    "forge.tokenGrant" = "CapTokenGrant";
  };

  tracker = {
    caps = {
      "tracker.transition" = "CapTransition";
      "tracker.editLabels" = "CapEditLabels";
      "tracker.labels" = "CapLabels";
      "tracker.comment" = "CapComment";
      "tracker.fileIssue" = "CapFileIssue";
      "tracker.closeIssue" = "CapCloseIssue";
    };
    # The methods behind tracker.labels; they ride on it rather than being
    # advertised themselves.
    methods = {
      "tracker.listLabels" = "CapListLabels";
      "tracker.createLabel" = "CapCreateLabel";
    };
  };

  # The roles are spelled <Role>Role unless listed.
  driver.roles.subagent = "DefaultRole";
}
