# The local backend row: data plus the env knobs it owns (issues #5633,
# #5635). The registry (./default.nix) documents the fields and fixes this
# row's order; lib/row-knobs.nix folds `knobs` into lib/env-schema.nix.
{
  name = "local";
  goVar = "Local";
  displayName = "local";
  validAsTracker = true;
  validAsCodeForge = true;
  # ADR 0033: local has no writable remote to push to.
  hostMediatedRemote = true;
  # ADR 0032: local is not reachable from inside the Box at all.
  inBoxUnreachableTracker = true;
  relayCapable = true;
  hostPostingCapable = true;
  trackerAxisRead = "LOCAL";
  # Explicitly empty, not omitted: local has no write step, so
  # gates_tracker.go's ISSUE_TRACKER_*_READWRITE/READONLY gates match neither
  # the "GITHUB" nor the "FORGEJO" arm.
  trackerAxisWrite = "";
  knobs = {
    localIssuesDir = {
      env = "LOCAL_ISSUES_DIR";
      group = "issues";
      flag = "local-dir";
      default = ".spindrift/issues";
      doc = "directory scanned for issue files when ISSUE_TRACKER=local; keep it git-ignored so breakout issues stay private";
      flakeOption = true;
      nixSubPath = "localDir";
      boxEnv = false;
    };
    localIssueReference = {
      env = "LOCAL_ISSUE_REFERENCE";
      group = "issues";
      flag = "local-reference";
      default = false;
      kind = "bool";
      doc = "when enabled and ISSUE_TRACKER=local, the PR body includes a non-auto-closing `Local-issue: <slug>` breadcrumb; default off keeps the private local ticket slug out of the PR body entirely (ISSUE_TRACKER=github is unaffected -- `Closes #ISSUE_NUMBER` stays either way)";
      flakeOption = true;
      nixSubPath = "localReference";
      boxEnv = true;
      boxEnvOnly = true;
    };
    codeForgeAccumulationRepoDir = {
      env = "CODE_FORGE_ACCUMULATION_REPO_DIR";
      group = "forge";
      flag = "accumulation-repo-dir";
      doc = "host path to the bare Accumulation repo (ADR 0033), mounted read-only into the Box and landed into host-side; when CODE_FORGE=local, defaults to .spindrift/accum.git under the launcher's working directory (auto-created and seeded) and an explicit value still overrides it; unused otherwise";
      flakeOption = true;
      hostDerived = true;
      nixSubPath = "accumulationRepoDir";
      boxEnv = false;
    };
  };
}
