# Frozen ground truth (issue #2522 review finding): every flakeOption = true
# knob name that existed in lib/env-schema.nix at the ADR 0037 Pass 2 freeze
# commit, i.e. `git show b2602019~1:lib/env-schema.nix` filtered for
# `flakeOption = true`. Sourced from git history exactly once and must never
# be edited again -- same "frozen snapshot" spirit as
# lib/legacy-settings-section.nix's own header comment. Consumed by
# nix/checks/schema-drift.nix as the independent cross-check for
# legacySettingsExempt: a knob in this list unconditionally predates the
# freeze and therefore had a real old `settings.<section>` alias, so it can
# never legitimately be legacySettingsExempt -- a real bug (mergeMethod
# wrongly marked exempt despite predating the freeze) slipped past the
# coverage assert before this list existed, because the assert trusted
# legacySettingsExempt at face value with nothing to catch the flag itself
# being wrong. Factored into its own file (mirroring
# lib/legacy-settings-section.nix and lib/structural-paths.nix) rather than
# hand-copied inline in the check, the same reason those two were factored
# out.
[
  "autoFormat"
  "autoLint"
  "baseBranch"
  "boxForgeAndIssueAccess"
  "branchPrefix"
  "bwrapUnshareNet"
  "codeForge"
  "codeForgeAccumulationRepoDir"
  "codeForgeRemoteURL"
  "completeLabel"
  "continuousDispatch"
  "devShellName"
  "devShellProbeTimeout"
  "failedLabel"
  "filerModel"
  "ghTokenRefreshFile"
  "gitUserEmail"
  "gitUserName"
  "holdJitterSecs"
  "inProgressLabel"
  "issueTracker"
  "jiraBaseURL"
  "jiraEmail"
  "jiraIncludeComments"
  "jiraProjectKey"
  "jiraStatusMapping"
  "label"
  "localIssueReference"
  "localIssuesDir"
  "maxBudgetTokens"
  "maxBudgetUSD"
  "maxFixAttempts"
  "maxJobs"
  "maxParallel"
  "maxRebaseAttempts"
  "memoryLimit"
  "mergeGuardPaths"
  "mergeMethod"
  "mergeMode"
  "mergePollInterval"
  "mergePollTimeout"
  "model"
  "orchestratorEnabled"
  "overlapGate"
  "pidsLimit"
  "podmanNetwork"
  "preflightStaleBase"
  "repoSlug"
  "reviewModel"
  "scoutModel"
  "transientBackoffSecs"
  "transientRetryMax"
  "workerModel"
]
