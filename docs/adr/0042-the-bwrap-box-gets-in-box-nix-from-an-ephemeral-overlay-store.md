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
