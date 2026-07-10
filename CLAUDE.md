# spindrift

## Issue tracker

Issues live on GitHub (`jordansmall/spindrift`). File agent-ready issues via the
`/to-issues` skill, never ad-hoc `gh issue create`.

### Triage label lifecycle

Agent issues move through these labels (see `.github/workflows/agent-dispatch.yml`):

- `ready-for-agent` — fully specified, ready for an AFK agent to pick up. **File new
  agent-ready issues with this label.**
- `agent-trigger` — adding it to an issue fires one dispatch run; the workflow claims
  the issue by swapping `agent-trigger`/`ready-for-agent` → `agent-in-progress` up front.
- `agent-in-progress` — an AFK agent is actively working the issue.
- `agent-complete` — agent work merged and green.
- `agent-failed` — the Box exited non-zero; needs human triage, re-label to retry.

### Comment injection trust boundary

The label gates which issues get dispatched — only triage-role holders can apply
it. But once labeled, the issue body and **every comment from any GitHub user**
feed the agent as prompt input. The trust boundary is the label, not the issue or
comment author.

## Worktrees

**Always do task work in a dedicated git worktree, one per task/branch.** Do not
edit files directly on whatever branch happens to be checked out. Parallel work
gets increasingly tangled without worktrees — uncommitted edits stranded on the
wrong branch, stash/pop juggling, and cross-task churn in a single tree. A
worktree per task keeps each change isolated on its own branch from the start.

```sh
git worktree add ../spindrift-<task> -b <branch> origin/main
```

## Nix edits

spindrift dogfoods `nixStoreWritable` + `extraClosures` (ADR 0018, issue #470)
on its own Consumer config, so the in-box Box working a spindrift issue has a
writable `/nix/store` and the check/dev closure pre-baked. That makes real
`nix flake check` the primary in-box gate — prefer it over guessing:

```sh
nix flake check
```

Run `nil diagnostics` on each changed `*.nix` file as a fast, per-file
pre-check while iterating — it catches syntax errors, duplicate attribute
keys, undefined variables, and unused bindings without a store round-trip:

```sh
nil diagnostics path/to/file.nix
```

`nil diagnostics` exits non-zero on errors (warnings still exit 0). It
complements, but does not replace, a full `nix flake check` before finishing
the task — `nil` catches structural mistakes early; only `nix flake check`
catches evaluation and build errors. If `nix flake check` is genuinely
unavailable (e.g. a Box built without the self-test knobs, or the bwrap
runner, which keeps its store read-only), fall back to `nil diagnostics` and
say so.

## Shell edits

Before finishing any task that touches `*.sh` or `*.bash` files, run
`shellcheck` on each changed file and resolve all findings:

```sh
shellcheck path/to/file.sh
```

`shellcheck` is baked into the dogfood Box alongside `nil`, so it's on PATH as
the `agent` user (uid 1000) without a store build. It complements, but does not
replace, the `shellcheck` check `nix flake check` runs in CI.

## Running `gh`

`gh` commands need network + the macOS keychain, which the command sandbox blocks
(TLS cert failure via trustd; token unreadable). Run `gh` **outside the sandbox**
(`dangerouslyDisableSandbox: true`) on the first attempt so a failed-then-retried
call doesn't fire a mutating action twice.
