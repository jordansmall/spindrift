# The Dashboard is a read-only process over the Daemon's published files

Status: proposed

## Context

An operator running the [Daemon](../../CONTEXT.md) unattended today learns
what it is doing by SSHing to its host and reading the journal and
`.spindrift/logs/` by hand. The [Console](../../CONTEXT.md) cannot help: it
drives its own Picked Dispatches and never reads the Daemon's state, and
[ADR 0023](0023-console-is-a-picks-only-driving-loop.md)
rejected a detach/reattach split for it.

Most of what a viewer needs is already published, but not all of it:

- **The present is on disk.** The status file (`.git/spindrift-daemon.status`)
  is rewritten atomically on every state change: pool state and reason,
  each slot's phase, kind, revision and Dispatch, per-kind next check.
- **The history is not.** The JSON-lines event stream goes to stdout only.
  Under the documented systemd unit that means the journal; under a
  foreground `nix run .#daemon` it means a terminal, gone unless redirected;
  on `aarch64-darwin` there is no journal at all.
- **A child's own output is not.** The launcher-side record of a Dispatch —
  build, claim, Box start, settle, CI polling, merge gate — reaches only the
  Daemon's stderr, and it is the only record of a Dispatch that failed
  outside its Box.
- **Pass log paths are derived, not published.** They are keyed by issue
  number in launcher internals, with nothing for a Chore.

Measured on the dogfood Daemon over 2026-09-29..10-06: about 13.4k events
(~2 MB) a week, against ~615 MB a week of child output, much of it the Box
stream the launcher also writes to the Pass log.

## Decision

Add a **Dashboard**: a read-only view of one Daemon, shipped as its own Go
binary and flake app (`apps.dashboard`) in this repo, never inside the Daemon
process. It runs in the foreground or under its own user unit, and stays up
— saying so — while the Daemon is down, halted, or restarting, which is when
it is most needed. It never picks, starts, stops, or settles anything.

The Dashboard reads **only what the Daemon publishes**, never Daemon or
launcher internals, so it can later move to its own repo without a rewrite.
The compiler enforces that, not a convention: the Dashboard is its own Go
module in its own top-level directory, standard library only, so it cannot
import the launcher's `internal/` packages. That also keeps it outside the
Daemon's source fileset, so a Dashboard commit never moves the Daemon's
store path and halts a running Daemon on self-change. Moving it out later is
the directory plus a flake of its own.

To make published files sufficient, the Daemon publishes three more things:

1. **An Events file** beside the status file — the same lines it writes to
   stdout, size-capped with one rotated generation, a fixed default with no
   knob. stdout is unchanged. The status file is the present; the Events
   file is the history.
2. **A Child log per child** — the child's stdout and stderr teed verbatim
   to a file under `.spindrift/logs/daemon/`, still forwarded to stderr as
   today. Not pruned for now; operators prune by hand.
3. **Events that name every log file.** `child_start` names the Child log;
   each `box` event names its Pass log. The Dashboard never derives a path,
   and it serves only files an event names — on an unauthenticated server
   that also rules out path traversal.

The status file gains a top-level `schema` and each event a `v`, bumped only
on a breaking change (added fields never bump it). The Dashboard and Daemon
restart independently and so can run different revisions; a Dashboard that
reads a version it does not know shows a banner and the raw JSON rather than
crashing or misreading.

The v1 page is a pool header, slot cards (idle slots included), a history
timeline, and a per-Dispatch drill-in following its Child log and Pass logs
live, with links out to the issue and PR. Logs show raw, ANSI colour
rendered; the Console's Activity-feed rendering is not in v1, since its
parser is per-Driver launcher code the Dashboard cannot import. It is plain
Go: HTML, CSS, and JS embedded with `go:embed`, live updates over
Server-Sent Events, files followed by polling, and no npm toolchain,
vendored library, or Go dependency. It binds loopback by default and takes
`--listen` to serve a LAN; it has no authentication yet, and says so on
start.

## Considered Options

- **Serve HTTP from the Daemon itself** — the least wiring, rejected: it
  puts a network listener in the unattended loop and takes the view down
  with the process whose failure it should explain.
- **Merge into the Console** — rejected: the Console drives, and teaching it
  to observe another driving loop reopens ADR 0023's detach decision.
- **Read history from the journal** (`journalctl --user -u …`) — no Daemon
  change, rejected: it works only under systemd and only for one unit name,
  leaving the foreground and macOS cases with no history at all.
- **Derive Pass log paths with `dispatch.LogPaths`** — no new event field,
  rejected: it imports launcher naming, has nothing for a Chore, and cannot
  tell which Dispatch a rotated-aside attempt belonged to.
- **A separate repo from the start** — rejected for now, after a second
  look. It would remove the self-halt hazard and keep UI churn out of
  spindrift's releases, but most of the work is Daemon-side and lands here
  regardless, the Dashboard couldn't be exercised until a release carried
  those fields, and it would need its own dispatch setup. A separate module
  in this repo gets the isolation without those costs.
- **A package in the launcher's module, reusing the Activity-feed parser**
  — rejected: it ties the Dashboard to launcher internals (making the move
  a rewrite), and it places the Dashboard inside the Daemon's source
  fileset, where every Dashboard commit would halt a running Daemon unless
  carved back out.
- **An SPA built with npm** — rejected: a JS toolchain, lockfile hash churn,
  and a second lint and test stack in a Go/Nix/bash repo for one read-only
  page; the flash is in the CSS, not the framework.

## Consequences

- The Events file, Child log naming, the log fields on `child_start` and
  `box`, and the schema versions become documented surfaces in
  `docs/reference.md`, changed under the bump rule above.
- Child logs grow without bound (~2.5 GB a month at the measured rate,
  largely duplicating Pass logs); Pass logs already do. Pruning both is a
  known gap, to be revisited.
- Deferred, each a later decision: stats over the Events file, a terminal
  rendering of the Dashboard, authentication, any steering action, and an
  Activity-feed view of Pass logs (which would need the Driver on `box`
  events and a parser the Dashboard can reach without importing the
  launcher).
