# spindrift

## Issue tracker

Issues live on GitHub (`jordansmall/spindrift`). File agent-ready issues via the
`/to-tickets` skill, never ad-hoc `gh issue create`.

### Triage label lifecycle

Agent issues move through these labels (see `.github/workflows/agent-dispatch.yml`):

- `ready-for-agent` — fully specified, ready for an AFK agent to pick up. **File new
  agent-ready issues with this label.**
- `agent-trigger` — adding it to an issue fires one dispatch run; the workflow claims
  the issue by swapping `agent-trigger`/`ready-for-agent` → `agent-in-progress` up front.
- `agent-in-progress` — an AFK agent is actively working the issue.
- `agent-complete` — agent work merged and green.
- `agent-failed` — the Box exited non-zero; needs human triage, re-label to retry.
- `agent-review-finding` — filed by the Filer from a non-blocking review
  finding (#393). Never carries a dispatch label
  (`agent-trigger`/`ready-for-agent`) — a human promotes it to
  `ready-for-agent` like any other issue before an agent picks it up.

### Dispatch authentication

`agent-dispatch.yml` and `agent-recover.yml` authenticate to GitHub by minting a
short-lived **GitHub App installation token** per run (`actions/create-github-app-token`
from the worker App secrets `SPINDRIFT_AGENT_WORKER_APP_ID` /
`SPINDRIFT_AGENT_WORKER_APP_PRIVATE_KEY`), not the long-lived `SPINDRIFT_GH_TOKEN`
fine-grained PAT. The App installation has its own rate-limit bucket, isolated
from any personal PAT — the fix for the 403 / secondary rate limiting hit during
dispatch, CI polling, and merge. The composite `agent-setup` action is unchanged;
it consumes whatever `gh-token` it is handed. **Caveat:** an installation token
expires ~1h after minting, so a Box whose run exceeds ~1h fails at `gh pr merge`
until in-Box token refresh lands (#1027) — keep `SPINDRIFT_GH_TOKEN` provisioned
as the fallback until then. See [GitHub App installation
token](docs/reference.md#github-app-installation-token-recommended) for the full
setup and required App permissions.

### Research label lifecycle

A second, disjoint label family (ADR 0022; see `.github/workflows/agent-research.yml`)
drives the advise-only research Dispatch kind — never the work path above.
Claiming a research issue strips only these labels, so a work lifecycle label
(`ready-for-agent`, `agent-in-progress`, ...) survives a research claim
untouched, and an issue may legitimately wear one label from each family at
once:

- `agent-research` — dual-role: standing state and trigger. Apply it to fire
  one research dispatch; re-apply it to retry (crash) or re-research (after
  answering an `unclear` verdict's questions) — the same gesture as
  `agent-trigger`.
- `agent-research-in-progress` — a Box is reviewing the issue against the
  Target repo and will post a single structured verdict comment.
- `agent-research-recommend` — relevant and enriched with context for a
  worker — promote it to `ready-for-agent`.
- `agent-research-reject` — false positive, not worth doing, or a duplicate
  (named in the comment) — close it. This is a *successful* conclusion
  (`Complete`), never `agent-research-failed`.
- `agent-research-unclear` — relevance needs an answer only a human has —
  answer the researcher's questions in the comment, then re-apply
  `agent-research`.
- `agent-research-failed` — the Box crashed or produced no verdict; a human
  triage queue distinct from `agent-research-reject`, so crash-retry and
  verdict-review never mix.

Research never opens a PR, watches CI, or merges — it posts one comment and
stops. `spindrift doctor` does not manage these labels (they're a fixed
vocabulary, not a configurable knob); create them manually — see [Create the
research labels](docs/reference.md#create-the-research-labels-on-the-target-repo).
The workflow accepts an optional `SPINDRIFT_RESEARCH_GH_TOKEN` least-privilege
PAT (Issues RW, Contents R, Metadata R), falling back to the main
`SPINDRIFT_GH_TOKEN` when unset — see [Research
token](docs/reference.md#research-token-least-privilege-optional). To drive
research through the dogfood loop instead of a one-off `spindrift research`,
run `dogfood.sh` with `DOGFOOD_KIND=research`.

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

## Build and check output

Whatever the tool — `nix build`, `nix flake check`, `go test`, `shellcheck`
sweeps — redirect its output to a file on disk and grep/tail that file for
what you need. Never stream the full output into the conversation context:
build logs, store paths, and eval traces are huge and mostly noise, and they
crowd out room for the actual task.

```sh
nix build .#checks-inbox >"$TMPDIR/checks.log" 2>&1; echo "exit=$?"
grep -nE 'error|FAIL' "$TMPDIR/checks.log" || tail -n 40 "$TMPDIR/checks.log"
```

Write the log and grep it in the **same shell invocation and sandbox mode** —
`$TMPDIR` differs across the sandbox boundary, so a file written sandboxed is
not visible to an unsandboxed follow-up (and vice versa).

## Nix edits

spindrift dogfoods the `nixStoreWritable` + `extraClosures` knobs (ADR 0018,
issue #469) on its own Consumer config (issue #470), so the Box working a
spindrift issue has a writable `/nix/store` and the check/dev closure
pre-baked. That makes real checks the primary in-box gate — prefer them over
guessing. But run the **scoped** target, not the full flake check (issue
#581): `checks-inbox` covers every source-level check (go test/vet/fmt,
shellcheck, nil-clean, marker/parity checks) and skips the checks that
build/inspect the OCI image (`dockerTools.buildLayeredImage`) or assert facts
about the box's own baked toolchain — the box is already built from that
image, so re-baking it in-box tests nothing the pre-dispatch build didn't,
and nested image builds are heavy/unreliable in a Box (issue #565 saw one
kicked with `EXIT:137`):

```sh
nix build .#checks-inbox
```

The full `nix flake check` — including the image-building checks
`checks-inbox` skips — is what CI runs pre-dispatch/pre-merge; it isn't the
in-box gate. Run it in-box only if you touched `nix/checks/image.nix`,
`lib/image.nix`, or anything else that changes what gets baked into the
image, since that's the one case the scoped target can't cover:

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
complements, but does not replace, `checks-inbox` before finishing the task —
`nil` catches structural mistakes early; only a real check build catches
evaluation and build errors. If neither `checks-inbox` nor `nix flake check`
is available (e.g. a Box built without the self-test knobs, or the bwrap
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
