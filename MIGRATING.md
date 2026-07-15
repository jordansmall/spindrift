# Migration Guide

## Knob env overrides deprecated; use `--flag` or `settings.*` (ADR 0020)

`spindrift` now hands every nix-computed value to the launcher through one
**Launcher input document** (the resolved `settings.*` values plus build/run
artifacts), passed via a single `--input` flag. Precedence is **CLI flag >
flake `settings.<section>.<knob>` > baked default**. An ambient knob env var
(a forgotten shell export, a sourced `harness.env`, CI env) still wins over
the flake setting **this release** — the launcher warns, naming the variable
and both migration targets — but a future MINOR release makes it an error.
Secrets (`GH_TOKEN`, `CLAUDE_CODE_OAUTH_TOKEN`, `ANTHROPIC_API_KEY`,
`JIRA_TOKEN`) and internal launcher→Box plumbing are unaffected — env keeps
those two jobs unchanged. `harness.env.example` now lists secrets only; move
any other knob you kept there to `settings.*` in your `flake.nix`, or pass it
as a flag per invocation.

`dogfood.sh` forwards its `MAX_JOBS`/`CONTINUOUS_DISPATCH` shell variables as
`--max-jobs`/`--continuous-dispatch` flags instead of exporting them —
`KNOB=x ./dogfood.sh` for any other knob is no longer a supported override
idiom; set it in `flake.nix` `settings` instead.

| Env var (deprecated as a knob channel) | Flag | Flake setting |
|---|---|---|
| `AUTO_FORMAT` | `--auto-format` | `settings.promptSkillIteration.autoFormat` |
| `AUTO_LINT` | `--auto-lint` | `settings.promptSkillIteration.autoLint` |
| `BASE_BRANCH` | `--base-branch` | `settings.branches.baseBranch` |
| `BRANCH_PREFIX` | `--branch-prefix` | `settings.branches.branchPrefix` |
| `BWRAP_UNSHARE_NET` | `--bwrap-unshare-net` | `settings.sandbox.bwrapUnshareNet` |
| `CODE_FORGE` | `--code-forge` | `settings.repository.codeForge` |
| `CODE_FORGE_REMOTE_URL` | `--code-forge-remote-url` | `settings.repository.codeForgeRemoteURL` |
| `COMPLETE_LABEL` | `--complete-label` | `settings.lifecycleLabels.completeLabel` |
| `CONTINUOUS_DISPATCH` | `--continuous-dispatch` | `settings.concurrency.continuousDispatch` |
| `DEV_SHELL_NAME` | `--dev-shell-name` | `settings.sandbox.devShellName` |
| `DEV_SHELL_PROBE_TIMEOUT` | `--dev-shell-probe-timeout` | `settings.sandbox.devShellProbeTimeout` |
| `FAILED_LABEL` | `--failed-label` | `settings.lifecycleLabels.failedLabel` |
| `FILER_MODEL` | `--filer-model` | `settings.models.filerModel` |
| `GIT_USER_EMAIL` | `--git-user-email` | `settings.repository.gitUserEmail` |
| `GIT_USER_NAME` | `--git-user-name` | `settings.repository.gitUserName` |
| `HOLD_JITTER_SECS` | `--hold-jitter-secs` | `settings.selfHealing.holdJitterSecs` |
| `IN_PROGRESS_LABEL` | `--in-progress-label` | `settings.lifecycleLabels.inProgressLabel` |
| `ISSUE_NUMBER` | `--issue-number` | — |
| `ISSUE_TRACKER` | `--issue-tracker` | `settings.issueDiscovery.issueTracker` |
| `JIRA_BASE_URL` | `--jira-base-url` | `settings.repository.jiraBaseURL` |
| `JIRA_EMAIL` | `--jira-email` | `settings.repository.jiraEmail` |
| `JIRA_INCLUDE_COMMENTS` | `--jira-include-comments` | `settings.issueDiscovery.jiraIncludeComments` |
| `JIRA_PROJECT_KEY` | `--jira-project-key` | `settings.repository.jiraProjectKey` |
| `JIRA_STATUS_MAPPING` | `--jira-status-mapping` | `settings.lifecycleLabels.jiraStatusMapping` |
| `LABEL` | `--label` | `settings.issueDiscovery.label` |
| `LOCAL_ISSUES_DIR` | `--local-issues-dir` | `settings.issueDiscovery.localIssuesDir` |
| `MAX_FIX_ATTEMPTS` | `--max-fix-attempts` | `settings.selfHealing.maxFixAttempts` |
| `MAX_JOBS` | `--max-jobs` | `settings.concurrency.maxJobs` |
| `MAX_PARALLEL` | `--max-parallel` | `settings.concurrency.maxParallel` |
| `MAX_REBASE_ATTEMPTS` | `--max-rebase-attempts` | `settings.selfHealing.maxRebaseAttempts` |
| `MEMORY_LIMIT` | `--memory-limit` | `settings.sandbox.memoryLimit` |
| `MERGE_GUARD_PATHS` | `--merge-guard-paths` | `settings.branches.mergeGuardPaths` |
| `MERGE_MODE` | `--merge-mode` | `settings.branches.mergeMode` |
| `MERGE_POLL_INTERVAL` | `--merge-poll-interval` | `settings.branches.mergePollInterval` |
| `MERGE_POLL_TIMEOUT` | `--merge-poll-timeout` | `settings.branches.mergePollTimeout` |
| `MODEL` | `--model` | `settings.models.model` |
| `OVERLAP_GATE` | `--overlap-gate` | `settings.concurrency.overlapGate` |
| `PIDS_LIMIT` | `--pids-limit` | `settings.sandbox.pidsLimit` |
| `PODMAN_NETWORK` | `--podman-network` | `settings.sandbox.podmanNetwork` |
| `REPO_SLUG` | `--repo-slug` | `settings.repository.repoSlug` |
| `REVIEW_MODEL` | `--review-model` | `settings.models.reviewModel` |
| `SCOUT_MODEL` | `--scout-model` | `settings.models.scoutModel` |
| `SPINDRIFT_PROMPT_DIR` | `--spindrift-prompt-dir` | — |
| `SPINDRIFT_SKILLS_DIR` | `--spindrift-skills-dir` | — |
| `TRANSIENT_BACKOFF_SECS` | `--transient-backoff-secs` | `settings.selfHealing.transientBackoffSecs` |
| `TRANSIENT_RETRY_MAX` | `--transient-retry-max` | `settings.selfHealing.transientRetryMax` |

## `nix run .#run` / `nix run .#build` (removed in v0.5.0)

spindrift 0.1.1 introduced a unified CLI. The `nix run .#run` and `nix run
.#build` app idioms were deprecated at that point and were removed in
**v0.5.0** (issue #613) — a Consumer invoking either now gets an
unknown-flake-output error, not a forwarding alias.

### Quick-start with the new CLI

```sh
# Enter the dev shell (spindrift goes on PATH automatically)
nix develop      # or: direnv allow  (if your repo has .envrc)

# Dispatch all ready-for-agent issues
spindrift dispatch

# Dispatch a single issue
spindrift dispatch 123

# Build / realize the agent image without running any agent
spindrift build

# Dispatch without auto-building (fail fast if image absent)
spindrift dispatch --no-build

# Show all flags and subcommands
spindrift --help

# Print the installed version
spindrift --version
```

### Old → new mapping

| Old command              | New command              | Notes                               |
|--------------------------|--------------------------|-------------------------------------|
| `nix run .#run`          | `spindrift dispatch`     | Removed alias (v0.5.0)              |
| `nix run .`              | `spindrift dispatch`     | `apps.default` now points to CLI    |
| `nix run .#build`        | `spindrift build`        | Removed alias (v0.5.0)              |
| `ISSUE_NUMBER=42 nix run .#run` | `spindrift dispatch 42` | Positional arg replaces env var |

### Template quick-start

Consumer flakes generated from `nix flake init -t github:jordansmall/spindrift`
now include a `.envrc` (`use flake`) and a `devShells.default` that puts
`spindrift` on PATH via `nix develop` or direnv. See `flake.nix` in the
template for the full pattern.

### Why the change?

ADR-0010 established a single `spindrift` CLI as the primary surface for the
harness. The old `nix run .#run` / `.#build` split was a build artefact, not a
user-facing design. The unified CLI is easier to discover, script, and extend.

## `DEPS_POLL_SECS` / `DEPS_WAIT_SECS` (removed)

`DEPS_POLL_SECS`/`DEPS_WAIT_SECS` (`settings.concurrency.depsPollSecs`/
`depsWaitSecs`) configured the in-process dependency-wave poll loop. That
loop was deleted (ADR 0019): every dispatch invocation now runs at most one
wave and exits, so the poll/wait knobs configure nothing. Setting either in
`settings.concurrency` now fails at flake-eval time with an unknown-key
error naming the valid keys.

`MAX_JOBS` still caps the wave size (`0` means uncapped); re-invoking
`dispatch` (directly, via a driving loop, or via `dogfood.sh`) is how a
dependency graph drains wave by wave.

| Removed knob     | Replacement                             |
|------------------|------------------------------------------|
| `DEPS_POLL_SECS` | none — remove it from your settings/env |
| `DEPS_WAIT_SECS` | none — remove it from your settings/env |

## `spindrift engage` (removed in v0.2.0)

`spindrift engage <issue>` was removed in **v0.2.0**. Use
`spindrift recover <issue>` instead — it performs the same merge-gate/adopt
operation.

| Removed command              | Replacement command           |
|------------------------------|-------------------------------|
| `spindrift engage <issue>`   | `spindrift recover <issue>`   |
