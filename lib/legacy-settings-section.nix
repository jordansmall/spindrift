# Frozen snapshot (ADR 0037 Pass 2) of each flakeOption knob's original
# `settings.<section>` attr name, kept until the deprecated aliases go at 1.0.
# Pass 2 re-cut the schema `group` field to domains, so the section name can no
# longer be derived from `group`. The schema-drift check (issue #2522) fails a
# knob that has no row here and is not `legacySettingsExempt` in lib/env-schema.nix.
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
  mergeMethod = "branches";
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
