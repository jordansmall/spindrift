# spindrift

## Issue tracker

Issues live on GitHub (`jordansmall/spindrift`). File agent-ready issues via the
`/to-tickets` skill, never ad-hoc `gh issue create`.

### Triage label lifecycle

Agent issues move through these labels (see `.github/workflows/agent-dispatch.yml`):

- `ready-for-agent`: fully specified and ready for an AFK agent to pick up. **File
  new agent-ready issues with this label.**
- `agent-trigger`: adding it to an issue fires one dispatch run. The workflow
  claims the issue up front by swapping `agent-trigger`/`ready-for-agent` for
  `agent-in-progress`.
- `agent-in-progress`: an AFK agent is working the issue.
- `agent-complete`: agent work merged and green.
- `agent-failed`: the Box exited non-zero. It needs human triage; re-label to retry.
- `agent-review-finding`: the Filer files it from a non-blocking review
  finding (#393). It never carries a dispatch label
  (`agent-trigger`/`ready-for-agent`). A human promotes it to
  `ready-for-agent` like any other issue before an agent picks it up.

### Dispatch authentication

`agent-dispatch.yml` and `agent-recover.yml` authenticate to GitHub by minting a
short-lived **GitHub App installation token** per run (`actions/create-github-app-token`
from the worker App secrets `SPINDRIFT_AGENT_WORKER_APP_ID` /
`SPINDRIFT_AGENT_WORKER_APP_PRIVATE_KEY`), not the long-lived `SPINDRIFT_GH_TOKEN`
fine-grained PAT. The App installation has its own rate-limit bucket, separate
from any personal PAT. That fixes the 403 and secondary rate limiting hit during
dispatch, CI polling, and merge. The composite `agent-setup` action is unchanged
and consumes whatever `gh-token` it receives. An installation token expires about
1h after minting, so both workflows pair the mint step with the
`gh-token-refresher` composite action. It re-mints a fresh token every 45 minutes
for the rest of the job and hands the launcher `--gh-token-refresh-file` (issue
#1027). The App private key stays in that background loop and never reaches the
launcher or the Box. See [GitHub App installation
token](docs/reference.md#github-app-installation-token-recommended) for the full
setup, the required App permissions, and how the refresh works.

### Research label lifecycle

A second, disjoint label family (ADR 0022; see `.github/workflows/agent-research.yml`)
drives the advise-only research Dispatch kind, never the work path above.
Claiming a research issue strips only the dispatch and verdict labels below.
`agent-research-finding` is a provenance label the claim step never touches. A
work lifecycle label (`ready-for-agent`, `agent-in-progress`, ...) therefore
survives a research claim untouched, and an issue may wear one label from each
family at once:

- `agent-research`: dual-role, both standing state and trigger. Apply it to fire
  one research dispatch. Re-apply it to retry after a crash, or to re-research
  after answering an `unclear` verdict's questions. This is the same gesture as
  `agent-trigger`.
- `agent-research-in-progress`: a Box is reviewing the issue against the
  Target repo and will post a single structured verdict comment.
- `agent-research-recommend`: the issue is relevant and enriched with context
  for a worker. Promote it to `ready-for-agent`.
- `agent-research-reject`: a false positive, not worth doing, or a duplicate
  (named in the comment). Applying it **closes the issue as not planned**
  automatically. `agent-research-close.yml` fires on the label and does the
  close, so the verdict and the issue state never drift and the Filer's
  closed-`agent-research-reject` suppression holds. This is a
  *successful* conclusion (`Complete`), never `agent-research-failed`.
- `agent-research-unclear`: relevance needs an answer only a human has.
  Answer the researcher's questions in the comment, then re-apply
  `agent-research`.
- `agent-research-failed`: the Box crashed or produced no verdict. This is a
  human triage queue distinct from `agent-research-reject`, so crash-retry and
  verdict-review never mix.
- `agent-research-finding`: the Filer files it from a research finding
  (ADR 0041). It never carries a dispatch label
  (`agent-trigger`/`ready-for-agent`). A human promotes it to
  `ready-for-agent` like any other issue before an agent picks it up.

Research never opens a PR, watches CI, or merges. It posts one comment and
stops. `spindrift doctor` checks these labels too and, in interactive mode,
offers to create them, but treats them as advisory: unlike the triage labels, a
missing research label never fails the check. To create them manually, see
[Create the research
labels](docs/reference.md#create-the-research-labels-on-the-target-repo).
The workflow authenticates with an optional least-privilege research GitHub App
(Issues RW, Contents R, Metadata R). Set the
`SPINDRIFT_AGENT_RESEARCH_APP_ID` / `SPINDRIFT_AGENT_RESEARCH_APP_PRIVATE_KEY`
repository secrets and `agent-research.yml` mints a short-lived installation
token per run. When the App is unset, it falls back to the main
`SPINDRIFT_GH_TOKEN`. See [Research
token](docs/reference.md#research-token-least-privilege-optional). To drive
research continuously instead of a one-off `spindrift research`, run the
daemon (`nix run .#daemon`) with the `research` kind selector, as in `nix run
.#daemon -- research`. Omit the selector to draw both dispatch and research off
one pool (issue #3541).

### Comment injection trust boundary

The label gates which issues get dispatched, and only triage-role holders can
apply it. Once an issue is labeled, though, its body and **every comment from
any GitHub user** feed the agent as prompt input. The trust boundary is the
label, not the issue or comment author.

## Worktrees

**Always do task work in a dedicated git worktree, one per task/branch.** Do not
edit files on whatever branch happens to be checked out. Without worktrees,
parallel work tangles: uncommitted edits end up on the wrong branch, you juggle
stash/pop, and tasks churn against each other in a single tree. A worktree per
task keeps each change on its own branch from the start.

```sh
git worktree add ../spindrift-<task> -b <branch> origin/main
```

## Build and check output

For any tool, whether `nix build`, `nix flake check`, `go test`, or `shellcheck`
sweeps, redirect its output to a file on disk and grep or tail that file for
what you need. Never stream the full output into the conversation context.
Build logs, store paths, and eval traces are huge and mostly noise, and they
crowd out room for the task.

```sh
<check-command> >"$TMPDIR/checks.log" 2>&1; echo "exit=$?"
grep -nE 'error|FAIL' "$TMPDIR/checks.log" || tail -n 40 "$TMPDIR/checks.log"
```

See "## Nix edits" below for this repo's actual in-box gate (the concrete
`nix build .#checks-inbox -L` invocation).

Write the log and grep it in the **same shell invocation and sandbox mode**.
`$TMPDIR` differs across the sandbox boundary, so an unsandboxed follow-up
cannot see a file written sandboxed, and vice versa.

## Nix edits

spindrift dogfoods the `nixStoreWritable` + `extraClosures` knobs (ADR 0018,
issue #469) on its own Consumer config (issue #470). The Box working a
spindrift issue therefore has a writable `/nix/store` and the check/dev closure
pre-baked, which makes real checks the primary in-box gate. Prefer them over
guessing. Run the **scoped** target, though, not the full flake check (issue
#581). `checks-inbox` covers every source-level check (go test/vet/fmt,
shellcheck, nil-clean, marker/parity checks). It skips the checks that build or
inspect the OCI image (`dockerTools.buildLayeredImage`) or assert facts about
the box's own baked toolchain. The box is already built from that image, so
re-baking it in-box tests nothing the pre-dispatch build didn't. Nested image
builds are also heavy and unreliable in a Box (issue #565 saw one killed with
`EXIT:137`):

```sh
nix build .#checks-inbox -L >"$TMPDIR/checks.log" 2>&1; echo "exit=$?"
grep -nE 'error|FAIL' "$TMPDIR/checks.log" || tail -n 40 "$TMPDIR/checks.log"
```

`-L` (`--print-build-logs`) is part of that invocation, not an extra.
Without it a failure prints only the failing derivation's store path
and a `[build failed]` line, so the compile or test error costs a second
turn running `nix log`. Pass no `-j`, `--max-jobs`, or `--cores` flags
alongside it. The Box's baked `nix.conf` pins `cores = 4` (lib/image.nix's
`nixConfigFile`), and Nix's own default already builds one derivation at
a time, so hand-tuning either on top is guesswork. The one exception, an
`EXIT:137` kill, is below.

A check failure is deterministic: the same derivation hash fails the same
way however it is scheduled. Never re-run a failed check unchanged, not
under reduced parallelism (the `EXIT:137` carve-out below is outside
this rule) and not after a sleep. Each retry is a multi-minute build with a
foregone conclusion. The only remedies are to fix the code or read the
log `-L` already printed, then re-run only after a real edit. A build the
kernel killed (`EXIT:137`, out of memory) is the exception. That is not a
check result at all, so a re-run is legitimate there, unlike a real
failure. Under the full `nix flake check`, re-run the scoped
`checks-inbox` target instead. Shrink the target, not `--cores`.
Under `checks-inbox` itself there is no smaller target left, so
`--cores 1` is the one sanctioned exception to the no-resource-flags
rule above. `max-jobs` is already 1, so what holds the memory is the
concurrent compiles or test binaries inside a single derivation. Reach
for it once, only after an `EXIT:137`, and never for a real check
failure.

Nix flakes only evaluate git-tracked files. `git add` any new file (for
example `git add -A`) before the first `nix build`/`nix flake check` that
touches it, or the build aborts with "is not tracked by Git" and burns a
checks cycle.

If the repo has a `flake.nix` devShell, prefer its pinned toolchain over an
ambient one:

```sh
nix develop -c <check-command>
```

If `nix develop` is unavailable or fails, fall back to the baked toolchain and
say so. For the Go tree here that is `gofmt -l .`, `go vet ./...`, and
`go test ./...`.

The full `nix flake check`, which adds the image-building checks
`checks-inbox` skips, is what CI runs pre-dispatch and pre-merge. It isn't the
in-box gate. Run it in-box only if you touched `nix/checks/image.nix`,
`lib/image.nix`, or anything else that changes what gets baked into the
image. That is the one case the scoped target can't cover:

```sh
nix flake check
```

Preferring the scoped target is a firm rule, and it **overrides** any
acceptance criteria in an issue that ask for `nix flake check` more loosely.
The same rules in agent-facing form live in `skills/nix-checks/SKILL.md`, the
dogfood-only `/nix-checks` skill that this section mirrors. Change one and
change the other.

Run `nil diagnostics` on each changed `*.nix` file as a fast per-file
pre-check while iterating. It catches syntax errors, duplicate attribute
keys, undefined variables, and unused bindings without a store round-trip:

```sh
nil diagnostics path/to/file.nix
```

`nil diagnostics` exits non-zero on errors (warnings still exit 0). It
complements `checks-inbox` before finishing the task but does not replace it.
`nil` catches structural mistakes early; only a real check build catches
evaluation and build errors. If neither `checks-inbox` nor `nix flake check`
is available, fall back to `nil diagnostics` and say so. That happens in a Box
built without the self-test knobs (`nixInBox` plus `nixStoreWritable`). The
runner is not itself a reason. Under bwrap a writable store is an ephemeral
tmpfs overlay over the read-only host store (ADR 0042), so a bwrap Box carrying
both knobs runs the same real checks the podman one does.

The repo's Go version floor is whatever `cmd/launcher/go.mod`'s `go`
directive says. There is no separate Nix-level Go version pin to keep in
sync. Every Nix expression that needs Go pulls `pkgs.go`/`p.go` from
the single `nixpkgs` flake input, so the floor comes transitively
through `flake.lock`. Bumping the `go.mod` directive only requires
re-verifying that the locked `nixpkgs` input's `go.version` still meets
the new floor.

## Shell edits

Before finishing any task that touches `*.sh` or `*.bash` files, run
`shellcheck` on each changed file and resolve all findings:

```sh
shellcheck path/to/file.sh
```

`shellcheck` is baked into the dogfood Box alongside `nil`, so it's on PATH for
the `agent` user (uid 1000) without a store build. It complements, but does not
replace, the `shellcheck` check `nix flake check` runs in CI.

## Running `gh`

`gh` commands need network and the macOS keychain, which the command sandbox
blocks (the TLS cert check fails via trustd and the token is unreadable). Run
`gh` **outside the sandbox** (`dangerouslyDisableSandbox: true`) on the first
attempt so a failed-then-retried call doesn't fire a mutating action twice.
