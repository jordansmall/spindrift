# bwrap Boxes run under a compiled syscall denylist

> **Renumbered from ADR 0045 (issue #3631).** Two unrelated ADRs had both
> been filed as 0045; `0045` now names only [ADR
> 0045](0045-registry-routes-declared-table-protocol-aware-mirror.md), the
> registry-routes decision. Any pre-#3631 reference to "ADR 0045" may mean
> either this document or that one.

Namespace and mount isolation (ADR 0042) bound what a bwrap Box can *see* and
*mount*, not what syscalls it can *issue*. bwrap on its own applies no
seccomp filter at all: a Box's uid-1000 process can call `ptrace`, `mount`,
`reboot`, `kexec_load`, and every other syscall the kernel exposes to an
unprivileged caller in its own user namespace, limited only by the kernel's
own unprivileged-namespace boundary rather than anything spindrift adds. This
is the gap issue #2670 closes: a compiled BPF filter, attached via bwrap's
own `--seccomp FD` flag, unconditional for every bwrap Box.

`lib/seccomp.nix` compiles the filter with libseccomp
(`seccomp_init`/`seccomp_rule_add`/`seccomp_export_bpf`) rather than hand-
assembling BPF, and the launcher (`bwrapAdapter.Run` in
`cmd/launcher/internal/runner/bwrap.go`) opens the resulting file and passes
its fd via `cmd.ExtraFiles`. Three questions had to be settled to get there.

- **Denylist, not a podman-style full allowlist.** podman's own default
  seccomp profile is an allowlist: every syscall not named is denied.
  Replicating that shape means enumerating every syscall the whole agent
  toolchain could ever need — an arbitrary Driver, an arbitrary Consumer's
  build tooling, transitively every compiler/package-manager/test-runner a
  Target repo's own toolchain might shell out to. That is not verifiable
  short of exhaustively exercising every combination, and a single missing
  entry is a false negative that silently breaks a Dispatch with an opaque
  `EPERM` deep in some unrelated tool. A denylist inverts the risk: it can
  only ever be too narrow, which is a documented gap to close later, never
  an outage sprung on a Consumer whose Dispatch happened to call an unlisted
  syscall. `lib/seccomp.nix`'s own header comment states this rationale
  inline, next to the list itself, so the two can never drift.

- **`clone`/`unshare`/`setns`/`personality` are excluded.** All four back
  namespace and personality manipulation podman's own profile restricts, but
  only for specific argument values (particular `clone` flags, particular
  `personality` values) — bare `clone(2)` is how every thread anywhere on
  the system gets created, `agent`'s own shell and toolchain included, so
  denying the syscall outright rather than the dangerous flag combinations
  breaks ordinary process/thread creation, not just the misuse case. Scoping
  the deny correctly needs argument-aware BPF rules — a `seccomp_rule_add`
  with a `SCMP_CMP` comparator against the flags argument, not a bare
  syscall-number deny — which this cut does not attempt. The four stay
  allowed rather than either breaking ordinary execution (deny outright) or
  landing an untested argument-matching rule; closing this gap is future
  work, not a decision made here.

- **No Consumer-facing on/off knob.** Unlike `MEMORY_LIMIT`/`PIDS_LIMIT`
  (`lib/env-schema.nix`), the filter has no `settings`-surfaced toggle: it is
  unconditional hardening, not a tunable. The denylist is deliberately
  conservative — every entry is a syscall no normal Driver/Consumer toolchain
  path calls (module loading, `ptrace`, raw device I/O, time-of-day
  manipulation, and similar) — so it needs no escape hatch the way, say, a
  network-egress restriction does. Nothing in the issue's acceptance
  criteria, and nothing observed in the toolchains spindrift already
  dispatches against, calls for a Consumer to turn this off; adding a knob
  nothing needs would just be another surface to keep in sync with
  `lib/env-schema.nix` and document.

- **Native architecture only.** `seccomp_init`'s default filter attribute
  covers the compiling host's own architecture; a multiarch Box (running
  32-bit binaries under compat syscall entry points, for instance) gets no
  coverage from this filter today. Extending to multiarch means adding
  `seccomp_arch_add` calls for each compat ABI the Box might exercise and is
  left as a documented follow-up, not attempted in this cut. (Amended by
  issue #6139: the filter now covers the compat ABIs; see the amendment
  below.)

- **Missing/unreadable filter file warns and proceeds without it, matching
  ADR 0042's own degrade-don't-lie precedent for cgroup delegation.** The
  filter is defense-in-depth layered on top of bwrap's existing namespace and
  mount isolation, not the sandbox's sole isolation mechanism, so a Box built
  without the filter is a hardening gap, not an unsafe launch. Refusing to
  launch over a missing filter file would make a nix-build/packaging problem
  into a hard outage for every Dispatch, for a filter that only ever narrows
  what an already-namespaced, already-mount-restricted process can do.

## Consequences

A Box's set of deniable syscalls is fixed at nix-build time — `deniedSyscalls`
in `lib/seccomp.nix` — and Consumers cannot extend or shrink it per-flake.
Closing the four excluded syscalls, or changing which architectures the
filter covers, means editing that list, its generator, and (for an
architecture) the per-arch probes in `nix/checks/seccomp.nix`, not exposing a
new knob.

The filter denies with `EPERM` (`SCMP_ACT_ERRNO(EPERM)`), not `SCMP_ACT_KILL`:
a Dispatch that trips a denied syscall gets a normal syscall failure its own
error handling can react to, not an unconditional process kill. This keeps a
denial legible in a Driver's own logs rather than surfacing as an
unexplained signal.

Because the denylist is deliberately narrow, it does not by itself defend
against every misuse ADR 0042's mount/namespace isolation already excludes
(reading the host's `/nix/store`, escaping to the host network namespace,
etc.) — those remain that ADR's job. This filter's contribution is the
kernel-level syscalls namespace/mount isolation alone does not reach:
`ptrace`, `mount`/`umount2`/`pivot_root`, module loading, raw device I/O, and
time-of-day/log manipulation, per the list in `lib/seccomp.nix`.

## Amendment (issue #5982): the filter reaches bwrap through pasta by path, not by fd

The decision above hands the filter to bwrap as an inherited fd 3 through
`cmd.ExtraFiles`. pasta closes every inherited fd before it execs its
command, so on the pasta chain (the default network mode) the filter never
arrived and every Box that had one died at startup with `bwrap: Can't read
seccomp data: Bad file descriptor`. On the pasta chain, pasta's command is now
a small `sh` trampoline that opens the filter path onto fd 3 and then execs
bwrap with `--seccomp 3`, so the open happens after pasta has finished
closing fds. The direct chain, with no pasta, is unchanged and still uses
`cmd.ExtraFiles`.

The launcher still opens the filter itself first and only emits the
trampoline when that open succeeds. An unopenable filter therefore still
degrades to a warning and no seccomp, as decided above, instead of reaching
the trampoline's redirect, which would fail the whole launch. The trampoline
reopens the path, so there is a small window where the file could vanish
between the launcher's open and the trampoline's. We accept it: the filter is
a nix store path, which does not change under a running launcher.

## Amendment (issue #6139): the filter covers the compat ABIs

The "Native architecture only" bullet above said a multiarch Box would get no
coverage from the filter. That was wrong: libseccomp's default bad-architecture
action is `SCMP_ACT_KILL`, so a process entering the kernel through a compat
ABI was not left unfiltered, it was killed with `SIGSYS` on its first syscall.
That broke the contract in `lib/seccomp.nix`, that the filter never breaks a
working Box outright.

The generator now calls `seccomp_arch_add` for the compat ABIs: i386 and x32
on x86_64, AArch32 on aarch64. Compat processes get the same denylist as native
ones, `EPERM` on a denied syscall and allow otherwise. Only a truly foreign
architecture still hits the kill default.

The denylist is the same only because it also names each compat-only form of a
denied syscall: `umount` (i386) for `umount2`, and `clock_settime64` and
`clock_adjtime64` (i386, AArch32) for their native counterparts. Without them
those calls reach ALLOW in the compat section. `vm86`, `vm86old`, and `stime`,
which are on the list but do not exist on x86_64, likewise now take effect on
i386, where they do. Adding a denied syscall therefore also means adding any
compat-only alias of it; the per-arch probes in `nix/checks/seccomp.nix` pin
only the aliases named today.

We did not set the bad-architecture action to `SCMP_ACT_ALLOW`. That would let
the i386 form of a denied syscall, such as `mount` or `ptrace` through
`int $0x80`, skip the denylist entirely, which defeats the filter.
