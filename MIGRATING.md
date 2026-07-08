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

## `spindrift engage` → `spindrift recover`

`spindrift engage <issue>` is deprecated and will be removed in **v0.2.0**.
Use `spindrift recover <issue>` instead — it performs the same merge-gate/adopt
operation. Running `engage` still works but prints a one-line notice to stderr.

| Old command                  | New command                   |
|------------------------------|-------------------------------|
| `spindrift engage <issue>`   | `spindrift recover <issue>`   |
