# Local code is the one host-mediated forge: read-only clone mount in, launcher-landed bundle out

## Context

ADR 0013 split the Forge into two independent axes — Issue Tracker and Code
Forge — and, on the code axis, **cut the fully-local path**: "A git remote is a
hard requirement on the Code Forge axis. The fully-local, no-remote path (mount
the operator's working copy, have the trusted launcher land the branch back)
was considered and cut: it would have punctured the Box isolation boundary or
required net-new machinery (a copy-out channel + host-side git)." The glossary
repeats it: "there is no mounting of a host working copy and no launcher-side
git."

Two things have changed since that cut, and together they reopen it on far
better terms:

- **ADR 0032 already built the pattern this needs.** It host-mediated the
  `local` *issue* content plane — a read-only mount in, a Launcher-posted
  comment out — and paid the isolation cost of documenting "the single, named
  exception to the Box's zero-shared-host-filesystem rule." The code plane can
  be the exact mirror of that, not net-new machinery.
- **"Launcher-side git" already exists.** The ADR 0013 amendment (#517) gave the
  `git` adapter a host-side `Merge`/`Rebase` that clones to a temp dir, merges,
  and pushes a ref back (`forge/git/git.go`). Landing code from the host is no
  longer hypothetical machinery to be "required"; it is a helper already in the
  tree.

The motivating workflow is not an air-gap. It is a solo operator breaking a
broad ticket into **small, testable, private seams**, chaining them locally,
and surfacing the whole thing as **one PR** for a team to review — without
polluting the shared tracker with sub-tickets the team won't care about, and
without pushing every half-finished seam to the shared remote. `ISSUE_TRACKER=local`
(ADR 0013) + ADR 0032 already keep the *issues* private and offline; the gap is
that the *code* still has to clone from and push to a network remote per seam.

## Decision

**Add `CODE_FORGE=local`, the code-plane mirror of the `local` Issue Tracker.**
This makes `local` the single host-mediated backend on *both* axes, unified by
one principle:

> **`local` = host-mediated because it is not reachable from inside the Box** —
> on the issue plane (ADR 0032) and now the code plane. Reachable backends
> (`github`, `git`) let the Box read and land directly; `local` does neither, so
> the Launcher mediates: a read-only mount in, a Launcher-applied artifact out.

The pieces:

- **Endpoint — a dedicated bare accumulation repo.** Code accumulates in
  `.spindrift/accum.git` by default under the launcher's working directory
  (`CODE_FORGE_ACCUMULATION_REPO_DIR` overrides it), a bare repo the Launcher
  owns, the code-plane sibling of `.spindrift/issues/`. The Launcher
  auto-creates it and seeds its base host-side from the operator's local
  checkout (offline) before any Box runs, idempotently on every run
  thereafter — no operator setup step required (issue #1726). It is *not* the
  operator's working repo: a bare repo has no checked-out branch (no
  `receive.denyCurrentBranch` foot-gun), keeps agent refs and objects out of
  the operator's `git branch`/gc, is isolated from the operator's live
  branch-juggling, and resets with `rm -rf`.

- **Code-in — a read-only clone mount.** The Launcher RO bind-mounts
  `.spindrift/accum.git` into the Box; the agent runs `git clone /repo /work`
  and works in the tmpfs work dir. The mount is read-only, so the operator's
  code stays single-writer (the Launcher), exactly as ADR 0032 keeps the issue
  file single-writer.

- **Code-out — a bundle through a writable outbox.** The Box cannot push to a
  read-only mount, so it emits its branch as a `git bundle` of
  `<base>..<agent-branch>` written to a small, **empty-at-start, throwaway
  writable outbox mount** (the code-plane analog of ADR 0032's stdout comment
  block). The Launcher relays it host-side —
  `git -C .spindrift/accum.git fetch <outbox>/seam.bundle <branch>`.

- **Landing — synchronous, host-side, onto an integration branch.** The Launcher
  merges the fetched branch onto **`integration/<parent>`** (one integration
  branch per broad ticket, keyed on *that seam's own* local issue's `parent`
  frontmatter — never a knob shared across the whole run, so a mixed-parent
  batch lands each seam onto its own branch instead of collapsing onto one),
  reusing the `git` adapter's temp-clone → merge → push-ref helper. `parent`
  is opaque and operator-authored (a GitHub URL, a Jira key, another local
  issue's slug); spindrift never reaches into another tracker to resolve it,
  it only sanitizes the string into a git-ref-safe token (lowercased, each run
  of non-`[a-z0-9]` characters collapsed to a single dash, leading/trailing
  dashes trimmed) before forming the branch name. An issue with no `parent:`
  set is its own broad ticket, keyed
  on its own sanitized slug instead — never a shared fallback branch. The
  merge *succeeding* **is** the landing — there is no PR, no network, no
  wait. A clean merge closes the seam-issue through the existing `reconcile`
  path (ADR 0029); a conflicting merge leaves the seam unlanded and blocked (the
  same failure posture ADR 0032 gives a missing/malformed comment block).
  `landing:` records the integration ref + commit sha — the ADR 0029 landing
  field generalizing to an immutable ref, as it already does for push-only `git`.

- **Chaining — the dependency graph, not a mode.** How seams compose is *not* a
  new knob; it falls out of the `## Blocked by` graph the operator already
  authors (`forge/blockers.go`), driven by the existing `waves` scheduler:
  independent seams fan out in parallel up to `MaxJobs`, dependent seams wait for
  their blockers, and all converge on the one integration branch. The decisive
  simplification: for `local`, **"blocker met" = the blocker seam is closed on
  disk** — a local frontmatter fact, not a remote PR query — so the whole DAG
  schedules **fully offline**. Sequential-where-dependent and
  parallel-where-independent are the same mechanism, selected per seam by its
  edges.

- **Surface — auto-fetch into the checkout, publish stays manual.** Once a
  broad ticket's seams are all landed and closed, the Launcher auto-surfaces
  `integration/<parent>`'s current tip into the operator's primary checkout as
  a local branch named after the ticket (issue #1730) — a host-side `git
  fetch` from `.spindrift/accum.git` into `pwd` that creates or fast-forwards
  only that branch ref, never switches the operator's checked-out branch, and
  makes no `origin` push. Nothing is surfaced for an incomplete ticket, an
  already-surfaced unchanged one is a no-op, and a target branch name the
  operator currently has checked out is left alone rather than clobbered. A
  one-line notice tells the operator it happened, so an assembled branch
  appearing is discoverable, not silent magic. Spindrift's responsibility still
  ends short of the shared remote: the operator surfaces the single team PR
  with the git/gh gestures they already know (`git push origin <branch>` →
  `gh pr create`). Keeping the local→shared *publish* transition a human act is
  the right trust boundary: it is the operator who decides when private
  breakdown work becomes public, the same caution ADR 0029 applied to leaking
  private local content into a shared remote.

- **Internal structure — a shared `git` substrate.** `local` and the existing
  `git` (remote push) forge share the `forge/git` package — branch naming and
  the landing helper are reused; only the code-out channel (RO mount, outbox,
  bundle relay) is `local`-specific. The public knob value stays `local` (parallel
  to `ISSUE_TRACKER=local`), *not* a `git_local`/`git_remote` rename that would
  break a shipped `CODE_FORGE` value (ADR 0010) and put a reachability flag in
  the operator's face where the shared substrate belongs in the code.

## The guarantee is "no forge/tracker network," not "no network"

The Box always keeps its network namespace, because the pluggable agent driver
(ADR 0009) reaches the model API over the network — that traffic is inherent to
running any agent and is unrelated to the loop. What `local × local` delivers is
that **the work planes make zero forge/tracker network calls**: no clone, no
push, no `gh`, no PR polling; issues and code move entirely on disk. A true
hard air-gap (a Box with no network at all) would require a local model driver
and is out of scope; noted here so it is not silently assumed. We do not add an
egress firewall — allow-listing only the API host is brittle machinery the
motivating use cases (offline convenience, self-hosting, private breakdown) do
not need.

## bwrap is the least-capable runner and sets the ceiling

As with ADR 0032, bubblewrap (ADR 0006) constrains the design:

- **Read-only code-in mount** binds like `/nix/store` and ADR 0032's `/issues`
  (`--ro-bind` under `--unshare-user --uid 1000`), no new mechanism.
- **The writable outbox is the reason code-out is a bundle, not a copy-out.**
  bwrap's root is a tmpfs on an ephemeral child that vanishes on exit, so the
  only way to get bytes out is a writable host bind — which is *not* a new
  capability: `buildMountSpecs` already produces one writable mount on both
  runners (the driver cache, #427: bwrap `--bind`, OCI no `:ro`). The outbox is
  a second instance of that same mechanism, empty-at-start and throwaway.

## Security

The read-only clone mount and the writable outbox both clear the bar, and
neither touches the invariant ADR 0032 protected:

- **The operator's code stays single-writer.** The repo mount is read-only; the
  Launcher is the only writer of `.spindrift/accum.git`. The Box writes only its
  own throwaway outbox — never the accumulation repo, never the operator's
  working checkout.
- **The new isolation statement is narrow.** ADR 0032 already documented one RO
  exception (the issues dir). This adds a second RO exception (the bare repo)
  plus one writable *scratch* mount. Neither is a writable mount of the
  operator's repo — the breach 0013's cut and 0032's rejected read-write option
  both feared. The honest new claim is "a read-only clone mount of
  `.spindrift/accum.git` and a writable throwaway outbox exist under
  `CODE_FORGE=local`."
- **A buggy or hostile Box cannot corrupt the endpoint.** Read-only enforces it
  structurally; a conflicting or malformed bundle fails the host-side merge and
  blocks the seam rather than damaging the accumulation repo.

## Considered Options

- **Read-write mount; the Box pushes directly** (i.e. reuse the `git` adapter
  with `CODE_FORGE_REMOTE_URL=/repo` and a writable bind). Near-zero new code —
  but it is exactly the read-write mount ADR 0032 rejected: two writers on the
  endpoint, and a buggy Box can corrupt refs. Rejected in favor of read-only +
  Launcher-landed, keeping the single-writer property.
- **Bundle out over stdout** (mirroring ADR 0032's comment block, no new mount).
  Consistent with 0032 and adds no writable bind — but the stdout channel is
  line-oriented with a 4 MiB per-line cap and `logscan.SkipOversized`
  *silently discards* oversized lines. For a comment that is fine; for the
  seam's actual code — the primary deliverable — a bundle over 4 MiB would be
  silently dropped and the seam would look landed-but-empty. Rejected: silent
  truncation of the deliverable is worse than a documented scratch mount.
- **A `git_local`/`git_remote` value rename** to make the reachability split
  visible in the knob. Rejected: breaks the shipped `git` value (ADR 0010) and
  puts a mechanism flag in the operator surface; the shared-substrate split
  belongs in the code, and `local` (parallel to `ISSUE_TRACKER=local`) is the
  right public name.
- **A `spindrift finalize <parent>` verb** that pushes the integration branch
  and opens the PR. Deferred, not rejected: convenient, but it bakes remote/PR
  assumptions into a loop that otherwise avoids them, and the manual gesture is
  the correct trust boundary for now. Revisit as the full tracker/forge matrix
  is built out.
- **A global "chain vs independent vs manual" merge mode.** Rejected: the
  `## Blocked by` graph already expresses exactly this, per seam, and the
  `waves` scheduler already honors it — a global mode would be a second, coarser
  encoding of information the operator already provides.
- **Drop the Box network namespace for a hard air-gap.** Rejected for this ADR:
  the agent driver needs the model API, so a networkless Box needs a local model
  driver — a separate concern.

## Consequences

- **ADR 0013's "a git remote is a hard requirement" is amended**: `local` is the
  documented exception — a bare accumulation repo mounted read-only, landed
  host-side — not a remote. The glossary's Code Forge entry is updated
  accordingly.
- `CODE_FORGE=local` is an additive knob value (MINOR under ADR 0010).
- `buildMountSpecs` (the Launcher computes specs; runners stay backend-agnostic)
  gains, gated on `CODE_FORGE=local` and `candidateMount`, a read-only mount of
  `.spindrift/accum.git` and a writable outbox mount. The zero-shared-host-filesystem
  claim now carries a third documented exception alongside ADR 0032's `/issues`.
- A new `git bundle` code-out grammar and a host-side relay join ADR 0032's
  comment-block extractor; `settle` wires the fetched branch to a host-side
  merge onto `integration/<parent>`, and a missing/malformed/conflicting bundle
  is treated as blocked.
- `reconcile` (ADR 0029) closes a `local`-code seam by observing a *local* merge
  onto its integration branch, not a remote PR; the `waves` blocker resolution
  reads the on-disk closed state for `local`, so dependency scheduling needs no
  network.
- Each seam's integration branch is keyed on *its own* local issue's `parent`
  field (issue #1734), so `CODE_FORGE=local`'s per-ticket accumulation
  **assumes a tracker that supplies a parent/epic link** — but never
  degrades to one shared branch: an issue with no `parent:` set is its own
  broad ticket, keyed on its own slug. `local × local` is the fully-specified
  cell; other trackers paired with `CODE_FORGE=local` stay permitted (ADR
  0013's matrix is unchanged) but are unspecified until their sub-issue links
  land.
- The Launcher's per-run wiring resolves every seam's parent independently —
  a mixed-parent dispatch batch lands, chains (`BASE_BRANCH` forwarding), and
  auto-surfaces (see below) each broad ticket on its own Integration branch; a
  cross-parent `## Blocked by` edge only orders scheduling and never folds one
  parent's work into another's base.
- The Launcher auto-surfaces a completed broad ticket's integration branch
  into the operator's checkout as a local branch (issue #1730); the operator
  still publishes the team PR manually. A `finalize` verb (push + PR) and a
  hard-air-gap local-model driver are noted futures.
