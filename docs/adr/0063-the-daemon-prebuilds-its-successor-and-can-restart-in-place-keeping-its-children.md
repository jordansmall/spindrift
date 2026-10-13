# The Daemon prebuilds its successor and can restart in place, keeping its children

Status: proposed

## Context

The Daemon halts self-changed when its own build moves at the fetched tip
(issue #3543). [ADR 0051](0051-the-driving-loop-is-a-shipped-app-above-the-invocation-boundary.md)
gave every halt one meaning: "stop filling slots, let running children drain,
exit." Bringing the Daemon back after that exit (10) is the job of a supervisor.
The Self-change halt reference documents that choice: the Daemon "never re-execs
itself" because "a freshly merged but broken daemon must not auto-load with
nobody awake".

On spindrift's own dogfood the build moves about hourly: 46 self-change halts
between 2026-10-07T13:02Z and 2026-10-09T22:11Z. Each drain lasts as long as the
longest Box in flight, a mean of 22 minutes and a maximum of 80. Every slot that
frees up meanwhile sits idle. Read from the Events file, that idle drain cost up
to ~29 slot-hours over the window. The restart that follows cost ~3 slot-hours:
a mean of 61 s with every slot dark, most of it building the new daemon.

The drain buys nothing. ADR 0051 already says the Daemon "holds nothing, so it
has nothing to drain": a child is a rev-pinned launcher invocation that owns its
Box, settle, CI wait and merge, so it runs fresh code by construction. Only the
Daemon's own supervision is stale, and that same stale Daemon supervises the
draining children for the whole drain anyway.

The restart also leans on systemd. A Daemon started by hand simply stops at
exit 10, and spindrift should not assume any particular supervisor.

## Decision

**The Daemon builds its successor before any self-change halt.** When the
evaluated self path differs from the running program, the Daemon realises it,
GC-roots it in the host state directory, and smoke-tests it with a check-only run
that parses its input and validates its config without taking the instance lock
or touching a tracker. The pool keeps filling slots throughout. A failure at any
of those steps does not halt the Daemon. It keeps running the build it has,
reports the blocked self-update with its revision and reason, and retries only
when the tip moves again. This holds in every mode. It answers the "broken
daemon" concern for whatever is merged, and it moves the build out of the
restart gap.

**The default is unchanged: a self-change drains and exits 10.** Restart in
place is opt-in, through a daemon flag (`--restart-in-place`). That flag is how
the documented systemd unit launches the Daemon. The mechanism itself assumes no
supervisor and behaves the same in a terminal.

**In place means `exec` in the same process, not detaching children.** With the
successor built and smoke-tested, the pool stops starting children and waits
for a clean moment: the discovery baton is free, so no child sits between
selecting an issue and claiming it, and no stop or abort has been requested. It
then:

- sets SIGTERM, SIGINT and SIGHUP to ignored, which survives the `exec`;
- clears close-on-exec only on the descriptors the handoff names: the instance
  lock, each child's report-pipe read end, and the handoff itself;
- `exec`s the successor's exact store path, which must be under the Nix store
  and equal to the self path evaluated at the fetched base-branch tip.

Because `exec` keeps the pid, every running child stays a child of that pid. A
child that exits during the window stays a zombie until the new image collects
its exit status, so its pid can never be recycled. The instance `flock` is never
released, so no second Daemon can start inside the window. A stop signal
landing inside the window is dropped, not held for the new image: Go's runtime
unblocks those three signals before any handler exists, so a pending one would
kill the new image on the default action. The new image latches SIGTERM and
SIGINT from the start of its `main` and acts on them only after it has
re-attached. In the instant between Go installing its own handlers and `main`
starting, a SIGTERM still kills the new image; Go offers no earlier hook.

**The handoff is a versioned document on an inherited descriptor**, named by
argv. It is never an environment variable, which every later child would
inherit, and never a file on disk, which another process could plant. Per busy
slot it carries the slot number, kind, revision, child start time, flight key,
issues, pass, model, chore, pid, report descriptor, and any partially read
report line. Pool-wide it carries the per-kind schedule and the breaker window.

**The new image re-attaches before it does anything that can fail.** Its first
act, before reading its input document, preflight or config validation, is to:

1. take over the lock;
2. validate each pid as a waitable child and each descriptor as a pipe;
3. re-mark them close-on-exec;
4. rebuild child handles;
5. restore SIGHUP's default, unless the predecessor already had it ignored. The
   stop latch for SIGTERM and SIGINT is installed before step 1.

Anything that fails validation is dropped and reported, never inherited. From then
on, own and inherited children share one supervision path: records,
`child_finish`, outcome interpretation, backoff, breaker, kind gating and halt.
A new image whose preflight then refuses to start `exec`s back to the previous,
still-rooted build with the same handoff, and that revision is not retried until
the tip moves. Only if no rollback is possible does it drain what it inherited
and exit 11.

**A child's output goes straight to its Child log file.** The Daemon opens the
file and hands it to the child as stdout and stderr, instead of copying output
through a pipe it must keep reading. A child can then neither stall on a full
pipe during the window nor die writing to a pipe whose reader is gone. That
second failure is real today: a SIGKILLed Daemon takes its children down the
first time they write. Child output no longer echoes onto the Daemon's own
stderr; the Child log and the Dashboard remain its home.

The in-place restart is a general handoff with self-change as its first
trigger. An in-place configuration reload is the intended second trigger and
is not decided here.

## Considered Options

- **Exit at once and adopt orphaned children from disk.** Each child would hold
  a `flock` lease, write its records and final exit code to a per-child file
  instead of the report pipe, and run in its own scope so a supervisor's cgroup
  kill would miss it. A new Daemon would sweep the leases. Rejected as the
  mechanism: it replaces ADR 0053's pipe with tailed files, needs an exit-code
  record because a non-parent cannot collect an exit status, and needs systemd
  `KillMode` changes to keep children alive. Its one advantage, adopting
  children after a Daemon *crash*, is kept as a possible follow-on rather than
  paid for on every restart.
- **Park the pipes across the restart** in systemd's file-descriptor store or a
  long-lived holder process. Rejected: it binds the design to systemd or adds a
  process, it still needs an on-disk exit record, and a pipe nobody reads fills
  up and stalls its child if no successor ever starts.
- **Terminate children on self-change, then restart.** Rejected: it discards
  Box work already paid for, up to an hour per restart at an hourly cadence.
  Terminating children remains the operator's second stop signal.
- **Make in-place the default.** Rejected: a plain start keeps today's
  semantics, and running main unattended in place is a choice an operator makes
  explicitly.
- **Keep scheduling for a bounded window before halting.** Rejected as a
  partial measure that still drains at the deadline.

## Consequences

- **ADR 0051 is amended.** "Halting means … let running children drain" no
  longer covers a self-change under `--restart-in-place`, and the Daemon now
  re-execs itself in that mode. The prebuild, smoke test and rollback stand in
  for the "broken daemon must not auto-load" guard.
- **ADR 0053 is amended twice.** First, the report pipe's read end survives an
  `exec`. Second, its premise that "a child is never newer than the daemon that
  started it" leaves an inherited child *older* than the image reading it. So the
  record vocabulary must stay readable by a newer image for any child that image
  can inherit, and an unrecognised record type stays ignored.
- **Only the restart gap is irreversible.** It is the `exec` itself, the wrapper,
  Go runtime start and re-attach, with nothing on the network or in the build.
  Every earlier failure leaves the old image untouched.
- **The pid and slot numbers never change across an in-place restart.** The
  status file's pid, the lock-is-liveness rule
  ([ADR 0051](0051-the-driving-loop-is-a-shipped-app-above-the-invocation-boundary.md)),
  and the Dashboard's one-pid, slot-plus-start ownership
  ([ADR 0060](0060-the-dashboard-is-a-read-only-process-over-the-daemons-published-files.md))
  all hold unchanged.
- **The Daemon now keeps GC roots for at most its running build and one
  predecessor.** That is a small, bounded footprint, unlike the generations
  ADR 0043 accumulated.
- **The documented systemd unit runs `--restart-in-place`**, and its restart
  policy now only matters for crashes.
- Spec: issue #4986.

## Amendment (issue #5016): the exec window ignores stop signals instead of blocking them

The first draft blocked SIGTERM, SIGINT and SIGHUP across the `exec` so a signal
would stay pending for the new image. That cannot work: Go's runtime unblocks
those signals at startup, before any handler is installed, so a signal pending
at `execve` kills the new image on the default action. The old image now sets
the three to ignored (`SIG_IGN` survives `exec`), so one landing inside the
window is dropped. The new image installs its stop latch before it re-attaches
and restores SIGHUP's default afterwards unless the handoff says it was
ignored. The new image, not the old, emits the `handoff` `self_update` event, so
a failed `exec` leaves none.

## Amendment (issue #5017): rollback marks the handoff, starts blocked, and roots two builds

The decision text says a refused new image `exec`s back. Four details it left
open:

- **The handoff document is version 3.** A rollback is the same document with
  `rolled_back` and `reason` set, sent in the reverse direction. Readers accept
  versions 1 to 3, but decoding is strict, so a build that predates a field
  would refuse a document carrying it, and the children it was handed would be
  orphaned. Amended by issue #5160: version 3 added the slot's CI-wait
  fields, raising the rollback floor from version 2 to 3, so an image resumed
  from a version-1 or version-2 writer does not roll back; it drains and exits
  as before.
- **The rolled-back build starts blocked at the refused revision.** Without
  that, its next fetch would see the same tip, rebuild the same refused path,
  and restart in place into it again. It emits `self_update` phase
  `rolled-back` (revision, reason, both paths, slots) rather than `handoff`,
  and never rolls back itself, so two refusing builds cannot pass the children
  back and forth.
- **Config validation refusals roll back too**, not only a failed startup
  preflight. Both fail after the children are re-attached and before the pool
  runs, so they are the same case. (The issue #5039 amendment below widens
  this to Loop's pool-config and handoff-fit checks.)
- **Two fixed GC roots in the checkout's git dir.** `spindrift-daemon.current`
  roots the running build and `spindrift-daemon.next` the prebuilt successor.
  An image whose startup succeeds roots itself as `.current` and deletes `.next`.
  Between a handoff and that point `.current` is the predecessor, the rollback
  target, so the build rolled back to cannot be collected. A forward `exec` is
  refused unless `.current` holds the running build. This is the "running build
  and one predecessor" bound in the consequences.

## Amendment (issue #5039): handoff-fit refusals roll back, and exit 1 without one

"Config validation refusals roll back too" held only for the checks the new
image ran itself. The pool's own config check, which includes whether the
handoff fits the new config (each handed-off slot inside `MAX_PARALLEL`, each
handed-off kind still configured), failed inside the pool's startup, after the
halt was emitted, and drained the children instead of rolling back. The new
image now runs that same check after its preflight and before it roots itself,
so a handoff the new build's config cannot take is handed back like any other
refusal and leaves no halt event behind. Validation is not deferred until the
pool has taken the children over, since that would supervise slots or kinds the
new config cannot represent.

With no rollback possible, the image halts `config-invalid` and drains what it
inherited, holding the lock until the children exit. It exits 1, the config
error code. The decision text's "exit 11" is the preflight refusal's code only.
