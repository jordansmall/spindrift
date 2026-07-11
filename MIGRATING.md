# Migration Guide

## `nix run .#run` / `nix run .#build` → `spindrift` CLI

spindrift 0.1.1 introduces a unified CLI. The `nix run .#run` and `nix run
.#build` app idioms are deprecated and will be removed in **v0.2.0**.

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
| `nix run .#run`          | `spindrift dispatch`     | Deprecated alias prints a notice    |
| `nix run .`              | `spindrift dispatch`     | `apps.default` now points to CLI    |
| `nix run .#build`        | `spindrift build`        | Deprecated alias prints a notice    |
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

| Removed knob      | Replacement                                      |
|--------------------|--------------------------------------------------|
| `DEPS_POLL_SECS`   | none — remove it from your settings/env           |
| `DEPS_WAIT_SECS`   | none — remove it from your settings/env           |

## `spindrift engage` (removed in v0.2.0)

`spindrift engage <issue>` was removed in **v0.2.0**. Use
`spindrift recover <issue>` instead — it performs the same merge-gate/adopt
operation.

| Removed command              | Replacement command           |
|------------------------------|-------------------------------|
| `spindrift engage <issue>`   | `spindrift recover <issue>`   |
