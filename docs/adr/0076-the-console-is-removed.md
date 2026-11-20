# The Console is removed

Status: accepted

## Context

The Console, `spindrift console`, is the interactive picks-only driving loop
of ADR 0023, refined by ADR 0025's multi-region layout, ADR 0030's
section-switched list with a live-tail sidebar, and ADR 0031's terminal-palette
styling. It lets an operator pick issues in a terminal UI and watch each
Dispatch run.

It is rarely used, and it is one of the noisiest parts of the tree to
maintain. It embeds its own in-process dispatch engine behind a Bubble Tea UI:
a session queue, a drain loop over the continuous engine, settle, and the
Terminate gesture with its Reclaim. Its teatest suite races that engine's
background goroutines, and the flakes have not stopped. At least ten de-flake
issues have been filed against it (#1109, #1327, #1663, #1981, #2013, #2014,
#3118 and #7046 among them), and a fresh one failed the x86_64-linux leg of the
release PR on 2026-11-20: quitting closes the queue, which can push an empty
snapshot into the model before the program exits.

The cost is not confined to the Console's own package. Engine packages carry
seams whose only reason to exist is the Console: live parallelism resizing,
startup orphan detection, a reclaim mode for the Terminate gesture, an output
override on the factory's heartbeat, a queue adapter backed by the discoverer,
and image-build report state in the runners. Every change to the engine has
to keep those seams working for a surface almost nobody drives.

What the Console offered now has other homes. Tracker labels triage work;
`spindrift dispatch`, or `ISSUE_NUMBER`, launches one issue; the daemon keeps
a refill pool of Dispatches running; the Dashboard (ADR 0060) shows what is
running without driving it.

## Decision

Delete the Console outright.

1. **`spindrift console` no longer exists.** It is an unknown subcommand, and
   the launcher exits 1 with its usage text. The Console package, its docs
   page and its tests are deleted. MIGRATING.md carries the entry for
   operators who used it.
2. **Its replacements are the existing surfaces.** Use tracker labels for
   triage, `spindrift dispatch` or `ISSUE_NUMBER` to launch one issue, the
   daemon for a refill pool, and the Dashboard for observation.
   `CONTINUOUS_DISPATCH` stays for operators who want a refill loop without a
   daemon.
3. **The terminal-UI dependencies leave the launcher module:** Bubble Tea,
   lipgloss, teatest, and Charm's ansi, cellbuf and golden packages. Charm's
   x/term stays, because quickstart and the masked flag input use it.
4. **The engine seams only the Console used are pruned by follow-up
   deletions:** live parallelism resizing, startup orphan detection, the
   Terminate-gesture reclaim mode, the factory's heartbeat-output override, the
   discoverer-backed queue adapter, and the runners' image-build report state.
   Whether a seam is Console-only is decided by its callers, not by what a
   comment says about it, so each deletion starts from a caller search.

## Considered Options

- **Keep the Console and keep fixing its flakes.** Rejected: ten de-flake
  issues have not ended the races, because the races are the in-process
  engine's background goroutines against a UI that must quit cleanly, and the
  Console is too little used to justify that standing cost.
- **Externalize the Console as a client of the daemon.** This would keep the
  picks UI and drop the embedded engine. Rejected: it needs a daemon control
  channel, to scale slots and to stop one Dispatch, plus new decisions about
  what a Pick means against the daemon's label-driven claiming. That is a
  large design for a rarely used surface. A daemon control channel remains a
  separate idea, to be judged on its own merits.

## Consequences

- This ADR supersedes ADR 0023, ADR 0025, ADR 0030 and ADR 0031. Each carries
  a banner pointing here. Passing mentions of the Console in other ADRs, such
  as ADR 0024's Terminate, stay as history.
- Operators who drove the Console move to labels, `spindrift dispatch` and the
  daemon. Anything that needs interactive picking now needs the daemon control
  channel that this ADR leaves unbuilt.
- The launcher module sheds its terminal-UI dependencies and its teatest
  suite, and with it the largest source of flaky tests in the launcher.
- The seam pruning in point 4 is separate work, so the engine keeps the
  Console-only seams until those deletions land.

Issue #7306, specified in #7305.
