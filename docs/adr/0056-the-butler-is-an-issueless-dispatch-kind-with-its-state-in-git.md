# The butler is an issue-less Dispatch kind whose state lives in a git ledger

Status: proposed

## Context

Every Dispatch today starts from an issue a human labelled. Nothing in
spindrift works the codebase on its own initiative: there is no scheduled
workflow, no dependency tooling, and the Daemon ([ADR
0051](0051-the-driving-loop-is-a-shipped-app-above-the-invocation-boundary.md)) only
re-polls label queues. We want a slow, unattended loop that keeps a Target
repo tidy — finding bugs, cheap refactors, and documentation drift — on
slots the Daemon would otherwise leave idle.

Three things in the existing design resist that:

- A Box needs an issue. Even self-contained research (ADR 0022 amendment)
  opens with "Research GitHub issue #N", and `buildBoxEnv` always sets
  `ISSUE_NUMBER`.
- Research's verdict vocabulary does not fit a standing scan. A sweep that
  finds nothing would settle `reject`, and `agent-research-reject` closes
  its issue (`agent-research-close.yml`).
- Kinds are string switches in about eight places (daemon `kind.go`,
  `main.go`, prompt assembly, `lib/labels.nix`, the CI workflows). Spec
  #2251 deferred a kind descriptor with the reopen trigger "a third
  Dispatch kind is proposed".

## Decision

**The butler is a third Dispatch kind, `butler`, keyed by chore rather than
by issue.** A **chore** is one standing topic of upkeep (`bugs`,
`refactor`, `docs-drift`). A butler Dispatch is one read-only Box that scans
one chore and relays findings to the Filer; it never pushes code and
never touches the Code Forge. The kind lands on a kind descriptor that
dispatch and research move onto first, honouring #2251's trigger.

**The Daemon drives it without a timer.** Each slot still runs one pinned
child invocation (`<kind> --max-jobs 1`). For `butler`, the child's
discovery is a due check: a chore is due when its interval has elapsed,
today's budgets allow another run, and there is something to scan. Not due
exits as "no work", and the Daemon's existing idle backoff does the waiting.
A slot picks `butler` only when both the dispatch and research queues report
no work; a butler Box already running when work arrives finishes (the
awake-window rule: gate starting, never stopping).

**State lives in the Target repo as a ledger.** Each chore has one ref,
`refs/spindrift/butler/<chore>`, pointing at a chain of small commits kept
off every branch. Each commit carries `state.json`: the main commit swept up
to (`lastSwept`), the rotating tree cursor, the issues filed, and the run's
token usage. `git log` on the ref is the chore's history.

- **Scope.** A run scans `lastSwept..HEAD` plus the next slice of the tree
  at the cursor, so new code is checked promptly and old code eventually,
  and no run reads the whole repo.
- **Claim.** Before starting a Box the host pushes a claim commit with
  `--force-with-lease` against the ledger tip it read. The loser of a race
  reports "no work". Settle pushes the finished state over the claim. A
  claim older than a timeout is abandoned and may be taken over. This holds
  across two slots, two Daemons, or a Daemon and a manual run.
- **Budgets.** Start gates only, since a Box cannot be stopped cleanly
  mid-run: per-chore interval, sweeps per day, findings per day, findings
  per sweep, promotions per day, plus a daily token ceiling as a backstop.
  Totals are computed by walking today's ledger commits; there is no second
  store.
- **Local forge.** Under `CODE_FORGE=local` the ledger refs live in the
  bare Accumulation repo and are written with `update-ref`, as the land
  path already does.
- **Hosted forges.** Under `github` and `forgejo` the host fetches the
  chore's ref into a scratch clone, builds the state commit there, and
  pushes it with the launcher's own credential. The ledger has its own
  refspec: it never goes through the bundle relay and never touches
  `refs/heads/`.

**Findings may be auto-promoted, host-decided, behind a separate opt-in.**
This is a deliberate exception to the rule that an agent-filed issue is
never dispatchable by the agent's own hand. A finding is filed already
labelled `ready-for-agent` only when all three hold:

1. its class is on the host-side allow-list for that chore (Consumer
   config; the Box cannot change it),
2. host-checkable limits hold (files touched, and so on), and
3. an in-Box reviewer, run only on candidates in allow-listed classes,
   concurred.

The allow-list is the trust gate; the reviewer is a quality filter only,
because it reads the same repo content as the scanner and cannot be a trust
boundary. Promotion is off until a Consumer sets `maxPromotionsPerDay > 0`;
enabling a chore alone only files findings. Every other finding is filed
unlabelled with `agent-butler-finding` for a human to triage.

**Host dedup covers closed findings, for every finding kind.** Rotation
guarantees the butler re-finds old problems. Settle skips a finding whose
site key matches an open *or closed* finding issue — butler, research, or
review — so a human's close is honoured by the host, not only by the
Filer's prompt.

**Chores are opt-in from a built-in catalog.** spindrift ships `bugs`,
`refactor`, and `docs-drift` prompts with default class allow-lists; a
Consumer enables chores by name and may add its own chore prompts. Nothing
sweeps on upgrade.

## Considered options

- **Reuse research on standing "charter" issues, re-triggered on a
  cadence.** No new kind, but the verdict vocabulary misfits (a quiet sweep
  closes its own charter) and the research prompt judges an issue's
  relevance rather than scanning. Rejected.
- **A timer inside the Daemon.** Would put scheduling above the invocation
  boundary ADR 0051 draws; the due check in the child keeps the Daemon a
  pure supervisor and lets a manual `spindrift butler` behave identically.
- **State in a standing tracker issue or a host state directory.** An issue
  marker is visible but is a tracker write per run and mixes state into
  injectable text; a host directory is invisible and per-machine, so two
  Daemons could not share a claim. A git ref gives an atomic
  compare-and-swap, history for free, and parity across forges.
- **Ref pointing at the main commit, cursor in a git note.** Two refs per
  update, not atomic. Rejected for the single-ref state commit.
- **Box-rated "trivial" tier as the promotion gate.** The rating is model
  output shaped by repo content. Rejected for the host allow-list.
- **Writable Box that opens PRs for cheap fixes.** Mixes research-shaped
  and work-shaped settle in one run and raises the Box's trust bar. The
  work kind already does fixes; the butler only decides what is worth one.
- **A dependency-update chore.** Detecting updates is deterministic and
  solved by Renovate, Dependabot, or a scheduled `nix flake update`. Bump
  PRs go through the normal flow; a failing one becomes an ordinary work
  issue.

## Consequences

- A Dispatch is no longer always per-issue: a butler Dispatch is per-chore,
  and its identity is the chore's ledger ref, not an issue number.
- The host gains a new write to the Target repo: custom refs outside
  `refs/heads/`. Every push path today hard-codes `refs/heads/<name>`; the
  ledger needs its own refspec and must not route through the bundle relay.
- The finding-label invariant in CLAUDE.md's triage lifecycle ("never
  carries a dispatch label") gains a documented, opt-in exception for
  `agent-butler-finding`.
- "butler", "chore", and "ledger" are working names; "chore" collides with
  the Conventional Commits type and is expected to be renamed.
