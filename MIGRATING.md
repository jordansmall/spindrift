# Migration Guide

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
