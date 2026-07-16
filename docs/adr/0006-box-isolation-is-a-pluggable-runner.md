# The Box's isolation is a pluggable runner; Linux gets a daemonless nix-store runner

spindrift isolates each Box in an OCI container (podman/docker) — the container
IS the boundary that makes `--dangerously-skip-permissions` safe. But an OCI
runtime is not the only way to get that boundary, and on Linux it is not the
most nix-native one. Isolation is therefore a *runner* seam, not a hardcoded
`podman run`: the OCI runner (build image → load → run) stays the portable
default, and a daemonless **bubblewrap** runner is offered on Linux that execs
the baked entrypoint straight against the nix store — `/nix/store` bind-mounted
read-only, a fresh tmpfs work dir, network kept for GitHub, the scoped token
injected — with no image tarball, no `load`, and no daemon.

Rootless podman already isolates via the same unprivileged user namespaces
bubblewrap uses, so this is the same kernel primitive with far less machinery,
not a weaker boundary. nix cannot *be* the runtime — it is an evaluation-time
builder, not a daemon (ADR 0005) — so "let nix do the podman operations" means
shrinking the runtime to a thin, daemonless exec, not deleting it.

## Considered Options

- **OCI-only (podman/docker), as today** — portable across Linux and
  macOS-with-a-VM, but drags the whole image build/`load` dance onto Linux hosts
  and CI where a store-native exec would do, and keeps a daemon in the loop;
  kept as the default, rejected as the *only* runner.
- **Drop isolation: `nix shell` + a temp dir on the host** — maximally simple
  and pure-nix, but forfeits the boundary that justifies
  `--dangerously-skip-permissions` for an agent that pushes and merges; rejected.
- **`systemd-nspawn`** — a capable daemonless isolator, but assumes systemd and
  usually root; heavier and less portable than bubblewrap for an unprivileged
  per-issue exec; rejected in favour of bwrap (revisit for hosts already on
  nspawn).
- **Make bubblewrap a drop-in `runtime = "bwrap"`** — the existing `runtime`
  knob assumes OCI verbs (`run`/`load`/`image exists`); bwrap has no image to
  load, so it is a distinct runner code path, not another OCI CLI name.

## Consequences

On Linux (including CI) a Box can run with zero container-image plumbing:
`dockerTools` and `load` fall away, leaving pure nix plus a bwrap exec. macOS
keeps the OCI+VM path, since Linux-namespace isolation there still needs a Linux
kernel (`podman machine`, or the reused nix-darwin `linux-builder` VM). Because
the runner is now a seam, `build` for the bwrap runner realizes the store
closure but skips the image/`load` step, and the entrypoint must not assume an
OCI filesystem layout — it already clones into a work dir and reads a mounted
prompt, both of which bwrap supplies as bind mounts.

`runtime = "rancher"` (issue #1274) adds a third OCI CLI name — Rancher
Desktop's containerd mode, driven via `nerdctl` — and is the first runtime
value where the knob value differs from the binary it execs (podman and
docker were value == binary). The `rancher → nerdctl` alias lives in exactly
one place in the runner package, consumed by both OCI adapter construction
and runtime validation; `runnerKind` is unchanged, since anything other than
`bwrap` already collapses to the `oci` family.

## Isolation trade-off

The bwrap runner is genuinely isolated for the normal threat model: separate
mount/PID/network namespaces, no host filesystem beyond the read-only store and
the throwaway work dir, and only the scoped token in scope. A misbehaving agent
or hostile build/test code in the cloned repo is contained to that sandbox —
which is the same isolation *class* rootless podman gives on a Linux host, since
podman there is also namespaces, not a VM. So bwrap is not a downgrade from
podman-on-Linux; it drops the daemon and image plumbing, not the boundary.

The single gap versus a VM is the shared host **kernel**: file/process/network
isolation holds, but a kernel- or namespace-level *escape* (a real vuln, not
routine misbehavior) reaches the host, whereas a VM would still have to defeat
the hypervisor. This is a difference in how *much* isolation, not *whether*. It
only bites when the Box runs genuinely untrusted code and you need defense
against kernel escape. For Boxes working repos you own, the container boundary
is the right tier. When VM-grade isolation is wanted, nest the bwrap runner
*inside* a Linux VM (a microVM, `podman machine`, or the `linux-builder`) — the
VM supplies the hard boundary, bwrap the clean daemonless per-issue exec.
