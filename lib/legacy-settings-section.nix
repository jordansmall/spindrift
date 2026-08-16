# Frozen snapshot (ADR 0037 Pass 2): each flakeOption knob's original
# ADR-0015-era `settings.<section>` attr name. Pass 2 re-cut the schema
# `group` field to domains, so the legacy section name can no longer be
# derived from `group`; this frozen map preserves the exact deprecated
# `perSystem.spindrift.settings.<section>.<knob>` alias spelling until the
# whole legacy surface is removed at 1.0. Consumed by both
# lib/flakeModule.nix (to build the deprecation-shim options) and
# nix/checks/schema-drift.nix's legacy-settings-section-coverage check
# (issue #2522), which asserts every flakeOption knob either has a row here
# or is `legacySettingsExempt = true;` in lib/env-schema.nix (a knob added
# after this freeze, which never had an old alias to preserve).
{
  autoFormat = "promptSkillIteration";
  autoLint = "promptSkillIteration";
  baseBranch = "branches";
  boxForgeAndIssueAccess = "repository";
  branchPrefix = "branches";
  bwrapUnshareNet = "sandbox";
  codeForge = "repository";
  codeForgeAccumulationRepoDir = "repository";
  codeForgeRemoteURL = "repository";
  completeLabel = "lifecycleLabels";
  continuousDispatch = "concurrency";
  devShellName = "sandbox";
  devShellProbeTimeout = "sandbox";
  failedLabel = "lifecycleLabels";
  filerModel = "models";
  ghTokenRefreshFile = "repository";
  gitUserEmail = "repository";
  gitUserName = "repository";
  holdJitterSecs = "selfHealing";
  inProgressLabel = "lifecycleLabels";
  issueTracker = "issueDiscovery";
  jiraBaseURL = "repository";
  jiraEmail = "repository";
  jiraIncludeComments = "issueDiscovery";
  jiraProjectKey = "repository";
  jiraStatusMapping = "lifecycleLabels";
  label = "issueDiscovery";
  localIssueReference = "issueDiscovery";
  localIssuesDir = "issueDiscovery";
  maxBudgetTokens = "selfHealing";
  maxBudgetUSD = "selfHealing";
  maxFixAttempts = "selfHealing";
  maxJobs = "concurrency";
  maxParallel = "concurrency";
  maxRebaseAttempts = "selfHealing";
  memoryLimit = "sandbox";
  mergeGuardPaths = "branches";
  mergeMode = "branches";
  mergePollInterval = "branches";
  mergePollTimeout = "branches";
  model = "models";
  orchestratorEnabled = "promptSkillIteration";
  overlapGate = "concurrency";
  pidsLimit = "sandbox";
  podmanNetwork = "sandbox";
  preflightStaleBase = "selfHealing";
  repoSlug = "repository";
  reviewModel = "models";
  scoutModel = "models";
  transientBackoffSecs = "selfHealing";
  transientRetryMax = "selfHealing";
  workerModel = "models";
}
