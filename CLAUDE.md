# spindrift

## Issue tracker

Issues live on GitHub (`jordansmall/spindrift`). File agent-ready issues via the
`/to-tickets` skill, never ad-hoc `gh issue create`.

### Dispatch kinds

The four kinds (work, research, butler, recover) are declared once, as
the Nix kind rows in `lib/dispatch-kinds/`, and `nix run .#regen` renders
them into the descriptor package `cmd/launcher/internal/dispatchkind`
(`kinds_gen.go`). Every kind fact — keying, label family, prompt file,
settle strategy, and the rest — is an axis on that row; neither the
launcher nor the Box branches on a kind name to re-derive one. See [ADR
0056](docs/adr/0056-the-butler-is-an-issueless-dispatch-kind-with-its-state-in-git.md)
(amendments, issues #3996 and #5668).

### Labels

**File new agent-ready issues with `ready-for-agent`.** Work issues move
through the triage labels below (`.github/workflows/agent-dispatch.yml`);
[Label lifecycle](docs/reference.md#label-lifecycle) is the full record,
including every path that ends in `agent-failed`. On Jira these labels are
the fallback under the default `JIRA_LIFECYCLE=hybrid`; `status` mode keeps
the work lifecycle on native statuses alone (see [Jira
lifecycle](docs/reference.md#jira-lifecycle-status)).

| Label | Meaning |
|---|---|
| `ready-for-agent` | fully specified; an AFK agent may pick it up |
| `agent-trigger` | fires one CI dispatch run; the claim swaps it and `ready-for-agent` for `agent-in-progress` |
| `agent-in-progress` | a Box is working the issue |
| `agent-complete` | landed green, or the change was already on the default branch |
| `agent-failed` | the Box failed or the PR could not land; human triage, re-label to retry (`spindrift recover <n>` for a stranded read-only bundle) |
| `agent-ambiguous-spec` | the pre-implement gate halted on a contradictory spec and posted questions; resolve them, then re-apply `agent-trigger` |

The provenance labels `agent-review-finding`, `agent-research-finding`,
`agent-butler-finding`, `agent-butler-patch`, and `agent-tuning-finding` mark
issues the host filed from a finding. They never carry a dispatch label: a
human promotes them to `ready-for-agent`, except for the butler's opt-in
promotion and patch PRs ([Butler](docs/reference.md#butler)). A PR closing an
`agent-tuning-finding` issue always merges by hand ([Merge
guard](docs/reference.md#merge-guard)).

Research (ADR 0022, `.github/workflows/agent-research.yml`) uses a disjoint
family, so an issue may wear one label from each: `agent-research-trigger`
(one CI run) or `agent-research` (the `spindrift research` / daemon queue) →
`agent-research-in-progress` → one verdict comment and
`agent-research-recommend`, `agent-research-reject` (auto-closes as not
planned), or `agent-research-unclear` (answer, then re-apply the trigger);
`agent-research-failed` means no verdict. Research never opens a PR. See
[Research dispatch](docs/reference.md#research-dispatch).

### Dispatch authentication

`agent-dispatch.yml` and `agent-recover.yml` mint a short-lived GitHub App
installation token per run, re-minted every 45 minutes by the
`gh-token-refresher` action, instead of using the `SPINDRIFT_GH_TOKEN` PAT. See
[GitHub App installation
token](docs/reference.md#github-app-installation-token-recommended) and, for
research's optional least-privilege App, [Research
token](docs/reference.md#research-token-least-privilege-optional).

### Comment injection trust boundary

The label gates which issues get dispatched — only triage-role holders can apply
it. But once labeled, the issue body and **every comment from any GitHub user**
feed the agent as prompt input. The trust boundary is the label, not the issue or
comment author. The launcher drops comments a maintainer has minimized on GitHub
from the transcript it renders, but that is a mitigation for that transcript
only: the Box's token can still read the full thread, and the agent still fetches
parent and linked issues itself, minimized comments included. A vetted-transcript
filter (#382, not yet active) will keep only GitHub OWNER/MEMBER/COLLABORATOR
comments and treat trackers that cannot attest standing (Forgejo, Jira) as
untrusted — see SECURITY.md.

## Worktrees

**Always do task work in a dedicated git worktree, one per task/branch.** Do not
edit files directly on whatever branch happens to be checked out:

```sh
git worktree add ../spindrift-<task> -b <branch> origin/main
```

## Build and check output

Whatever the tool, redirect its output to a file and grep/tail that file;
never stream build logs, store paths, or eval traces into the context. Write
the log and grep it in the **same shell invocation and sandbox mode** —
`$TMPDIR` differs across the sandbox boundary. The gate below shows the
pattern.

## Nix edits

spindrift dogfoods `nixStoreWritable` + `extraClosures` (ADR 0018, issues
#469 and #470), so a Box working this repo has a writable `/nix/store` and the
check closure pre-baked: run real checks rather than guessing. The
`/nix-checks` skill (`skills/nix-checks/SKILL.md`) mirrors this section —
change one and change the other; `nix/checks/nix-checks-lore-parity.nix`
pins the shared clauses.

Run the **scoped** gate, not the full flake check (issue #581):

```sh
nix run .#gate >"$TMPDIR/checks.log" 2>&1; echo "exit=$?"
grep -nE '^gate:|error|FAIL' "$TMPDIR/checks.log" || tail -n 40 "$TMPDIR/checks.log"
```

It builds the inbox set (go test/vet/fmt, shellcheck, nil-clean,
marker/parity checks) in parallel `nix-eval-jobs` workers, prints a `gate:`
line per failure plus a summary, and exits non-zero on any failure. It skips
the bats suite — build `.#checks.<system>.bats-shard-N` when you change bash
under test — the OCI image checks (the Box already is that image; issue #565),
and `docs-edit-isolation`, which costs a large share of a cold eval by
re-evaluating the whole checkset twice (issue #5849). `nix build
.#checks-inbox` is the same set evaluated single-threaded, for CI and humans.
A `gate:` line saying paths "exist in this Box's store but not in its
database" means the host realised them after the Box started (issue #6095),
not a regression: start a fresh Box or leave it to CI.

Checks run in two tiers. While iterating, run the narrowest check: `go test`
on one package, one bats shard, or `nix build .#checks.<system>.<name> -L`
(list attrs with `nix eval .#checks.<system> --apply builtins.attrNames`).
Run the full gate once per pass, just before the pass's final commit. After a
rebase, re-run it only if the rebase hit conflicts or brought in changes to
files the branch touches. A Box's worker subagents run targeted checks only —
its coordinator agent owns the full gate.

The gate holds no Connector check: a Connector's Go and conformance checks run
only from its own subflake, in CI's `connector` lane. A change touching a
Connector contract path (`.github/scripts/ci-connector-contract`) runs
`nix run .#connector-dev -- <name> flake check` for each first-party Connector
under `connectors/`; a change to one Connector, or to its core package
`cmd/launcher/internal/{forge,driver}/<name>`, runs that one's.

Keep `-L` (`--print-build-logs`) on every single-attr `nix build`: without it
a failure prints only a store path and a `[build failed]` line, costing a turn
on `nix log`. Pass no `-j`, `--max-jobs`, or `--cores`: the Box's `nix.conf`
pins `cores = 4` and `max-jobs = 2` (lib/image.nix's `nixConfigFile`) and the
gate sizes its own workers.

A check failure is deterministic: never re-run a failed check unchanged, not
under reduced parallelism and not after a sleep — fix the code or read the
log, then re-run. The one exception is an `EXIT:137` (out-of-memory) kill,
which is not a check result: re-run a smaller target (the gate instead of
`nix flake check`), and if the gate itself was killed, run `nix build
.#checks-inbox -L --max-jobs 1 --cores 1` once.

Nix flakes only evaluate git-tracked files: `git add` any new file before the
first build that touches it, or it aborts with "is not tracked by Git".

The full `nix flake check` is what CI runs (a docs-only PR builds just
`checks-docs`; issue #5155). Run it in-box only if you touched
`nix/checks/image.nix`, `lib/image.nix`, or anything else baked into the
image. Preferring the scoped gate is a firm rule, and it **overrides** any
acceptance criteria in an issue that ask for `nix flake check` more loosely.

Run `nil diagnostics path/to/file.nix` on each changed `*.nix` file while
iterating; it catches structural mistakes without a store build but does not
replace the gate. Prefer the devShell toolchain (`nix develop -c <cmd>`). In a
Box without `nixInBox` plus `nixStoreWritable`, fall back to `nil diagnostics`
and the baked `gofmt -l .`, `go vet ./...`, `go test ./...`, and say so; a
bwrap Box with both knobs runs real checks too (ADR 0042).

A Driver's Nix checks live in `nix/checks/drivers/<name>.nix`, named after its
row in `lib/drivers/default.nix`; `nix/checks/go.nix` generates its
`drivers-<name>-go-test` from the same row.

The Go version floor is `cmd/launcher/go.mod`'s `go` directive; Go comes from
the locked `nixpkgs` input, so a bump only needs that input's `go.version` to
meet it. Bump `dashboard/go.mod`, `sdk/go.mod`, `connectors/claude/go.mod`,
`connectors/github/go.mod`, `connectors/jira/go.mod`,
`nix/fixtures/connector-wasm/go.mod`, and `nix/fixtures/connector/go.mod`
alongside.

## Shell edits

Run `shellcheck` on each changed `*.sh`/`*.bash` file and resolve all findings
before finishing. It is on PATH in the dogfood Box.

## Running `gh`

`gh` commands need network + the macOS keychain, which the command sandbox blocks
(TLS cert failure via trustd; token unreadable). Run `gh` **outside the sandbox**
(`dangerouslyDisableSandbox: true`) on the first attempt so a failed-then-retried
call doesn't fire a mutating action twice.
