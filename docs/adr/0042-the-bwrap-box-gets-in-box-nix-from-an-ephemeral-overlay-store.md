# The bwrap Box gets in-box nix from an ephemeral overlay store

[ADR 0018](0018-writable-store-self-test-mode.md) closed the in-box
`nix flake check` gap for OCI runners and declared the bwrap runner out of
scope: bwrap "binds the host's `/nix/store` read-only directly into the
sandbox ... there is no writable container layer to isolate a self-test
write into," so "making the bwrap store writable would mean writing into
the *host's* real `/nix/store`." That objection was correct about the
mechanism available at the time and wrong about the conclusion. bubblewrap
0.9+ can stack an overlay whose upper layer is a tmpfs, which yields
exactly the ephemeral writable layer ADR 0018 said bwrap lacked — the host
store stays read-only underneath and untouched.

The gap is also larger than writability. A bwrap Box carries `nix` on PATH
(it is in `agentEnv` whenever `nixInBox` is on) but has no `/etc/nix/nix.conf`,
no `/nix/var`, and therefore no store database at all: those are baked by
`lib/image.nix` into an image the bwrap runner never builds. In-box nix
under bwrap is not merely read-only today, it is non-functional. This ADR
makes it work, and settles the surrounding runtime posture that a
first-class bwrap Box needs.

The decision set:

- **The writable store is a tmpfs overlay, never a host-store write.**
  `--overlay-src /nix/store --tmp-overlay /nix/store` presents the host's
  store as a read-only lower with an ephemeral tmpfs upper. New paths the
  Box substitutes or builds live in that upper and die with the sandbox.
  This preserves both properties ADR 0018 relied on — no shared mutable
  state, ephemeral by construction — so its rejection of a host-store bind
  mount stands unchanged; only its claim that bwrap has no alternative is
  superseded.

- **The store database is a VACUUMed snapshot of the host's, overlaid per
  Box.** `build` copies `/nix/var/nix/db/db.sqlite` once to an
  agent-owned, VACUUMed snapshot; each Box gets a tmpfs `/nix/var` with
  that snapshot as an overlay lower. Overlaying the host's `/nix/var`
  directly does not work and cannot be made to: `big-lock` is root-owned
  mode 0600 and `profiles/per-user` needs a chmod, and in bwrap's user
  namespace host root maps to an unmapped `nobody`, so both are EPERM for
  uid 1000. Seeding only the declared closure roots (the `closureInfo`
  registration `lib/image.nix` uses) was rejected for a different reason:
  the host's paths are physically present on the lower but would be
  DB-invalid, so nix would re-substitute from the network what is already
  on local disk — discarding the reuse that makes bwrap worth having.

- **The knobs mirror the OCI path exactly.** `nixInBox` gates the
  synthesized nix.conf and the DB snapshot over a read-only store —
  enough for `nix develop` and `nix eval` against host-present paths.
  `nixStoreWritable` adds the overlay on top. One mental model across both
  runtimes, and a Consumer's existing configuration means the same thing
  under either.

- **nix.conf, passwd, and group are single-sourced in nix.** The bwrap
  runner binds store paths produced by the nix layer rather than writing
  its own copies. This retires an existing duplication — the passwd/group
  content was spelled out both in `lib/image.nix` and as Go string
  constants in the bwrap adapter — instead of adding a third.

- **A bwrap Box gets its own network namespace by default.** bwrap alone
  shares the host's, so a Box can reach every service on the operator's
  loopback; rootless podman cannot. pasta closes this, but only with
  explicit flags — its defaults still splice guest loopback to the host's.
  The Box therefore runs under pasta with
  `-t none -T none -u none -U none --no-map-gw`, which is podman parity,
  and which finally makes the pre-existing `BWRAP_UNSHARE_NET` knob usable
  (on its own it leaves the Box with no DNS).

- **Resource containment is layered and best-effort, and never gates
  parallelism.** bwrap imposes no limits of its own, and `MEMORY_LIMIT`
  and the pids limit are silently inert under it today. Three portable
  layers replace that silence: nix.conf bounds `cores` (the dominant
  consumer — a Box's nix defaults to one job across all host cores);
  `prlimit --nproc` caps process count with no cgroup involvement; and
  where a writable delegated cgroup v2 subtree exists, the launcher
  creates a per-Box cgroup by writing the cgroup filesystem directly. That
  last layer deliberately does not shell out to `systemd-run` — the
  delegation is what is required, not the binary — and its absence warns
  rather than refuses. Concurrency is the operator's knob and is never
  reduced to compensate.

- **A bwrap Box is strictly ephemeral; nothing is retained on failure.**
  The OCI path keeps a failed container so a human can recover locally,
  but nothing in the launcher ever reads one — it is a manual affordance
  for a recovery path spindrift no longer depends on. The artifacts that
  matter already leave the Box by design: pass logs are host-side files
  under `.spindrift/logs/`, and under a read-only Box the Driver bundles
  its branch into the host-mounted `/outbox`, which the host-decided
  outcome (ADR 0039) already reads as evidence. Only work from a Box that
  died before bundling is lost, and that is replayable from the logs.

- **Missing prerequisites are tiered by consequence.** No pasta refuses to
  launch, because silently sharing the host's network namespace is the
  exposure this ADR closes. No unprivileged overlayfs refuses only when
  `nixStoreWritable` is set. No cgroup delegation warns and continues.
  `spindrift doctor` reports all three.

## Consequences

A bwrap Box's set of valid store paths is a function of the host's store
at snapshot time, so two operators' Boxes differ and neither matches a
built image. This is a real loss of reproducibility, but a smaller one
than it appears: the bwrap runner already binds the host's whole store
read-only, so the Box could always *read* those paths — the snapshot only
changes which of them nix trusts. Consumers who need the image's
reproducibility guarantee (ADR 0002) should use an OCI runtime, which
remains the default.

The snapshot is a point-in-time copy with no gcroot pinning it, so a host
`nix-store --gc` during a run can remove paths the Box's database still
believes are valid. This is accepted rather than solved; pinning would
make a Box hold host disk it should be free to release.

Unprivileged bwrap maps exactly one uid, so a Box writes through its
writable mounts as the operator's own uid — rootless podman would use a
subuid range instead. This is intrinsic to the mechanism, not a flag we
declined to set, and the mitigation is to keep writable mounts minimal.

## Amendment (issue #3049): cgroup v2 pids.max is the sole process-containment layer, prlimit --nproc is removed

The "Resource containment is layered and best-effort" decision above named
three layers, one of which was `prlimit --nproc` capping process count "with
no cgroup involvement." That layer is now removed from the bwrap exec chain
entirely. Process containment for bwrap Boxes is cgroup v2 `pids.max` alone,
applied when a writable delegated cgroup v2 subtree exists (via
`provisionCgroup`, unchanged), with the same warn-and-degrade posture as
before when delegation is unavailable — its absence still warns rather than
refuses. nix.conf's `cores` bound is untouched; this amendment concerns only
the process-count layer, not the CPU layer.

The reason is that `RLIMIT_NPROC` — what `prlimit --nproc` sets — is
enforced per-UID across the *entire host*, not scoped to the Box's own
subtree: it counts every task the invoking UID owns anywhere on the host,
including threads, ambient desktop and dev workload included. On a host
with roughly 1200 ambient tasks already open under the invoking user, the
default `PIDS_LIMIT=512` made the very first clone in the wrapped exec chain
fail with EAGAIN ("Failed to clone process with detached namespaces:
Resource temporarily unavailable"), killing every Box launch on that host.
The bug was latent rather than immediately obvious: it only became
reachable once PR #3047 (forward PATH through the prlimit wrapper) made the
wrapper actually functional — before that fix, the wrapper either errored
earlier or was silently skipped whenever `prlimit` wasn't resolvable on
PATH, so this failure mode had never been exercised end-to-end.

An adaptive rlimit — reading the invoking UID's current task count at
launch and setting the rlimit to that count plus `PIDS_LIMIT`, rather than a
fixed value — was considered and rejected. It is racy at launch, since the
ambient task count can shift between the read and the `setrlimit` call, so
it cannot guarantee the Box the headroom it asks for; and, more
fundamentally, `RLIMIT_NPROC` is a per-UID budget, so concurrent Boxes
launched under the same UID would share one adaptive budget rather than
each getting its own — no choice of number can make a per-UID limit deliver
per-Box semantics. cgroup v2 `pids.max`, scoped to the cgroup subtree
itself rather than to the UID, is what makes correct per-Box containment
possible at all. OCI is unaffected by any of this: `--pids-limit` under
podman/docker is already cgroup-backed, was never subject to this bug, and
`PIDS_LIMIT` keeps the same meaning there. `spindrift doctor`'s
`bwrap-cgroup-delegation` row stays Advisory — its tier is unchanged — but
its remedy text now describes cgroup delegation as the sole containment
mechanism for `PIDS_LIMIT` under bwrap, not one of two.

## Amendment (issue #3272): a limit-write failure degrades only that limit, not the whole cgroup

`provisionCgroup` previously treated a failed `pids.max`/`memory.max` write,
or a malformed `MEMORY_LIMIT`, the same as failing to create the cgroup
directory at all: it removed the just-created dir and reported no cgroup,
so the Box lost cgroup identity entirely — `IsRunning`/`ListRunning`/`Reap`,
the `ErrAlreadyRunning` collision guard, and Console's orphan detection all
stopped seeing it, on top of running that one limit uncapped. This
conflated two independent failures under one all-or-nothing response.

The real-world trigger is an ordinary systemd user session: the launcher's
own cgroup commonly has no controllers enabled in its
`cgroup.subtree_control`, so a delegated child subtree can still be created
(the directory write succeeds), but the `pids.max`/`memory.max` control
files either don't exist or reject a write with `permission denied` rather
than `no such file`. That is a missing controller, not a missing
delegation, and does not warrant discarding the cgroup itself.

`provisionCgroup` now keys success on directory creation alone. Once the
per-Box cgroup directory exists, it is kept regardless of what happens to
the individual limit writes: a failed `pids.max` write, a failed
`memory.max` write, or a malformed `MEMORY_LIMIT` each warn, name the
specific limit that is going uncapped, and continue — they no longer
remove the directory or short-circuit the other limit's write. `Run` moves
the Box's PID into `cgroup.procs` and later cleans the directory up
whenever the directory itself exists, independent of which limits landed.
Only a failure to find or create the directory in the first place still
degrades the box to no cgroup at all, per the original amendment above.

## Amendment (issue #3273): the per-Box cgroup anchors at the delegation root, not the launcher's own cgroup

`provisionCgroup` created the per-Box cgroup as a direct child of the
launcher's own cgroup. That location can never carry `pids.max`/
`memory.max`, for a structural reason rather than a permissions accident:
those control files only appear in a child once the parent enables the
corresponding controller in its own `cgroup.subtree_control`, and cgroup
v2 forbids enabling a controller on any cgroup that currently holds
processes — which the launcher's own cgroup always does, being the
launcher's own cgroup. So the requirement was unsatisfiable as written on
every host where the launcher runs as an ordinary process, and wrapping
the launcher in a delegated systemd scope does not help: the same
constraint just reappears one level down, at whatever cgroup now holds
the launcher.

This mattered more than it looked, because the #3049 amendment above made
cgroup v2 `pids.max` the *sole* process-containment layer for bwrap Boxes.
On a standard systemd user session, that meant bwrap Boxes ran with no
process containment at all — while `spindrift doctor`'s
`bwrap-cgroup-delegation` row reported the gap only as a permanently-failing
Advisory line, easy to read as a cosmetic warning rather than a total loss
of the containment layer that amendment made load-bearing.

The fix walks up from the launcher's own cgroup toward the cgroup root
and anchors the per-Box cgroup at the outermost ancestor this process can
still create a subdirectory in whose `cgroup.subtree_control` carries the
controllers the *configured* limits need — not both controllers
unconditionally, since the dogfood default disables `MEMORY_LIMIT` on
Linux, and demanding it anyway would reject a host that can enforce
`PIDS_LIMIT` perfectly well. Two explicit guards bound the walk to a
genuinely delegated subtree, rather than leaving it to be stopped
somewhere sane by an incidental permission failure. The root of the
unified hierarchy is never itself a selectable anchor: it is the init
system's tree top, never a delegation target. And if this process *can*
create a directory in that root, there is no delegation boundary anywhere
on the path — we are running as root, or on a hierarchy that is entirely
ours — so every candidate would pass the create probe on permission
alone; the walk resolves no anchor at all rather than picking one
arbitrarily high. An empty controller set resolves no anchor for a
related reason: no configured limit needs a controller, so no limit write
will ask for one and there is nothing to climb for. Neither guard fires
on an ordinary systemd user session, where the walk lands on the per-user
slice — which already enables the controllers and is owned by the
invoking user, while everything above it is root-owned, so no hardcoded
path or allowlist is needed to keep the climb from overshooting. This
reaffirms rather than revisits this ADR's existing choice to read and
write the cgroup filesystem directly instead of shelling out to systemd:
a walk needs no new dependency, and `systemd-run` would not work on a
non-systemd host either.

Where no ancestor qualifies — containers, non-systemd hosts, a
restrictive delegation — the anchor falls back to the launcher's own
cgroup, the pre-#3273 location, so the Box still gets cgroup identity and
tracking and only the individual limit writes degrade, per the #3272
amendment above. Warn-and-proceed is unchanged; nothing ever refuses to
launch over this.

Box discovery needed no change: `findCgroupDir` already walks the whole
cgroup filesystem for spindrift-owned directories instead of deriving a
path from the launcher's own cgroup, so a Box anchored above the
launcher's own session scope stays just as discoverable, and a leftover
cgroup from a crashed launcher stays just as reapable, without either
helper knowing where a given Box's anchor landed. This is also strictly
better than before: such a cgroup no longer disappears when the
launcher's own session scope goes away, since it now typically lives
above it.

`spindrift doctor`'s `bwrap-cgroup-delegation` probe asks the same
question a real launch asks, not a stronger one. It resolves *both* the
anchor and the controller files it checks for from the configured
controller set the runner itself resolves —
`CgroupControllers(memoryLimit, pidsLimit)`, handed to the same
`cgroupParentDir` seam `provisionCgroup` anchors through — instead of
probing a hardcoded memory-plus-pids pair. So the row it reports and the
containment a real launch gets can never disagree in either direction: on
a pids-only delegated host with `MEMORY_LIMIT` unset, doctor now reports
the row green exactly where the runner enforces `PIDS_LIMIT`, rather than
failing it over a `memory.max` no configured limit would ever write.

## Amendment (issue #2960): Reap no longer races a Box's own provisioning

`Run`'s cgroup provisioning is not one atomic step: `provisionCgroup`
creates the per-Box directory well before the PID that makes it "running"
is written. Between those two points sit a syscall-filter build, argument
assembly, and a blocking `flock` on the snapshot lock — not a short
window. For the whole span, the directory exists with an empty
`cgroup.procs`, which is exactly what `IsRunning` and `Reap` see for a
genuinely leftover, already-exited Box. Reap could not tell the two apart:
it read the directory, saw nothing running, and deleted it.

Deleting a mid-launch Box's cgroup directory out from under it is not
cosmetic. The Box keeps running with no `pids.max`/`memory.max`
enforcement for the rest of its life — the containment layer the #3049
amendment made load-bearing — and `IsRunning`/`ListRunning` report it as
never having run at all, for as long as it runs, since the directory they
would have found is gone.

The fix is an in-process provisioning guard on the adapter, not a change
to the directory or its contents. `bwrapAdapter` now refcounts box names
currently between `provisionCgroup` and the `cgroup.procs` write, in a
`provisioning map[string]int` guarded by the same `a.mu` that already
guards `running` — a refcount rather than a plain set so two concurrent
`Run` calls for the same name don't let the first's release unblock
`Reap` while the second is still provisioning. `Run` marks a name
provisioning immediately before `provisionCgroup` and clears it
immediately after the `cgroup.procs` write, via a deferred, idempotent
release so every early-return path in between — including a failed
`cmd.Start()` — still clears it. `Reap` checks this guard before its
`IsRunning` check and returns nil early for a name still provisioning,
leaving the directory untouched until the launch either completes or
fails on its own. `Reap` holds `a.mu` across this whole check-and-remove
— the provisioning check, `IsRunning`, `findCgroupDir`, and
`removeCgroupDir` — rather than releasing it after just the map read,
since dropping the lock in between would leave the same race open at a
narrower width; neither `IsRunning` nor `removeCgroupDir` takes `a.mu`
itself, so holding it here cannot deadlock.

Three alternatives were considered and rejected. Holding `a.mu` across
`provisionCgroup`, `cmd.Start()`, and the PID write would close the race
by serializing it away, but at the cost of serializing every concurrent
`Run` against a blocking `flock` and a fork/exec, and `Kill` also takes
`a.mu` and would stall behind it for the same span. A filesystem marker
was considered next, but not rejected as outright impossible: real
cgroupfs rejects creation of arbitrary regular files inside a cgroup
directory (only `mkdir` of sub-cgroups is supported), so the obvious
plain-file marker only works against the plain-directory fake the test
suite substitutes for real cgroupfs. Two variants that *are*
implementable on real cgroupfs were still not worth it here: a marker
sub-cgroup created with `mkdir` inside the per-Box directory adds a
second cgroup lifecycle to create, clean up, and reap-filter —
`findCgroupDir` and `IsRunning` would both have to learn to ignore it —
and an out-of-band state directory outside cgroupfs adds a second
source of truth that itself needs staleness handling after a launcher
crash, which is the very failure mode `Reap` exists to clean up. An
mtime-based grace window in `Reap` was also rejected: it would trade one
race for a time-fuzzed heuristic, and a window wide enough to cover a
slow launch would just as happily let a genuinely orphaned directory
survive a reap sweep.

This guard is in-process only, and that limitation is deliberate rather
than an oversight: a `Reap` issued from a *different* launcher process —
the cross-process case `findCgroupDir`'s whole-tree walk exists to serve
— cannot see this process's in-memory provisioning map, so a Box launched
by one launcher process could in principle still be reaped mid-launch by
a `Reap` call from another. In practice every in-tree `Reap` call today
is same-process: the only non-test caller in the tree is the OCI
adapter's own `Reap` call from `ociAdapter.Run`, and the `Runner.Reap`
interface method itself has no cross-process caller at all. The
in-process guard therefore covers every caller that exists today; the
cross-process case remains a latent gap that `findCgroupDir`'s
whole-tree walk makes reachable in principle, should a future caller
ever `Reap` a Box from a different process than the one that ran it.
