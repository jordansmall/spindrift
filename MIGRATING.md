# Migration Guide

## `spindrift doctor` exit codes are no longer a flat 0/1 (issue #2569)

`spindrift doctor` used to exit `0` on success and `1` on any failure — the
only two codes it ever produced. It now exits a distinct code per failure
class: `0` healthy (advisory findings — missing research/priority/
ambiguous-spec labels, container runtime not ready, etc. — are allowed and
reported informationally), `1` reserved for internal/unclassified errors,
`2` configuration invalid, `3` auth or connectivity, `4` required checks
failed or declined. See [`spindrift doctor` exit
codes](docs/reference.md#spindrift-doctor-exit-codes-issue-2569) for the full
table.

A script that only distinguishes `$? -eq 0` (success) from anything else is
unaffected. A script that specifically checked `$? -eq 1` to mean "any
failure" must switch to `$? -ne 0` — a real failure can now also exit `2`,
`3`, or `4`.

Two more doctor behaviors changed alongside the exit codes: every
issue-tracker/code-forge connectivity error doctor reports now carries an
`issue tracker or code forge connectivity failure: ` prefix (it names the
failure class being classified as exit `3`), and an advisory-tier label
(research/priority/ambiguous-spec) that fails to create at the interactive
prompt is reported but no longer fails the check — it exits `0`, where it
previously exited `1`. Only a work-tier (triage) label create failure is
still fatal.

## Roster entries fail eval on an unknown key, a missing model, or an unresolvable prompt file (issue #2571)

`roster` entries — both `defaultRoster`'s own built-in four and
hand-authored `perSystem.spindrift.roster` / direct `mkHarness roster`
entries — now go through strict validation in `normalizeRoster`
(`lib/roster.nix`) instead of being accepted as-is. Three previously-silent
mistakes now fail eval:

- An entry carrying a key outside the documented shape (`name`, `model`,
  `effort`, `mode`, `description`, `tools`, `promptFile`, `prompt`) — a
  typo like `dexcription` — now throws instead of silently passing the
  stray key through unused.
- An entry that omits `model` entirely now throws. An explicit `model =
  ""` is unaffected — it's still the supported #392 opt-out, dropped by a
  separate `rosterLib.dropOptedOut` step `lib/mkHarness.nix` applies after
  validation succeeds.
- A `promptFile` that isn't a non-empty string (e.g. a number, `null`, or
  `""`) now throws.
- An entry whose `promptFile` (explicit, or the auto-derived
  `<name>-prompt.md` default) doesn't resolve to a real file under
  `templates/default/prompts/`, and which carries no inline `prompt`, now
  throws instead of silently baking an agent with no usable prompt. The
  auto-derived default is `<name>-prompt.md` for every roster entry except
  `reviewer`, which defaults to `review-prompt.md` (matching that agent's
  on-disk template name, `lib/roster.nix`'s `defaultPromptFileOverrides`).

This promptFile check runs at eval time against the checked-in
`templates/default/prompts/` tree only. A Consumer relying on a *runtime*
prompt-dir override (`SPINDRIFT_PROMPT_DIR` / `--prompt-dir` /
`perSystem.spindrift.agents.promptDir`) to supply a custom agent's prompt
file must add an inline `prompt` to that roster entry (or ship the file in
the checked-in tree) to keep evaluating — the override directory doesn't
exist yet at build time, so the eval-time check can't see into it.

This is a breaking change to the versioned `roster` flake option surface
(see [`VERSIONING.md`](VERSIONING.md)). Most existing rosters are
unaffected: only entries of the exact invalid shapes above — the kind that
used to silently misbehave rather than do anything useful — now fail eval.

A plain default `mkHarness` call (no explicit `byName` overrides) now bakes
3 agents instead of 4: `defaultRoster`'s built-in `filer` entry resolves to
the `filerModel` schema default, `""`, which is the existing #392 opt-out
sentinel, and `rosterLib.dropOptedOut` — which `lib/mkHarness.nix` now runs
unconditionally ahead of every downstream consumer of the resolved roster —
drops it before the image is built. This is not new behavior: `filer` was
always opted out by default, only previously masked by Driver-level
empty-model filters (removed as redundant by this same issue) rather than
by roster-entry count.

## `MAX_BUDGET_TOKENS`/`MAX_BUDGET_USD` now also cap the orchestrator's review loop (issue #2694)

These two knobs previously gated only `selfHealGate`'s host-side decision to
dispatch another fix-pass Box (issue #2001) — the value never left the
launcher process. They are now `boxEnv` (`lib/env-schema.nix`), so
`entrypoint.sh` forwards them into the Box unconditionally as the
orchestrator's own `--max-budget-tokens`/`--max-budget-usd` flags, and the
orchestrator's review-pass loop (`--review-prompt-file` set, the default
under `ORCHESTRATOR_ENABLED`) now applies the same threshold to its own
cumulative spend to commit to a terminal land pass instead of a further
`BLOCK`-triggered review round. This is the same threshold, not the same
running total: the in-Box sum starts fresh at zero each Box invocation
(implement/fix/review passes plus dispatched workers in that Box only),
distinct from `selfHealGate`'s own cross-Box figure that sums every
attempt across the whole issue — a run spanning several fix-pass Boxes
gets a fresh budget in each one.

If you already set `MAX_BUDGET_TOKENS`/`MAX_BUDGET_USD` purely to bound
host-side fix-pass retries, this is a behavior widening, not a breaking
change: the cap now also applies to the in-Box review loop, which can land
a run earlier than before at the same threshold. A malformed or negative
value degrades to `0` (disabled) rather than erroring, matching this pair's
existing host-side tolerance (`atoiNonneg`/`floatNonneg`) — under the
legacy single-loop path (`ORCHESTRATOR_ENABLED` off, or a custom
`SPINDRIFT_PROMPT_DIR` with no review-pass prompt) the value is accepted
but never consulted.

## `agent-trigger` dropped from the versioned label lifecycle contract (issue #2557)

[`VERSIONING.md`](VERSIONING.md)'s "Label lifecycle names" row no longer lists
`agent-trigger` as part of the versioned contract. It's an internal
CI-dispatch-workflow label `agent-dispatch.yml` swaps away the moment it
claims an issue, not a durable state like the four triage names
(`ready-for-agent`, `agent-in-progress`, `agent-complete`, `agent-failed`) —
so it never should have carried a semver guarantee. The label itself is
unaffected and still in active use by `.github/workflows/agent-dispatch.yml`;
only the documented *contract* — the promise that its name won't change
without a version bump — is withdrawn. If you depended on `agent-trigger`'s
name as a stable integration point, it may still change in a future
non-major release.

The same table also now enumerates the previously-undocumented research
(ADR 0022) and priority (ADR 0040) label families as part of the contract.

## Choices-bearing knobs now enforce their valid values (issue #2519)

The seven knobs that declare a `choices` list in `lib/env-schema.nix`
(`mergeMode`, `codeForge`, `issueTracker`, `overlapGate`, `mergeMethod`,
`syncMethod`, `boxForgeAndIssueAccess`) now reject an out-of-list value at
build/eval time instead of accepting it silently, on both entry paths:

- **Flake module.** Each knob's generated Consumer option
  (`perSystem.spindrift.<path>`) now has type `types.enum choices` instead
  of `types.str`. Setting one to a value outside its choices — e.g.
  `git.merge.policy = "Auto"` (valid: `immediate`, `auto`, `manual`) — now
  fails `nix eval`/`nix build` at the option, naming the option path and
  the valid values, instead of evaluating.
- **Direct `mkHarness` callers.** A Consumer or fixture calling `mkHarness
  { defaults = { ... }; }` directly, bypassing the flake module, now gets
  the same rejection: an out-of-choices value, or explicitly passing `null`
  for one of these seven knobs, now throws naming the knob, the bad value,
  and the valid choices, instead of silently rendering into the Launcher
  input document (a `null` used to render as an empty string, e.g.
  `MERGE_METHOD=""`).

If you set one of these seven knobs, either through `perSystem.spindrift.*`
or a direct `mkHarness` call, confirm the value is one of its documented
choices (see `docs/reference.md` or `spindrift --help --all`).

## `mkHarness`'s check-only return keys moved under `internals` (issue #2529)

A direct `mkHarness { ... }` call's return attrset no longer carries its
21 check-only keys (`build`, `run`, `manpage`, `bashCompletion`,
`fishCompletion`, `zshCompletion`, `imagePath`, `promptDir`, `skillsDir`,
`driverExecBin`, `roster`, …) at the top level. They now live under one
named `internals` attrset instead, e.g. `result.driverExecBin` becomes
`result.internals.driverExecBin`. This surface was never part of the
versioned Consumer contract (ADR 0010 scopes that to `image`/`spindrift`/
`packages`/`apps`; see `docs/reference.md`'s "Calling `mkHarness` directly"),
so it carries no semver bump, but any fixture or fork reaching into these
keys directly needs the `internals.` prefix added.

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
| `CODE_FORGE` | `--forge-backend` | `settings.repository.codeForge` |
| `CODE_FORGE_REMOTE_URL` | `--remote-url` | `settings.repository.codeForgeRemoteURL` |
| `COMPLETE_LABEL` | `--complete-label` | `settings.lifecycleLabels.completeLabel` |
| `CONTINUOUS_DISPATCH` | `--continuous-dispatch` | `settings.concurrency.continuousDispatch` |
| `DEV_SHELL_NAME` | `--dev-shell-name` | `settings.sandbox.devShellName` |
| `DEV_SHELL_PROBE_TIMEOUT` | `--dev-shell-probe-timeout` | `settings.sandbox.devShellProbeTimeout` |
| `FAILED_LABEL` | `--failed-label` | `settings.lifecycleLabels.failedLabel` |
| `FILER_MODEL` | `--filer-model` | `settings.models.filerModel` |
| `GIT_USER_EMAIL` | `--user-email` | `settings.repository.gitUserEmail` |
| `GIT_USER_NAME` | `--user-name` | `settings.repository.gitUserName` |
| `HOLD_JITTER_SECS` | `--hold-jitter-secs` | `settings.selfHealing.holdJitterSecs` |
| `IN_PROGRESS_LABEL` | `--in-progress-label` | `settings.lifecycleLabels.inProgressLabel` |
| `ISSUE_NUMBER` | `--issue-number` | — |
| `ISSUE_TRACKER` | `--tracker` | `settings.issueDiscovery.issueTracker` |
| `JIRA_BASE_URL` | `--jira-base-url` | `settings.repository.jiraBaseURL` |
| `JIRA_EMAIL` | `--jira-email` | `settings.repository.jiraEmail` |
| `JIRA_INCLUDE_COMMENTS` | `--jira-include-comments` | `settings.issueDiscovery.jiraIncludeComments` |
| `JIRA_PROJECT_KEY` | `--jira-project-key` | `settings.repository.jiraProjectKey` |
| `JIRA_STATUS_MAPPING` | `--jira-status-mapping` | `settings.lifecycleLabels.jiraStatusMapping` |
| `LABEL` | `--dispatch-label` | `settings.issueDiscovery.label` |
| `LOCAL_ISSUES_DIR` | `--local-dir` | `settings.issueDiscovery.localIssuesDir` |
| `LOCAL_ISSUE_REFERENCE` | `--local-reference` | `settings.issueDiscovery.localIssueReference` |
| `MAX_FIX_ATTEMPTS` | `--max-fix-attempts` | `settings.selfHealing.maxFixAttempts` |
| `MAX_JOBS` | `--max-jobs` | `settings.concurrency.maxJobs` |
| `MAX_PARALLEL` | `--max-parallel` | `settings.concurrency.maxParallel` |
| `MAX_REBASE_ATTEMPTS` | `--max-rebase-attempts` | `settings.selfHealing.maxRebaseAttempts` |
| `MEMORY_LIMIT` | `--memory-limit` | `settings.sandbox.memoryLimit` |
| `MERGE_GUARD_PATHS` | `--merge-guard-paths` | `settings.branches.mergeGuardPaths` |
| `MERGE_MODE` | `--merge-policy` | `settings.branches.mergeMode` |
| `MERGE_POLL_INTERVAL` | `--merge-poll-interval` | `settings.branches.mergePollInterval` |
| `MERGE_POLL_TIMEOUT` | `--merge-poll-timeout` | `settings.branches.mergePollTimeout` |
| `MODEL` | `--model` | `settings.models.model` |
| `OVERLAP_GATE` | `--overlap-gate` | `settings.concurrency.overlapGate` |
| `PIDS_LIMIT` | `--pids-limit` | `settings.sandbox.pidsLimit` |
| `PODMAN_NETWORK` | `--podman-network` | `settings.sandbox.podmanNetwork` |
| `REPO_SLUG` | `--repo-slug` | `settings.repository.repoSlug` |
| `REVIEW_MODEL` | `--review-model` | `settings.models.reviewModel` |
| `SCOUT_MODEL` | `--scout-model` | `settings.models.scoutModel` |
| `SPINDRIFT_PROMPT_DIR` | `--prompt-dir` | — |
| `SPINDRIFT_SKILLS_DIR` | `--skills-dir` | — |
| `TRANSIENT_BACKOFF_SECS` | `--transient-backoff-secs` | `settings.selfHealing.transientBackoffSecs` |
| `TRANSIENT_RETRY_MAX` | `--transient-retry-max` | `settings.selfHealing.transientRetryMax` |

### Flag names re-cut to domains (ADR 0037 Pass 2)

The `Flag` column above shows each knob's **canonical** flag. Several were
renamed to read from their domain leaf, dropping a now-redundant prefix —
`--issue-tracker` → `--tracker`, `--code-forge` → `--forge-backend`,
`--merge-mode` → `--merge-policy`, `--git-user-name` → `--user-name`, and so on.
Every previous flag name **keeps working as a deprecated alias** (it resolves to
the same value; `spindrift --help --all` marks it `(deprecated)`), so no
dispatch script breaks — migrate at your leisure before the aliases are removed
at 1.0. The env-var names are unchanged. The primary flake surface is the domain
tree under `perSystem.spindrift.*` (see `docs/flake-options.md`); the
`settings.<section>.*` paths above remain as deprecated aliases until 1.0.

Full alias-to-domain mapping:

<!-- BEGIN GENERATED LEGACY SETTINGS MAPPING -- nix run .#regen -- DO NOT EDIT -->
| Legacy alias | Canonical replacement |
| --- | --- |
| `perSystem.spindrift.settings.branches.baseBranch` | `perSystem.spindrift.git.baseBranch` |
| `perSystem.spindrift.settings.branches.branchPrefix` | `perSystem.spindrift.git.branchPrefix` |
| `perSystem.spindrift.settings.branches.mergeGuardPaths` | `perSystem.spindrift.git.merge.guardPaths` |
| `perSystem.spindrift.settings.branches.mergeMethod` | `perSystem.spindrift.git.merge.method` |
| `perSystem.spindrift.settings.branches.mergeMode` | `perSystem.spindrift.git.merge.policy` |
| `perSystem.spindrift.settings.branches.mergePollInterval` | `perSystem.spindrift.git.merge.pollInterval` |
| `perSystem.spindrift.settings.branches.mergePollTimeout` | `perSystem.spindrift.git.merge.pollTimeout` |
| `perSystem.spindrift.settings.concurrency.continuousDispatch` | `perSystem.spindrift.dispatch.continuous.enable` |
| `perSystem.spindrift.settings.concurrency.maxJobs` | `perSystem.spindrift.dispatch.maxJobs` |
| `perSystem.spindrift.settings.concurrency.maxParallel` | `perSystem.spindrift.dispatch.maxParallel` |
| `perSystem.spindrift.settings.concurrency.overlapGate` | `perSystem.spindrift.dispatch.overlapGate` |
| `perSystem.spindrift.settings.issueDiscovery.issueTracker` | `perSystem.spindrift.issues.tracker` |
| `perSystem.spindrift.settings.issueDiscovery.jiraIncludeComments` | `perSystem.spindrift.issues.jira.includeComments` |
| `perSystem.spindrift.settings.issueDiscovery.label` | `perSystem.spindrift.issues.labels.dispatch` |
| `perSystem.spindrift.settings.issueDiscovery.localIssueReference` | `perSystem.spindrift.issues.localReference` |
| `perSystem.spindrift.settings.issueDiscovery.localIssuesDir` | `perSystem.spindrift.issues.localDir` |
| `perSystem.spindrift.settings.lifecycleLabels.completeLabel` | `perSystem.spindrift.issues.labels.complete` |
| `perSystem.spindrift.settings.lifecycleLabels.failedLabel` | `perSystem.spindrift.issues.labels.failed` |
| `perSystem.spindrift.settings.lifecycleLabels.inProgressLabel` | `perSystem.spindrift.issues.labels.inProgress` |
| `perSystem.spindrift.settings.lifecycleLabels.jiraStatusMapping` | `perSystem.spindrift.issues.jira.statusMapping` |
| `perSystem.spindrift.settings.models.filerModel` | `perSystem.spindrift.agents.models.filer` |
| `perSystem.spindrift.settings.models.model` | `perSystem.spindrift.agents.models.default` |
| `perSystem.spindrift.settings.models.reviewModel` | `perSystem.spindrift.agents.models.review` |
| `perSystem.spindrift.settings.models.scoutModel` | `perSystem.spindrift.agents.models.scout` |
| `perSystem.spindrift.settings.models.workerModel` | `perSystem.spindrift.agents.models.worker` |
| `perSystem.spindrift.settings.promptSkillIteration.autoFormat` | `perSystem.spindrift.agents.format.enable` |
| `perSystem.spindrift.settings.promptSkillIteration.autoLint` | `perSystem.spindrift.agents.lint.enable` |
| `perSystem.spindrift.settings.promptSkillIteration.orchestratorEnabled` | `perSystem.spindrift.dispatch.orchestrator.enable` |
| `perSystem.spindrift.settings.repository.boxForgeAndIssueAccess` | `perSystem.spindrift.forge.boxAccess` |
| `perSystem.spindrift.settings.repository.codeForge` | `perSystem.spindrift.forge.backend` |
| `perSystem.spindrift.settings.repository.codeForgeAccumulationRepoDir` | `perSystem.spindrift.forge.accumulationRepoDir` |
| `perSystem.spindrift.settings.repository.codeForgeRemoteURL` | `perSystem.spindrift.forge.remoteURL` |
| `perSystem.spindrift.settings.repository.ghTokenRefreshFile` | `perSystem.spindrift.forge.ghTokenRefreshFile` |
| `perSystem.spindrift.settings.repository.gitUserEmail` | `perSystem.spindrift.git.user.email` |
| `perSystem.spindrift.settings.repository.gitUserName` | `perSystem.spindrift.git.user.name` |
| `perSystem.spindrift.settings.repository.jiraBaseURL` | `perSystem.spindrift.issues.jira.baseURL` |
| `perSystem.spindrift.settings.repository.jiraEmail` | `perSystem.spindrift.issues.jira.email` |
| `perSystem.spindrift.settings.repository.jiraProjectKey` | `perSystem.spindrift.issues.jira.projectKey` |
| `perSystem.spindrift.settings.repository.repoSlug` | `perSystem.spindrift.forge.repoSlug` |
| `perSystem.spindrift.settings.sandbox.bwrapUnshareNet` | `perSystem.spindrift.infra.network.bwrapUnshare` |
| `perSystem.spindrift.settings.sandbox.devShellName` | `perSystem.spindrift.infra.devShell.name` |
| `perSystem.spindrift.settings.sandbox.devShellProbeTimeout` | `perSystem.spindrift.infra.devShell.probeTimeout` |
| `perSystem.spindrift.settings.sandbox.memoryLimit` | `perSystem.spindrift.infra.limits.memory` |
| `perSystem.spindrift.settings.sandbox.pidsLimit` | `perSystem.spindrift.infra.limits.pids` |
| `perSystem.spindrift.settings.sandbox.podmanNetwork` | `perSystem.spindrift.infra.network.podman` |
| `perSystem.spindrift.settings.selfHealing.holdJitterSecs` | `perSystem.spindrift.dispatch.retry.holdJitter` |
| `perSystem.spindrift.settings.selfHealing.maxBudgetTokens` | `perSystem.spindrift.dispatch.budget.tokens` |
| `perSystem.spindrift.settings.selfHealing.maxBudgetUSD` | `perSystem.spindrift.dispatch.budget.usd` |
| `perSystem.spindrift.settings.selfHealing.maxFixAttempts` | `perSystem.spindrift.dispatch.retry.maxFix` |
| `perSystem.spindrift.settings.selfHealing.maxRebaseAttempts` | `perSystem.spindrift.dispatch.retry.maxRebase` |
| `perSystem.spindrift.settings.selfHealing.preflightStaleBase` | `perSystem.spindrift.git.merge.preflightStaleBase` |
| `perSystem.spindrift.settings.selfHealing.transientBackoffSecs` | `perSystem.spindrift.dispatch.retry.transientBackoff` |
| `perSystem.spindrift.settings.selfHealing.transientRetryMax` | `perSystem.spindrift.dispatch.retry.transientMax` |
<!-- END GENERATED LEGACY SETTINGS MAPPING -->

## `REVIEW_EFFORT` dispatch-time override removed (issue #2512)

`REVIEW_EFFORT` (`--review-effort` / `perSystem.spindrift.agents.models.reviewEffort`)
no longer takes effect as a live, per-dispatch Box-runtime override —
`REVIEW_EFFORT=xhigh spindrift dispatch ...` (or `--review-effort xhigh`) is
now a silent no-op against an already-built image. The knob now has the same
shape `REVIEW_MODEL` already has: it only sets the roster `reviewer` entry's
effort at nix build time, baked into the image before dispatch, which the
orchestrator's code-owned review pass then reads via the prompt-assembly
Handoff. Set `perSystem.spindrift.agents.models.reviewEffort` in your
Consumer flake (or the roster's `reviewer` entry directly) and rebuild
(`spindrift build`) instead.

Under `DRIVER=opencode` specifically, the review pass's effort now always
falls back to the coordinator's own `--effort`: opencode's `agentsJsonTemplate`
never renders a `reviewer` key, so there is no roster value for the Handoff
to extract — the same fallback `REVIEW_MODEL` already has on that Driver.
`perSystem.spindrift.agents.models.reviewEffort` has no effect under
`DRIVER=opencode` for this reason.

This is also a default-behavior change, not just a removed override: with
`REVIEW_EFFORT` unset, the review pass previously fell back to the
coordinator's own `--effort`; under `DRIVER=claude` it now runs at the
roster reviewer's effort (`rosterDefaults.reviewer.effort`, `"high"` by
default) instead. Every default-roster orchestrator Consumer whose
coordinator runs below `high` sees its review pass cost rise accordingly
unless it pins `perSystem.spindrift.agents.models.reviewEffort` (or the
roster's `reviewer` entry) explicitly.

| Removed capability | Replacement |
|---|---|
| Dispatch-time `REVIEW_EFFORT=...` / `--review-effort ...` override | `perSystem.spindrift.agents.models.reviewEffort` in your Consumer flake under `DRIVER=claude` (image rebuild required); no effect under `DRIVER=opencode` |

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
