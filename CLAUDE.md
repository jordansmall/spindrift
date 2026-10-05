# spindrift

## Issue tracker

Issues live on GitHub (`jordansmall/spindrift`). File agent-ready issues via the
`/to-tickets` skill, never ad-hoc `gh issue create`.

### Dispatch kinds

The three kinds (work, research, butler) are declared once, in
`cmd/launcher/internal/dispatchkind`. Every kind fact — keying, label
family, prompt file, settle strategy, and the rest — is a row on that
descriptor; neither the launcher nor the Box branches on a kind name to
re-derive one. See [ADR
0056](docs/adr/0056-the-butler-is-an-issueless-dispatch-kind-with-its-state-in-git.md)
(amendment, issue #3996).

### Triage label lifecycle

Agent issues move through these labels (see `.github/workflows/agent-dispatch.yml`):

- `ready-for-agent` — fully specified, ready for an AFK agent to pick up. **File new
  agent-ready issues with this label.**
- `agent-trigger` — adding it to an issue fires one dispatch run; the workflow claims
  the issue by swapping `agent-trigger`/`ready-for-agent` → `agent-in-progress` up front.
- `agent-in-progress` — an AFK agent is actively working the issue.
- `agent-complete` — agent work merged and green, or the issue's change was
  found already on the default branch (`status=already-resolved`, closed as
  completed).
- `agent-failed` — the Box failed or stopped `status=blocked`, or its PR
  could not land (e.g. red CI after the fix passes, a merge-gate failure, no
  PR found); needs human triage, re-label to retry.
- `agent-ambiguous-spec` — the pre-implement gate found the issue internally
  contradictory and the Box halted on purpose (`status=ambiguous`), posting
  its questions as a comment. Not a crash and never `agent-failed`; a human
  resolves the spec, then re-applies `agent-trigger` (or `ready-for-agent`)
  to retry. See [Create the ambiguous-spec
  label](docs/reference.md#create-the-ambiguous-spec-label-on-the-target-repo).
- `agent-review-finding` — filed by the Filer from a non-blocking review
  finding (#393); the host also ensure-creates the label when it files a
  relayed finding; doctor requires it on a read-only deployment with a Filer.
  Never carries a dispatch label
  (`agent-trigger`/`ready-for-agent`) — a human promotes it to
  `ready-for-agent` like any other issue before an agent picks it up.
- `agent-butler-finding` — filed by the host from a butler Chore finding
  (ADR 0056); the host creates the label if missing, and a butler daemon
  refuses to start without it (except on `local`/`jira`, which have no
  label registry to check). Never carries a dispatch label
  (`agent-trigger`/`ready-for-agent`) — a human promotes it to
  `ready-for-agent` like any other issue before an agent picks it up.
  **Opt-in exception** (issue #3880): the host itself adds
  `ready-for-agent` (or your configured `LABEL`) at settle when the
  finding's class clears the Chore's host-side allow-list, a bounded
  number of files, an in-Box reviewer's concurrence, and the day's
  promotion budget — off by default. See
  [Butler](docs/reference.md#butler).
- `agent-butler-patch` — provenance label for a butler finding the host lands
  as a patch PR (ADR 0057), alongside `agent-butler-finding`. Never carries a
  dispatch label, except when the host's push or PR create fails and the
  finding falls back to promotion — the issue then keeps this label with no
  patch PR behind it (a failed create first adopts any PR the forge made
  anyway, else deletes the pushed branch; issue #4112). The host ensure-creates
  the label, and a butler daemon refuses to start without it while the patch
  rung is on. The patch rung is off until
  `BUTLER_MAX_PATCHES_PER_DAY` is greater than 0, and also needs an issue
  tracker that can add a label after filing (`github`/`forgejo`; never
  `ISSUE_TRACKER=local`), since a failed landing's fallback has nowhere
  else to put the work label, and that tracker must name the same backend
  as `CODE_FORGE` (`github`+`github` or `forgejo`+`forgejo`), since `Closes
  #N` and the agent branch both name an issue in the forge's own
  namespace (issue #4074). The patch PR lands through the work merge
  gate with zero fix passes — even when the run's Ledger Done commit is
  lost after the PR opened (issue #4118): a clean merge closes the
  finding and marks it `agent-complete` like any landed work PR (a
  green PR left unmerged — `MERGE_MODE=manual`, a `MERGE_GUARD_PATHS`
  hit, or a merge blocked after green — stays open, marked ready, for
  a human to merge, while `MERGE_MODE=auto` queues it at the forge;
  neither adds `agent-complete`); a red run or a CI-poll timeout leaves the
  PR in draft and the issue wearing only its two labels, for a human to
  relabel `ready-for-agent` or close — `agent-failed` never applies to
  this path. [Butler](docs/reference.md#butler) is the full record;
  change it and this summary together.

### Dispatch authentication

`agent-dispatch.yml` and `agent-recover.yml` authenticate to GitHub by minting a
short-lived **GitHub App installation token** per run (`actions/create-github-app-token`
from the worker App's repository variable `SPINDRIFT_AGENT_WORKER_APP_ID` and repository
secret `SPINDRIFT_AGENT_WORKER_APP_PRIVATE_KEY`), not the long-lived `SPINDRIFT_GH_TOKEN`
fine-grained PAT. The App installation has its own rate-limit bucket, isolated
from any personal PAT — the fix for the 403 / secondary rate limiting hit during
dispatch, CI polling, and merge. The composite `agent-setup` action is unchanged;
it consumes whatever `gh-token` it is handed. An installation token expires ~1h
after minting, so both workflows pair the mint step with the `gh-token-refresher`
composite action, which re-mints a fresh token every 45 minutes for the rest of
the job and hands the launcher `--gh-token-refresh-file` (issue #1027) — the App
private key stays in that backgrounded loop, never reaching the launcher or the
Box. See [GitHub App installation
token](docs/reference.md#github-app-installation-token-recommended) for the full
setup, required App permissions, and how the refresh mechanism works.

### Research label lifecycle

A second, disjoint label family (ADR 0022; see `.github/workflows/agent-research.yml`)
drives the advise-only research Dispatch kind — never the work path above.
Claiming a research issue strips the trigger, the standing `agent-research`,
and the verdict/failed labels below (`agent-research-finding` is a
provenance label the claim step never touches), so a work lifecycle label
(`ready-for-agent`, `agent-in-progress`, ...) survives a research claim
untouched, and an issue may legitimately wear one label from each family at
once:

- `agent-research-trigger` — applying it fires one research run in CI;
  re-apply it to retry (crash) or re-research (after answering an
  `unclear` verdict's questions) — the same gesture as `agent-trigger`. The
  claim strips it before any build work, so it self-clears and never
  re-fires on its own.
- `agent-research` — the standing research queue: `spindrift research` and
  the daemon (`nix run .#daemon -- research`) draw from it. Every claim
  (CI's or the launcher's) strips it too, so a claimed issue drops out of
  the queue while a Box works it.
- `agent-research-in-progress` — a Box is reviewing the issue against the
  Target repo and will post a single structured verdict comment.
- `agent-research-recommend` — relevant and enriched with context for a
  worker — promote it to `ready-for-agent`.
- `agent-research-reject` — false positive, not worth doing, or a duplicate
  (named in the comment). Applying it **closes the issue as not planned**,
  automatically: `agent-research-close.yml` fires on the label and does the
  close, so the verdict and the issue state never drift, and the Filer's
  closed-`agent-research-reject` suppression actually holds. This is a
  *successful* conclusion (`Complete`), never `agent-research-failed`.
- `agent-research-unclear` — relevance needs an answer only a human has —
  answer the researcher's questions in the comment, then re-apply
  `agent-research-trigger` (CI) or `agent-research` (daemon /
  `spindrift research` queue).
- `agent-research-failed` — the Box crashed or produced no verdict; a human
  triage queue distinct from `agent-research-reject`, so crash-retry and
  verdict-review never mix.
- `agent-research-finding` — filed by the Filer from a research finding
  (ADR 0041). Never carries a dispatch label
  (`agent-trigger`/`ready-for-agent`) — a human promotes it to
  `ready-for-agent` like any other issue before an agent picks it up.

Research never opens a PR, watches CI, or merges — it posts one comment and
stops. `spindrift doctor` checks and, in interactive mode, offers to create
these labels too, but treats them as advisory: unlike the triage labels, a
missing research label never fails the check unless `--research` promotes the
tier, as the daemon's startup preflight does when the research kind is
explicitly selected. Doctor does not check or create
`agent-research-trigger` — like `agent-trigger`, it is repo-local Actions
trigger vocabulary, not a doctor-managed label — so create it
manually for the CI path; see [Create the research
labels](docs/reference.md#create-the-research-labels-on-the-target-repo).
The workflow authenticates with an optional least-privilege research GitHub App
(Issues RW, Contents R, Metadata R) — set the
`SPINDRIFT_AGENT_RESEARCH_APP_ID` repository variable and the
`SPINDRIFT_AGENT_RESEARCH_APP_PRIVATE_KEY` repository secret, and
`agent-research.yml` mints a short-lived installation token per run, falling
back to the main `SPINDRIFT_GH_TOKEN` when the App is unset — see [Research
token](docs/reference.md#research-token-least-privilege-optional). To drive
research continuously instead of a one-off `spindrift research`, run the
daemon (`nix run .#daemon`) with the `research` kind selector — `nix run
.#daemon -- research` — or omit the selector to draw from every configured
kind (dispatch, research, and, once `BUTLER_CHORES` enables one, the
butler) off one pool (issue #3541, #3878).

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
<check-command> >"$TMPDIR/checks.log" 2>&1; echo "exit=$?"
grep -nE 'error|FAIL' "$TMPDIR/checks.log" || tail -n 40 "$TMPDIR/checks.log"
```

See "## Nix edits" below for this repo's actual in-box gate (the concrete
`nix build .#checks-inbox -L` invocation).

Write the log and grep it in the **same shell invocation and sandbox mode** —
`$TMPDIR` differs across the sandbox boundary, so a file written sandboxed is
not visible to an unsandboxed follow-up (and vice versa).

## Nix edits

spindrift dogfoods the `nixStoreWritable` + `extraClosures` knobs (ADR 0018,
issue #469) on its own Consumer config (issue #470), so the Box working a
spindrift issue has a writable `/nix/store` and the check/dev closure
pre-baked. That makes real checks the primary in-box gate — prefer them over
guessing. But run the **scoped** target, not the full flake check (issue
#581): `checks-inbox` covers the source-level checks (go test/vet/fmt,
shellcheck, nil-clean, marker/parity checks) except the bats suite,
which CI runs and which would push a cold gate past the 10-minute Bash
cap — build `.#checks.<system>.bats-shard-N` directly when you change
bash under test. It also skips the checks that build/inspect the OCI image
(`dockerTools.buildLayeredImage`) or assert facts about the box's own
baked toolchain — the box is already built from that image, so
re-baking it in-box tests nothing the pre-dispatch build didn't, and
nested image builds are heavy/unreliable in a Box (issue #565 saw one
kicked with `EXIT:137`):

```sh
nix build .#checks-inbox -L >"$TMPDIR/checks.log" 2>&1; echo "exit=$?"
grep -nE 'error|FAIL' "$TMPDIR/checks.log" || tail -n 40 "$TMPDIR/checks.log"
```

`-L` (`--print-build-logs`) is part of that invocation, not an extra.
Without it a failure prints only the failing derivation's store path
and a `[build failed]` line, so the compile or test error costs a second
turn running `nix log`. Pass no `-j`, `--max-jobs`, or `--cores` flags
alongside it: the Box's baked `nix.conf` pins `cores = 4` and
`max-jobs = 2` (lib/image.nix's `nixConfigFile`), so hand-tuning either
on top is guesswork. The one exception, an
`EXIT:137` kill, is below.

Checks run in two tiers. While iterating, run the narrowest check that
covers the change — `go test` on one package, one bats shard, or a single
check attr with `nix build .#checks.<system>.<name> -L` (`<system>` is
e.g. `x86_64-linux`). List the attrs with `nix eval .#checks.<system>
--apply builtins.attrNames`. Run the full `checks-inbox` gate once per
pass, just before the pass's final commit. After a rebase, re-run it only
if the rebase hit conflicts or brought in changes to files the branch
touches; otherwise CI's full `nix flake check` is the final gate. A Box's
worker subagents run targeted checks only — its coordinator agent owns the
full gate.

A check failure is deterministic: the same derivation hash fails the same
way however it is scheduled. Never re-run a failed check unchanged — not
under reduced parallelism (the `EXIT:137` carve-out below is outside
this rule), not after a sleep. Each retry is a multi-minute build with a
foregone conclusion; the only remedies are to fix the code or read the
log `-L` already surfaced, and re-run only after a real edit. A build the
kernel killed (`EXIT:137`, out of memory) is the exception: that is not a
check result at all, so a re-run is legitimate there, unlike a real
failure. Under the full `nix flake check`, re-run the scoped
`checks-inbox` target instead — a smaller target, not a smaller
`--cores`. Under `checks-inbox` itself there is no smaller target left,
so `--max-jobs 1 --cores 1` is the one sanctioned exception to the
no-resource-flags rule above: it drops the two concurrent derivations to
one and the concurrent compiles or test binaries inside it to one as
well. Reach for it once, only after an `EXIT:137`, and never for a
check that genuinely failed.

Nix flakes only evaluate git-tracked files: `git add` any new file (e.g.
`git add -A`) before the first `nix build`/`nix flake check` that touches it,
or the build aborts with "is not tracked by Git" and burns a checks cycle.

If the repo has a `flake.nix` devShell, prefer its pinned toolchain over an
ambient one:

```sh
nix develop -c <check-command>
```

If `nix develop` is unavailable or fails, fall back to the baked toolchain —
`gofmt -l .`, `go vet ./...`, `go test ./...` for the Go tree here — and say
so.

The full `nix flake check` — including the image-building checks
`checks-inbox` skips — is what CI runs pre-dispatch/pre-merge; it isn't the
in-box gate. Run it in-box only if you touched `nix/checks/image.nix`,
`lib/image.nix`, or anything else that changes what gets baked into the
image, since that's the one case the scoped target can't cover:

```sh
nix flake check
```

Preferring the scoped target is a firm rule, and it **overrides** any
acceptance criteria in an issue that ask for `nix flake check` more loosely.
The same rules in agent-facing form live in `skills/nix-checks/SKILL.md`, the
dogfood-only `/nix-checks` skill this section's wording is mirrored from —
change one and change the other.

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
is available — a Box built without the self-test knobs (`nixInBox` plus
`nixStoreWritable`) — fall back to `nil diagnostics` and say so. The runner
is not itself a reason: under bwrap a writable store is an ephemeral tmpfs
overlay over the read-only host store (ADR 0042), so a bwrap Box carrying
both knobs runs the same real checks the podman one does.

The repo's Go version floor is whatever `cmd/launcher/go.mod`'s `go`
directive says. There is no separate Nix-level Go version pin to keep in
sync — every Nix expression that needs Go pulls `pkgs.go`/`p.go` from
the single `nixpkgs` flake input, so the floor is sourced transitively
through `flake.lock`. Bumping the `go.mod` directive only requires
re-verifying that the locked `nixpkgs` input's `go.version` still meets
the new floor.

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
