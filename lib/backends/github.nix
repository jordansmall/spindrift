# The github backend row: pure data only (issue #5633). The registry
# (./default.nix) documents the fields and fixes this row's order.
{
  name = "github";
  goVar = "GitHub";
  displayName = "GitHub";
  validAsTracker = true;
  validAsCodeForge = true;
  # A bare `Closes #N` in a PR closes this tracker's own issue #N (ADR 0075).
  bareRefClosesIssues = true;
  connectorOnly = true;
  tokenEnvVar = "GH_TOKEN";
  outboxRelayCapable = true;
  relayCapable = true;
  hostPostingCapable = true;
  labelRegistry = true;
  issuesWearLabels = true;
  goTestRepoFiles = [ ".github/workflows/agent-dispatch.yml" ];
  goTestBuildsOn = "git";
  quickstart = {
    hosts = [ "github.com" ];
    axes = [
      "tracker"
      "forge"
    ];
  };
}
