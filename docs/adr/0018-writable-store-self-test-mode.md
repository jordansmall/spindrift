# Writable-store self-test mode via nixStoreWritable + extraClosures

A Consumer whose checks are `nix flake check` cannot run them meaningfully
inside a default Box: `/nix/store` is baked read-only (chown reaches only
`nix/var`, ADR 0008), so single-user daemonless nix inside the Box gets
EACCES the moment it tries to substitute or build any path not already
present. The agent either guesses at correctness or round-trips CI to find
out — the exact gap `nixInBox` (ADR 0008) was meant to close, left open.

Two build-time `mkHarness` knobs close it:

- **`nixStoreWritable`** (default `false`) — the image additionally chowns
  the `/nix/store` *directory itself* (non-recursive; `chown 1000:1000
  nix/store`, no `-R`) to the agent uid. Existing baked paths stay
  root-owned and immutable — only the directory's own write bit changes, so
  the agent can add new entries but not rewrite existing ones. New paths
  substituted or built inside the Box land in the container's ephemeral
  copy-on-write layer, exactly like any other write inside the Box, and die
  with it.
- **`extraClosures`** (default `[]`) — a function of the (Linux) `pkgs`,
  mirroring `packages`, returning extra derivations whose closures are
  folded into the image `contents` and the `closureInfo` `rootPaths` used
  for store-DB registration (ADR 0008) alongside `agentEnv`/`agentFiles`.
  This lets a Consumer bake its check/dev closure so in-box nix treats those
  paths as already present instead of cold-substituting the world on every
  Box — independent of `nixStoreWritable`, and useful whether or not it is
  set.

## Threat model

The only mutable surface a writable store touches is the running
container's own copy-on-write layer:

- The **image itself** is never mutated — `nixStoreWritable` changes what
  `fakeRootCommands` bakes at *build* time (an ownership bit), not anything
  written at *run* time. Every Box built from the same image starts from
  the identical read-only base layers.
- **No shared mutable state** — nothing about this knob introduces a host
  bind-mount, a named volume, or any channel back to the launcher's host.
  A Box that substitutes or builds new store paths does so entirely inside
  its own throwaway container filesystem; a sibling Box, a future Box from
  the same image, and the host all see none of it.
- **Ephemeral by construction** — the container is disposable per the
  existing isolation model (ADR 0006); when it exits, the copy-on-write
  layer is discarded with it. There is no cleanup step because there is
  nothing to clean up.

This was chosen over two alternatives that both introduce a persistence
channel the threat model above deliberately avoids:

- **Mounting the image-build nix volume into agent Boxes** — would let a
  Box durably poison the same store the *next* Box's image build reads
  from, turning one compromised or buggy run into a supply-chain problem
  for every subsequent build. Rejected.
- **A host-store bind mount** (`/nix/store` from the launcher's host) —
  same durable-poisoning problem, plus it would tie a Box's correctness to
  whatever happens to be on the host at dispatch time, breaking the
  reproducible-image guarantee (ADR 0002) entirely. Rejected.

Because the trade is real even if bounded — anything the Box substitutes is
untrusted content running with the agent's own build/eval privileges for
the rest of that run — the knob must be loud, not just off by default: the
image bakes a `NIX_STORE_WRITABLE` env marker, and the entrypoint prints a
`==> WARNING` at Box start naming self-test mode and the trust caveat
(issue #469). This is an entrypoint/launcher concern, not a nix-layer one —
the nix layer keeps `throw`/`assert` only, per the existing warning idiom.

## bwrap limitation

This is OCI-runner behavior only. The bwrap runner (ADR 0006) binds the
host's `/nix/store` read-only directly into the sandbox
(`--ro-bind /nix/store /nix/store`, `cmd/launcher/internal/runner/bwrap.go`)
rather than baking a store into a built image — there is no writable
container layer to isolate a self-test write into, and no per-run image to
carry a chowned directory. Making the bwrap store writable would mean
writing into the *host's* real `/nix/store`, which breaks the
ephemeral/no-shared-mutable-state property this ADR relies on. `bwrap`
Consumers who want in-box `nix flake check` feedback should switch
`runtime` to an OCI runtime (`podman`/`docker`, the default) and opt into
`nixStoreWritable` there.

## Considered Options

- **Always writable** — simplest surface, but makes every default Box
  non-hermetic and silently at odds with the ADR 0008 baseline; rejected.
- **A separate self-test image variant** — a parallel `agent-image-selftest`
  output would keep the default image untouched, but doubles the image
  surface (two builds, two things to keep in sync) for what is a single
  ownership-bit difference; rejected in favor of one image with a
  conditional chown, the same shape `nixInBox` already established.
- **Runtime env knob instead of a build-time image arg** — would let a
  Consumer flip it via `harness.env` with no rebuild, but the chown is
  necessarily baked at image-build time (`fakeRootCommands` runs once, at
  build); a runtime flag that cannot change what it claims to control would
  be misleading. Kept as a build-time `mkHarness` arg, following the
  `nixInBox` pattern (build-time image args, not env-schema runtime knobs —
  no regen surface involved).

## Consequences

Consumers who opt in accept that a Box's in-container nix operations are no
longer hermetic for that run: a substituted or built path could in
principle be poisoned by anything reachable from the Box's network access,
and that path is now indistinguishable from a baked one to any tool running
later in the same Box. This is why the ADR 0008 default (`nixStoreWritable
= false`) is unchanged and why the entrypoint warns loudly rather than
staying silent. In exchange, a Consumer iterating on its own `flake.nix` or
CI configuration can run the real `nix flake check` inside a Box and see
real substitution/build errors immediately, instead of guessing or waiting
on a CI round-trip — the same value proposition `nixInBox` (ADR 0008)
established for the read side, now extended to the write side for
Consumers who explicitly ask for it.
